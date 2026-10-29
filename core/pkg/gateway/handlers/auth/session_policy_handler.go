package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"go.uber.org/zap"
)

// GET /v1/namespace/session-policy   the namespace's device and sign-in policy,
//                                    the topics its end users read and its
//                                    per-IP limits
// PUT /v1/namespace/session-policy   {"device_policy": "optional"|"required"|"approval"|"function",
//                                     "sign_in": "members"|"open",
//                                     "end_user_topics": ["presence/*", ...],
//                                     "per_ip_limits": "on"|"off",
//                                     "new_wallets_per_ip_hour": 20,
//                                     "invokes_per_ip_minute": 1200}
//
// Every field of a PUT is optional but at least one is sent, and one left out
// keeps what it was. All are validated before any is written. end_user_topics
// replaces the list, and [] clears it. The two ceilings are kept while
// per_ip_limits is off, and apply once it is on.
//
// The route policy requires the namespace-write permission; this handler reads
// the namespace from the caller's credential, never from the request.

// SessionPolicyRequest sets a namespace's device policy, its sign-in policy, or
// both. A field left out is left as it is.
type SessionPolicyRequest struct {
	DevicePolicy        *string   `json:"device_policy"`
	SignIn              *string   `json:"sign_in"`
	EndUserTopics       *[]string `json:"end_user_topics"`
	PerIPLimits         *string   `json:"per_ip_limits"`
	NewWalletsPerIPHour *int      `json:"new_wallets_per_ip_hour"`
	InvokesPerIPMinute  *int      `json:"invokes_per_ip_minute"`
}

// maxSessionPolicyBody bounds a PUT: a full end-user topic list
// (authsvc.MaxEndUserTopics patterns of up to 256 characters) and the two
// policies, JSON-quoted.
const maxSessionPolicyBody = 24 << 10

// SessionPolicyHandler reads or sets the calling namespace's session policy.
func (h *Handlers) SessionPolicyHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	namespace := h.callerNamespace(r)
	if authsvc.IsLobbyNamespace(namespace) {
		writeError(w, http.StatusForbidden, "the lobby has no session policy; set one on a namespace you own")
		return
	}

	switch r.Method {
	case http.MethodGet:
		if body, ok := h.sessionPolicyBody(w, r, namespace); ok {
			writeJSON(w, http.StatusOK, body)
		}
	case http.MethodPut:
		h.setSessionPolicy(w, r, namespace)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET to read, PUT {\"device_policy\": ..., \"sign_in\": ..., \"end_user_topics\": [...], \"per_ip_limits\": ...} to set")
	}
}

// sessionPolicyBody is the namespace's policy as recorded now. It answers the
// request itself when the registry cannot be read.
func (h *Handlers) sessionPolicyBody(w http.ResponseWriter, r *http.Request, namespace string) (map[string]any, bool) {
	device, err := h.authService.DevicePolicyOf(r.Context(), namespace)
	if err != nil {
		h.writeUnavailable(w, "read the session policy", err)
		return nil, false
	}
	signIn, err := h.authService.SignInPolicyOf(r.Context(), namespace)
	if err != nil {
		h.writeUnavailable(w, "read the sign-in policy", err)
		return nil, false
	}
	topics, err := h.authService.EndUserTopicsOf(r.Context(), namespace)
	if err != nil {
		h.writeUnavailable(w, "read the end-user topics", err)
		return nil, false
	}
	limits, err := h.authService.PerIPLimitsOf(r.Context(), namespace)
	if err != nil {
		h.writeUnavailable(w, "read the per-IP limits", err)
		return nil, false
	}
	return map[string]any{"namespace": namespace, "device_policy": string(device), "sign_in": string(signIn),
		"end_user_topics": topics, "per_ip_limits": authsvc.PerIPLimitsSwitch(limits.Enabled),
		"new_wallets_per_ip_hour": limits.NewWalletsPerHour, "invokes_per_ip_minute": limits.InvokesPerMinute}, true
}

// sessionPolicyChange is a validated PUT. A nil field is one the request did
// not send.
type sessionPolicyChange struct {
	device        *authsvc.DevicePolicy
	signIn        *authsvc.SignInPolicy
	endUserTopics *[]string
	perIPLimits   authsvc.PerIPLimitsChange
}

