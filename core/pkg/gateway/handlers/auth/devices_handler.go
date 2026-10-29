package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// An account's devices.
//
// GET    /v1/auth/devices          the caller's account's devices
// DELETE /v1/auth/devices/{id}     revoke one (DeviceRevokeRequest)
// POST   /v1/auth/devices/approve  approve a pending device link from this device
//
// Whose devices comes from the caller's own token, never from the request. A
// token bound to a device can act only while that device is active, and a
// revocation from one carries the device's proof: an access token lifted off a
// device must not be able to sign the account out of every other one. A token
// bound to no device is the account's own session — the wallet's — and may act
// on any of the account's devices — where the namespace lets the wallet alone
// be the account, which is the optional policy (or a wallet that operates the
// namespace, which is held to it). Under any other policy sessionOwner refuses
// such a token. A pending device's restricted session reaches none of these
// routes.

// DeviceApproveRequest approves a pending device link. The proof is the
// approving device's, over the user code (see authsvc.DeviceProofMessage).
type DeviceApproveRequest struct {
	UserCode    string               `json:"user_code"`
	DeviceProof *authsvc.DeviceProof `json:"device_proof"`
}

// DevicesHandler lists the caller's devices.
func (h *Handlers) DevicesHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed (GET)")
		return
	}
	claims, namespace, ok := h.deviceCaller(w, r)
	if !ok {
		return
	}
	devices, err := h.authService.ListDevices(r.Context(), namespace, claims.Sub)
	if err != nil {
		h.writeUnavailable(w, "read the devices", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace": namespace,
		"subject":   claims.Sub,
		"devices":   deviceViews(devices, claims.Did),
	})
}

// deviceViews describes devices without their keys. current names the one the
// caller's own session is bound to.
func deviceViews(devices []authsvc.SessionDevice, current string) []map[string]any {
	out := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		out = append(out, map[string]any{
			"id":           d.ID,
			"subject":      d.Subject,
			"label":        d.Label,
			"state":        string(d.State),
			"approved_by":  d.ApprovedBy,
			"created_at":   formatSessionTime(d.CreatedAt),
			"activated_at": formatSessionTime(d.ActivatedAt),
			"revoked_at":   formatSessionTime(d.RevokedAt),
			"current":      current != "" && d.ID == current,
		})
	}
	return out
}

// DeviceProofRequest carries a device's proof on a request that has no other
// body — revoking a device, ending a session.
type DeviceProofRequest struct {
	DeviceProof *authsvc.DeviceProof `json:"device_proof"`
}

// requireCallerDeviceProof checks, when the caller's token is bound to a
// device, that the device signed for this action on this target. A caller
// bound to no device needs none. It writes the refusal itself.
func (h *Handlers) requireCallerDeviceProof(w http.ResponseWriter, r *http.Request, namespace string, claims *authsvc.JWTClaims, action, target string) bool {
	if claims.Did == "" {
		return true
	}
	var req DeviceProofRequest
	// An empty body (chunked or not) carries no proof; a device-bound caller
	// is then refused below for the proof it did not send.
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body: expected {\"device_proof\": {...}}")
		return false
	}
	return h.verifyCallerDeviceProof(w, r, namespace, claims, action, target, req.DeviceProof)
}

// verifyCallerDeviceProof checks that the device the caller's token is bound
// to is still active and signed proof for this action on this target, and
// spends the proof. It writes the refusal itself.
func (h *Handlers) verifyCallerDeviceProof(w http.ResponseWriter, r *http.Request, namespace string, claims *authsvc.JWTClaims, action, target string, proof *authsvc.DeviceProof) bool {
	_, ok := h.callerDeviceWithProof(w, r, namespace, claims, action, target, proof)
	return ok
}

