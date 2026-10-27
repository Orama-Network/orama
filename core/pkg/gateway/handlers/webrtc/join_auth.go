package webrtc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"go.uber.org/zap"
)

// joinRefusal is why a join was refused: the HTTP status and typed code for a
// socket that has not been upgraded, and the frame code for one that has.
type joinRefusal struct {
	status    int
	rpcCode   httputil.RPCErrorCode
	frameCode string
	message   string
	retryable bool
}

func (r *joinRefusal) write(w http.ResponseWriter) {
	opts := []httputil.RPCErrorOption{}
	if r.retryable {
		opts = append(opts, httputil.WithRetryable())
	}
	httputil.WriteRPCError(w, r.status, r.rpcCode, r.message, opts...)
}

const (
	codeAdmissionRequired httputil.RPCErrorCode = "WEBRTC_ADMISSION_REQUIRED"
	codeAdmissionExpired  httputil.RPCErrorCode = "WEBRTC_ADMISSION_EXPIRED"
	codeAdmissionRevoked  httputil.RPCErrorCode = "WEBRTC_ADMISSION_REVOKED"

	// storeTimeout bounds one read of the admission tables on the join path.
	storeTimeout = 5 * time.Second
)

// authorizeJoin decides whether the caller may join room and, if so, signs the
// ticket the SFU takes the caller's identity from. A namespace that requires
// admission admits only a user whose admission its functions issued for this
// room and this device, and has not expired or been revoked.
func (h *WebRTCHandlers) authorizeJoin(ctx context.Context, r *http.Request, ns, room string) (string, *joinRefusal) {
	who, ok := callerOf(r)
	if !ok {
		return "", &joinRefusal{status: http.StatusUnauthorized, rpcCode: httputil.ErrCodeUnauthorized, frameCode: "unauthenticated",
			message: "WebRTC signalling needs a signed-in user's token"}
	}
	if h.admissions == nil || h.controlKey == nil {
		h.logger.ComponentError(logging.ComponentGeneral, "WebRTC join refused: the gateway has no admission store or no TURN secret to sign SFU tickets with",
			zap.String("namespace", ns))
		return "", unavailable("this gateway cannot admit WebRTC joins: it has no admission store or TURN secret")
	}

	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	// The issue time is taken before the admission is read, not after: a kick
	// stamps its time after it commits the revocation, so a ticket stamped
	// later than the read could carry a time after a kick it did not see.
	now := h.now()
	adm, admitExp, admitGen, ref := h.checkAdmission(ctx, ns, room, who)
	if ref != nil {
		return "", ref
	}

	ticket, err := ctrlauth.Ticket{
		Namespace: ns, Room: room, UserID: who.UserID, DeviceID: who.DeviceID, Muted: adm.Muted, VideoMuted: adm.VideoMuted,
		EventSink: h.eventSink, IssuedAtMs: now.UnixMilli(), Expires: now.Add(ctrlauth.TicketTTL).Unix(),
		AdmitExp: admitExp, AdmitGen: admitGen,
	}.Seal(h.controlKey)
	if err != nil {
		h.logger.ComponentError(logging.ComponentGeneral, "Failed to sign a WebRTC join ticket", zap.String("namespace", ns), zap.Error(err))
		return "", unavailable("failed to sign the join ticket for the SFU")
	}
	return ticket, nil
}

// checkAdmission applies the namespace's policy. adm carries whether the
// namespace has muted the caller's audio or video in this room, which the SFU
// enforces from the start. admitExp is the unix second the admission ends when
// the namespace requires one (0 otherwise): the SFU ends the session then.
// admitGen is the generation of that admission, which the SFU weighs against a
// kick's.
func (h *WebRTCHandlers) checkAdmission(ctx context.Context, ns, room string, who caller) (adm Admission, admitExp, admitGen int64, ref *joinRefusal) {
	require, err := h.admissions.RequireAdmission(ctx, ns)
	if err != nil {
		return Admission{}, 0, 0, h.storeFailure(ns, err)
	}
	adm, err = h.admissions.Lookup(ctx, ns, room, who.UserID, who.DeviceID)
	if err != nil {
		return Admission{}, 0, 0, h.storeFailure(ns, err)
	}
	if !require {
		return adm, 0, 0, nil
	}
	switch {
	case adm.Valid:
		return adm, adm.ValidUntil, adm.Generation, nil
	case adm.Revoked:
		return Admission{}, 0, 0, refusedAdmission(codeAdmissionRevoked, "admission_revoked", "the namespace revoked your admission to this room")
	case adm.Expired:
		return Admission{}, 0, 0, refusedAdmission(codeAdmissionExpired, "admission_expired", "your admission to this room has expired; ask the application for a new one")
	}
	return Admission{}, 0, 0, refusedAdmission(codeAdmissionRequired, "admission_required",
		fmt.Sprintf("room %q admits only users the application admitted to it", room))
}

func refusedAdmission(code httputil.RPCErrorCode, frame, message string) *joinRefusal {
	return &joinRefusal{status: http.StatusForbidden, rpcCode: code, frameCode: frame, message: message}
}

func unavailable(message string) *joinRefusal {
	return &joinRefusal{status: http.StatusServiceUnavailable, rpcCode: httputil.ErrCodeServiceUnavailable,
		frameCode: "admission_unavailable", message: message, retryable: true}
}

func (h *WebRTCHandlers) storeFailure(ns string, err error) *joinRefusal {
	h.logger.ComponentError(logging.ComponentGeneral, "WebRTC admission check failed", zap.String("namespace", ns), zap.Error(err))
	if errors.Is(err, errAdmissionSchema) {
		return &joinRefusal{status: http.StatusInternalServerError, rpcCode: httputil.ErrCodeInternal,
			frameCode: "admission_unavailable", message: err.Error()}
	}
	return unavailable("the namespace's admission records could not be read; retry")
}