// parseSessionPolicyRequest reads and validates a PUT.
func parseSessionPolicyRequest(w http.ResponseWriter, r *http.Request) (sessionPolicyChange, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSessionPolicyBody)
	var req SessionPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: expected {\"device_policy\": \"optional|required|approval|function\", \"sign_in\": \"members|open\", \"end_user_topics\": [\"pattern\", ...], \"per_ip_limits\": \"on|off\", \"new_wallets_per_ip_hour\": n, \"invokes_per_ip_minute\": n}")
		return sessionPolicyChange{}, false
	}
	if req.DevicePolicy == nil && req.SignIn == nil && req.EndUserTopics == nil &&
		req.PerIPLimits == nil && req.NewWalletsPerIPHour == nil && req.InvokesPerIPMinute == nil {
		writeError(w, http.StatusBadRequest, "send device_policy (optional|required|approval|function), sign_in (members|open), end_user_topics (a list of topic patterns), per_ip_limits (on|off), new_wallets_per_ip_hour, invokes_per_ip_minute, or several")
		return sessionPolicyChange{}, false
	}
	var change sessionPolicyChange
	if req.DevicePolicy != nil {
		p, err := authsvc.ParseDevicePolicy(*req.DevicePolicy)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return sessionPolicyChange{}, false
		}
		change.device = &p
	}
	if req.SignIn != nil {
		p, err := authsvc.ParseSignInPolicy(*req.SignIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return sessionPolicyChange{}, false
		}
		change.signIn = &p
	}
	if req.EndUserTopics != nil {
		topics, err := authsvc.ParseEndUserTopics(*req.EndUserTopics)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return sessionPolicyChange{}, false
		}
		change.endUserTopics = &topics
	}
	limits, err := parsePerIPLimitsChange(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return sessionPolicyChange{}, false
	}
	change.perIPLimits = limits
	return change, true
}

// parsePerIPLimitsChange reads and validates the per-IP limit fields of a PUT.
func parsePerIPLimitsChange(req SessionPolicyRequest) (authsvc.PerIPLimitsChange, error) {
	change := authsvc.PerIPLimitsChange{NewWalletsPerHour: req.NewWalletsPerIPHour, InvokesPerMinute: req.InvokesPerIPMinute}
	if req.PerIPLimits != nil {
		on, err := authsvc.ParsePerIPLimitsSwitch(*req.PerIPLimits)
		if err != nil {
			return authsvc.PerIPLimitsChange{}, err
		}
		change.Enabled = &on
	}
	return change, change.Validate()
}

func (h *Handlers) setSessionPolicy(w http.ResponseWriter, r *http.Request, namespace string) {
	change, ok := parseSessionPolicyRequest(w, r)
	if !ok {
		return
	}
	device, signIn := change.device, change.signIn
	actor := authsvc.ActorFromRequest(r)
	// The topics and the per-IP limits go first and opening sign-in last, so
	// a request that opens it with them never leaves it open without them.
	if change.endUserTopics != nil && !h.applyEndUserTopics(w, r, namespace, *change.endUserTopics, actor) {
		return
	}
	if !change.perIPLimits.Empty() && !h.applyPerIPLimits(w, r, namespace, change.perIPLimits, actor) {
		return
	}
	// The two fields are two writes. Opening sign-in goes last and closing it
	// first, so a request that fails between them leaves the namespace closed,
	// never open under a device policy the request meant to replace.
	opening := signIn != nil && *signIn == authsvc.SignInOpen
	if signIn != nil && !opening && !h.applySignInPolicy(w, r, namespace, *signIn, actor) {
		return
	}
	revokedKeys, sweepErr := 0, error(nil)
	if device != nil {
		var ok bool
		if revokedKeys, sweepErr, ok = h.applyDevicePolicy(w, r, namespace, *device, actor); !ok {
			return
		}
	}
	if opening && !h.applySignInPolicy(w, r, namespace, *signIn, actor) {
		return
	}
	h.writeSessionPolicyResult(w, r, namespace, device != nil, revokedKeys, sweepErr)
}

// applySignInPolicy records a sign-in policy and audits it. It answers the
// request itself when the write fails.
func (h *Handlers) applySignInPolicy(w http.ResponseWriter, r *http.Request, namespace string, policy authsvc.SignInPolicy, actor string) bool {
	if err := h.authService.SetSignInPolicy(r.Context(), namespace, policy, actor); err != nil {
		h.writeUnavailable(w, "record the sign-in policy", err)
		return false
	}
	h.recordSignInPolicy(r, namespace, actor, policy)
	return true
}