// callerDeviceWithProof is verifyCallerDeviceProof returning the device that
// signed.
func (h *Handlers) callerDeviceWithProof(w http.ResponseWriter, r *http.Request, namespace string, claims *authsvc.JWTClaims, action, target string, proof *authsvc.DeviceProof) (*authsvc.SessionDevice, bool) {
	device, err := h.authService.RequireActiveDevice(r.Context(), namespace, claims.Sub, claims.Did)
	if err == nil {
		var key *authsvc.DeviceKey
		if key, err = device.Key(); err == nil {
			err = h.authService.VerifyDeviceProof(r.Context(), key, action, namespace, target, proof)
		}
	}
	if err != nil {
		if !writeDeviceRefusal(w, err) {
			h.writeUnavailable(w, "check the device proof", err)
		}
		return nil, false
	}
	return device, true
}

// DeviceRevokeRequest revokes a device. The proof is the calling device's,
// over the revoked device's id, and is required from a session bound to a
// device. Evidence is the application's (an account-signed roster entry, for
// example): the gateway keeps it with the tombstone, verbatim, and shows it to
// the revoked device in its DEVICE_REVOKED refusals.
type DeviceRevokeRequest struct {
	DeviceProof *authsvc.DeviceProof `json:"device_proof"`
	Evidence    string               `json:"evidence"`
}

// jsonEscapeExpansion is the most a byte of text can grow when JSON escapes
// it: a control byte is written \u00XX, six bytes for one.
const jsonEscapeExpansion = 6

// revokeProofRoom is the room the device proof and the rest of the object take
// around the evidence.
const revokeProofRoom = 4096

// revokeBodyLimit is what a revocation's body may be: evidence at its bound,
// however its client escaped it, and room for the proof. The bound itself is
// checked on the decoded evidence (authsvc.ValidRevocationEvidence); this only
// stops a body that cannot hold valid evidence from being read whole.
const revokeBodyLimit = authsvc.MaxRevocationEvidenceBytes*jsonEscapeExpansion + revokeProofRoom

// readRevokeRequest reads a revocation's body, which may be empty. It writes
// the refusal itself.
func readRevokeRequest(w http.ResponseWriter, r *http.Request) (DeviceRevokeRequest, bool) {
	var req DeviceRevokeRequest
	r.Body = http.MaxBytesReader(w, r.Body, revokeBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest,
			"invalid json body: expected {\"device_proof\": {...}, \"evidence\": \"...\"}, evidence at most "+
				strconv.Itoa(authsvc.MaxRevocationEvidenceBytes)+" bytes")
		return req, false
	}
	if err := authsvc.ValidRevocationEvidence(req.Evidence); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return req, false
	}
	return req, true
}

// DeviceByIDHandler revokes one device, or approves a device link.
func (h *Handlers) DeviceByIDHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/auth/devices/"), "/")
	switch {
	case rest == "approve" && r.Method == http.MethodPost:
		h.approveDeviceLink(w, r)
	case authsvc.ValidDeviceID(rest) && r.Method == http.MethodDelete:
		h.revokeDevice(w, r, rest)
	default:
		writeError(w, http.StatusMethodNotAllowed,
			"DELETE /v1/auth/devices/{id} revokes a device; POST /v1/auth/devices/approve approves a device link")
	}
}

func (h *Handlers) revokeDevice(w http.ResponseWriter, r *http.Request, id string) {
	claims, namespace, ok := h.deviceCaller(w, r)
	if !ok {
		return
	}
	req, ok := readRevokeRequest(w, r)
	if !ok {
		return
	}
	// A session bound to no device is the wallet's own (deviceCaller let it
	// through only where the wallet alone is the account), and signs nothing.
	how := authsvc.DeviceRevocation{By: authsvc.RevokedByAccount, Evidence: req.Evidence}
	if claims.Did != "" {
		revoker, ok := h.callerDeviceWithProof(w, r, namespace, claims, authsvc.DeviceProofRevoke, id, req.DeviceProof)
		if !ok {
			return
		}
		how = authsvc.DeviceRevocationByDevice(namespace, revoker, req.DeviceProof, req.Evidence)
	}
	if err := h.authService.RevokeDevice(r.Context(), namespace, claims.Sub, id, how); err != nil {
		if !writeDeviceRefusal(w, err) {
			h.writeUnavailable(w, "revoke the device", err)
		}
		return
	}
	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     claims.Sub,
		Action:    authsvc.AuditDeviceRevoked,
		Result:    authsvc.AuditSuccess,
		Resource:  id,
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "id": id})
}

