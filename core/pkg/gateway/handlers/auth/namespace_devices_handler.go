package auth

import (
	"net/http"
	"strings"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// A namespace operator's view of its users' devices: the way back when a user
// cannot help themselves.
//
// GET    /v1/namespace/devices?subject=<wallet>  an account's devices
// DELETE /v1/namespace/devices/{id}              revoke one of them ({"evidence"} optional)
//
// Under the approval policy an account that lost its only device, or whose
// stolen device revoked the others, has nothing left that can approve a new
// one — and a wallet signature alone cannot, by design, since that is the
// credential the policy refuses to trust on its own. An operator (the route
// requires the members-write permission) revokes what the user no longer
// holds; with no active device left, the user's next sign-in enrols its device
// as the account's first again.

// NamespaceDevicesHandler lists one account's devices.
func (h *Handlers) NamespaceDevicesHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed (GET /v1/namespace/devices?subject=<wallet>)")
		return
	}
	subject := authsvc.NormalizeWallet(r.URL.Query().Get("subject"))
	if subject == "" {
		writeError(w, http.StatusBadRequest, "whose devices: ?subject=<wallet>")
		return
	}
	namespace := h.callerNamespace(r)
	devices, err := h.authService.ListDevices(r.Context(), namespace, subject)
	if err != nil {
		h.writeUnavailable(w, "read the devices", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace": namespace,
		"subject":   subject,
		"devices":   deviceViews(devices, ""),
	})
}

// NamespaceDeviceByIDHandler revokes one device of any account in the
// namespace.
func (h *Handlers) NamespaceDeviceByIDHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/namespace/devices/"), "/")
	if r.Method != http.MethodDelete || !authsvc.ValidDeviceID(id) {
		writeError(w, http.StatusMethodNotAllowed, "DELETE /v1/namespace/devices/{id}, with an id from GET /v1/namespace/devices")
		return
	}
	// The operator's body may carry evidence for the application. A device
	// proof means nothing here: the operator is no device of the account.
	req, ok := readRevokeRequest(w, r)
	if !ok {
		return
	}
	if req.DeviceProof != nil {
		writeError(w, http.StatusBadRequest, "an operator's revocation carries no device_proof; send {\"evidence\": \"...\"} or no body")
		return
	}
	namespace := h.callerNamespace(r)
	device, err := h.authService.Device(r.Context(), namespace, id)
	if err == nil {
		err = h.authService.RevokeDevice(r.Context(), namespace, device.Subject, id,
			authsvc.DeviceRevocation{By: authsvc.RevokedByOperator, Evidence: req.Evidence})
	}
	if err != nil {
		if !writeDeviceRefusal(w, err) {
			h.writeUnavailable(w, "revoke the device", err)
		}
		return
	}
	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     authsvc.ActorFromRequest(r),
		Action:    authsvc.AuditDeviceRevoked,
		Result:    authsvc.AuditSuccess,
		Resource:  id,
		Metadata:  map[string]string{"subject": device.Subject, "by": "operator"},
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "id": id, "subject": device.Subject})
}

// callerNamespace is the namespace the caller's credential belongs to.
func (h *Handlers) callerNamespace(r *http.Request) string {
	if ns, ok := r.Context().Value(CtxKeyNamespaceOverride).(string); ok && ns != "" {
		return ns
	}
	return h.defaultNS
}