// applyEndUserTopics records the topics a namespace's end users read and
// audits the change: naming a topic opens it to every wallet that signs in.
// It answers the request itself when the write fails.
func (h *Handlers) applyEndUserTopics(w http.ResponseWriter, r *http.Request, namespace string, topics []string, actor string) bool {
	if _, err := h.authService.SetEndUserTopics(r.Context(), namespace, topics, actor); err != nil {
		h.writeUnavailable(w, "record the end-user topics", err)
		return false
	}
	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     actor,
		Action:    authsvc.AuditSessionPolicySet,
		Result:    authsvc.AuditSuccess,
		Resource:  "end_user_topics",
		Metadata:  map[string]string{"end_user_topics": strings.Join(topics, ",")},
	})
	return true
}

// applyPerIPLimits records a change to the namespace's per-IP limits and
// audits it. It answers the request itself when the write fails.
func (h *Handlers) applyPerIPLimits(w http.ResponseWriter, r *http.Request, namespace string, change authsvc.PerIPLimitsChange, actor string) bool {
	limits, err := h.authService.SetPerIPLimits(r.Context(), namespace, change, actor)
	if err != nil {
		h.writeUnavailable(w, "record the per-IP limits", err)
		return false
	}
	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     actor,
		Action:    authsvc.AuditSessionPolicySet,
		Result:    authsvc.AuditSuccess,
		Resource:  "per_ip_limits",
		Metadata: map[string]string{
			"per_ip_limits":           authsvc.PerIPLimitsSwitch(limits.Enabled),
			"new_wallets_per_ip_hour": strconv.Itoa(limits.NewWalletsPerHour),
			"invokes_per_ip_minute":   strconv.Itoa(limits.InvokesPerMinute),
		},
	})
	return true
}

// applyDevicePolicy records a device policy and audits it. A sweep of the end
// users' sign-in keys that stopped partway is returned, not answered: the
// policy is set, and the rest of the request goes on.
func (h *Handlers) applyDevicePolicy(w http.ResponseWriter, r *http.Request, namespace string, policy authsvc.DevicePolicy, actor string) (int, error, bool) {
	revokedKeys, err := h.authService.SetDevicePolicy(r.Context(), namespace, policy, actor)
	sweepIncomplete := errors.Is(err, authsvc.ErrSignInKeySweepIncomplete)
	if err != nil && !sweepIncomplete {
		h.writeUnavailable(w, "record the session policy", err)
		return 0, nil, false
	}
	h.recordSessionPolicy(r, namespace, actor, policy, revokedKeys, sweepIncomplete)
	if sweepIncomplete {
		return revokedKeys, err, true
	}
	return revokedKeys, nil, true
}

// writeSessionPolicyResult answers a PUT with the policy as recorded now.
func (h *Handlers) writeSessionPolicyResult(w http.ResponseWriter, r *http.Request, namespace string, deviceSet bool, revokedKeys int, sweepErr error) {
	body, ok := h.sessionPolicyBody(w, r, namespace)
	if !ok {
		return
	}
	if deviceSet {
		body["revoked_sign_in_keys"] = revokedKeys
	}
	if sweepErr == nil {
		writeJSON(w, http.StatusOK, body)
		return
	}
	if h.logger != nil {
		h.logger.ComponentWarn(logging.ComponentGeneral, "the session policy is set but its sign-in key sweep stopped",
			zap.String("namespace", namespace), zap.Error(sweepErr))
	}
	body["code"] = ErrCodePolicySweepIncomplete
	body["error"] = "the policy is set, but " + strconv.Itoa(revokedKeys) +
		" sign-in keys were revoked before the sweep stopped; repeat the request to revoke the rest"
	writeJSON(w, http.StatusServiceUnavailable, body)
}

// recordSignInPolicy audits a sign-in policy that was set. Opening a namespace
// to wallets nobody invited is the change worth a durable record.
func (h *Handlers) recordSignInPolicy(r *http.Request, namespace, actor string, policy authsvc.SignInPolicy) {
	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     actor,
		Action:    authsvc.AuditSignInPolicySet,
		Result:    authsvc.AuditSuccess,
		Resource:  string(policy),
	})
}

// recordSessionPolicy audits a policy that was set, whether or not every
// existing sign-in key was revoked with it.
func (h *Handlers) recordSessionPolicy(r *http.Request, namespace, actor string, policy authsvc.DevicePolicy, revokedKeys int, sweepIncomplete bool) {
	metadata := map[string]string{"revoked_sign_in_keys": strconv.Itoa(revokedKeys)}
	if sweepIncomplete {
		metadata["sign_in_key_sweep"] = "incomplete"
	}
	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: namespace,
		Actor:     actor,
		Action:    authsvc.AuditSessionPolicySet,
		Result:    authsvc.AuditSuccess,
		Resource:  string(policy),
		Metadata:  metadata,
	})
}