func (h *Handlers) approveDeviceLink(w http.ResponseWriter, r *http.Request) {
	claims, namespace, ok := h.deviceCaller(w, r)
	if !ok {
		return
	}
	if claims.Did == "" {
		writeDeviceRefusal(w, authsvc.ErrDeviceProofRequired)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req DeviceApproveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: expected {\"user_code\": \"...\", \"device_proof\": {...}}")
		return
	}
	code, err := authsvc.NormalizeUserCode(req.UserCode)
	if err != nil {
		h.writeDeviceError(w, "approve the device link", err)
		return
	}
	if err := h.approveAsDevice(r, namespace, claims, code, req.DeviceProof); err != nil {
		h.recordDeviceApproval(r, namespace, claims, authsvc.AuditFailure, err)
		if !writeDeviceRefusal(w, err) {
			h.writeDeviceError(w, "approve the device link", err)
		}
		return
	}
	h.recordDeviceApproval(r, namespace, claims, authsvc.AuditSuccess, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "namespace": namespace, "subject": claims.Sub})
}

// approveAsDevice checks the approver is an active device of the account that
// signed for this user code, then approves.
func (h *Handlers) approveAsDevice(r *http.Request, namespace string, claims *authsvc.JWTClaims, code string, proof *authsvc.DeviceProof) error {
	ctx := r.Context()
	approver, err := h.authService.RequireActiveDevice(ctx, namespace, claims.Sub, claims.Did)
	if err != nil {
		return err
	}
	key, err := approver.Key()
	if err != nil {
		return err
	}
	if err := h.authService.VerifyDeviceProof(ctx, key, authsvc.DeviceProofApprove, namespace, code, proof); err != nil {
		return err
	}
	return h.authService.ApproveDeviceLink(ctx, code, namespace, approver)
}

func (h *Handlers) recordDeviceApproval(r *http.Request, namespace string, claims *authsvc.JWTClaims, result string, err error) {
	event := authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     claims.Sub,
		Action:    authsvc.AuditDeviceLoginApproved,
		Result:    result,
		Metadata:  map[string]string{"approver_device": claims.Did},
	}
	if err != nil {
		event.Metadata["reason"] = err.Error()
	}
	h.authService.Audit().RecordFromRequest(r.Context(), r, event)
}

// deviceCaller is whose devices these are: the caller's token's subject, in
// its namespace. A token bound to a device acts only while that device is
// active — a pending device's session is refused by the route policy and by
// sessionOwner, and a revoked one's tokens are refused before they get here,
// so this is the backstop, not the gate.
func (h *Handlers) deviceCaller(w http.ResponseWriter, r *http.Request) (*authsvc.JWTClaims, string, bool) {
	subject, namespace, ok := h.sessionOwner(w, r)
	if !ok {
		return nil, "", false
	}
	claims, _ := r.Context().Value(CtxKeyJWT).(*authsvc.JWTClaims)
	if claims == nil || claims.Sub != subject {
		writeError(w, http.StatusForbidden, "devices belong to a signed-in account")
		return nil, "", false
	}
	if claims.Did != "" {
		if _, err := h.authService.RequireActiveDevice(r.Context(), namespace, subject, claims.Did); err != nil {
			if !writeDeviceRefusal(w, err) {
				h.writeUnavailable(w, "read the calling device", err)
			}
			return nil, "", false
		}
	}
	return claims, namespace, true
}
