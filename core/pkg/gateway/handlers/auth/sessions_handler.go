package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// Seeing and ending the sessions signed in as you.
//
// GET  /v1/auth/sessions      the caller's own live sessions
// DELETE /v1/auth/sessions/{id}  end one of them
//
// The subject comes from the caller's own token, never from the request, so
// there is no version of these that reaches somebody else's sessions.

// SessionsHandler lists the caller's live sessions.
func (h *Handlers) SessionsHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed (GET)")
		return
	}

	subject, namespace, ok := h.sessionOwner(w, r)
	if !ok {
		return
	}

	sessions, err := h.authService.ListSessions(r.Context(), namespace, subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, map[string]any{
			"id":         session.ID,
			"subject":    session.Subject,
			"audience":   session.Audience,
			"created_at": formatSessionTime(session.CreatedAt),
			"expires_at": formatSessionTime(session.ExpiresAt),
			"device_id":  session.DeviceID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace": namespace,
		"subject":   subject,
		"sessions":  out,
	})
}

// SessionByIDHandler ends one session.
func (h *Handlers) SessionByIDHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed (DELETE /v1/auth/sessions/{id})")
		return
	}

	raw := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/auth/sessions/"), "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "which session: DELETE /v1/auth/sessions/{id}, as listed by GET /v1/auth/sessions")
		return
	}

	subject, namespace, ok := h.sessionOwner(w, r)
	if !ok {
		return
	}
	claims, _ := r.Context().Value(CtxKeyJWT).(*authsvc.JWTClaims)
	if !h.requireCallerDeviceProof(w, r, namespace, claims, authsvc.DeviceProofEndSession, strconv.FormatInt(id, 10)) {
		return
	}

	// A session that predates session ids is ended too; it just cannot be
	// ended all at once, and the response says so.
	endErr := h.authService.EndSession(r.Context(), namespace, subject, id)
	if endErr != nil && !errors.Is(endErr, authsvc.ErrSessionTokensOutlive) {
		writeError(w, http.StatusNotFound, endErr.Error())
		return
	}

	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     subject,
		Action:    authsvc.AuditLoggedOut,
		Result:    authsvc.AuditSuccess,
		Metadata:  map[string]string{"session": strconv.FormatInt(id, 10)},
	})

	body := map[string]any{"status": "ended", "id": id}
	if endErr != nil {
		body["warning"] = endErr.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// sessionOwner is whose sessions these are — and whose devices, which
// deviceCaller asks it.
//
// A JWT names its subject. An API key does not name a wallet — it is a
// credential in its own right — so it cannot be used to list or end the
// sessions of the wallet that happened to mint it.
//
// Two tokens that do name a wallet are not the account's to act with either:
//
//   - a pending device's restricted session, which has no say over the
//     account's other sessions or devices (the route policy refuses it before
//     it gets here; this is the backstop);
//   - a token bound to no device, in a namespace whose policy requires one for
//     this wallet. Such a token acts on every device with no proof, which is
//     what a session bound to no device is for under optional, where the wallet
//     alone is the account. Under any other policy an end user is issued no
//     such session and cannot refresh one, so the only ones left are minted
//     before the policy was set, for the rest of their fifteen minutes: they are
//     refused here rather than left to revoke the devices the policy now
//     protects.
func (h *Handlers) sessionOwner(w http.ResponseWriter, r *http.Request) (subject, namespace string, ok bool) {
	ctx := r.Context()
	namespace = h.servedNamespace(r)

	claims, cast := ctx.Value(CtxKeyJWT).(*authsvc.JWTClaims)
	if !cast || claims == nil || strings.TrimSpace(claims.Sub) == "" {
		writeError(w, http.StatusForbidden,
			"sessions belong to a signed-in wallet: call this with a token from 'orama auth login', not with an API key")
		return "", "", false
	}
	if claims.PendingDevice() {
		writeDeviceRefusal(w, authsvc.ErrDevicePendingApproval)
		return "", "", false
	}
	if claims.Did == "" && !h.requireNoDevicePolicy(w, r, namespace, claims.Sub) {
		return "", "", false
	}
	return claims.Sub, namespace, true
}

// servedNamespace is the namespace this gateway serves the request for: the
// namespace gateway's own, or this one's default.
func (h *Handlers) servedNamespace(r *http.Request) string {
	if s, cast := r.Context().Value(CtxKeyNamespaceOverride).(string); cast && s != "" {
		return s
	}
	return h.defaultNS
}

// formatSessionTime renders a timestamp, or nothing when the column was empty.
func formatSessionTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at.UTC().Format(time.RFC3339)
}
