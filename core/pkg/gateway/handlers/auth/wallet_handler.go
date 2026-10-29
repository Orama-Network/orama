package auth

import (
	"context"
	"net/http"
	"strings"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// WhoamiHandler answers "who am I here, and what may I do".
//
// It used to answer the first half and, for an API key, answer it by handing
// the key itself back — into the caller's terminal, their shell history and
// whatever logged the response. And it never answered the second half at all,
// so the only way to find out whether a credential could do a thing was to try
// the thing.
//
// GET /v1/auth/whoami
// Response: { "authenticated", "method", "principal", "subject", "namespace",
//
//	"role", "grants", "expires_at" }, and for a device-bound session
//	"device_id" and "device_state" ("active", or "pending": the restricted
//	session of a device the namespace has yet to approve, which holds no grant)
func (h *Handlers) WhoamiHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	ctx := r.Context()
	ns := h.defaultNS
	if v := ctx.Value(CtxKeyNamespaceOverride); v != nil {
		if s, ok := v.(string); ok && s != "" {
			ns = s
		}
	}

	body := map[string]any{"namespace": ns}

	claims, _ := ctx.Value(CtxKeyJWT).(*authsvc.JWTClaims)
	key, _ := ctx.Value(CtxKeyAPIKey).(string)

	// What the grant is looked up by is not always what is shown: a key is
	// identified in the answer by a fingerprint and in the grants table by its
	// stored hash.
	lookup := ""

	switch {
	case claims != nil:
		body["authenticated"] = true
		body["method"] = "jwt"
		body["subject"] = claims.Sub
		lookup = claims.Sub
		body["issuer"] = claims.Iss
		body["audience"] = claims.Aud
		body["issued_at"] = claims.Iat
		body["not_before"] = claims.Nbf
		body["expires_at"] = claims.Exp
		if claims.Did != "" {
			body["device_id"] = claims.Did
			body["device_state"] = claims.DeviceState()
		}
		if claims.PendingDevice() {
			// The account's grant is not this session's: a pending device
			// reaches its own session and the functions open to pending
			// devices, and nothing a grant describes.
			body["grants"] = []string{}
			writeJSON(w, http.StatusOK, body)
			return
		}
	case key != "":
		body["authenticated"] = true
		body["method"] = "api_key"
		// The key itself is not in the answer, and neither is the value the
		// api_keys table holds: on a cluster with no HMAC secret configured
		// that value *is* the key.
		body["subject"] = authsvc.KeyFingerprint(key)
		lookup = h.authService.HashAPIKey(key)
	default:
		// Reaching here needs a credential the middleware accepted, so this is
		// the gateway that serves whoami with no auth layer in front of it —
		// a namespace gateway in a test, and the honest answer is "nobody".
		body["authenticated"] = false
		body["method"] = "none"
		writeJSON(w, http.StatusOK, body)
		return
	}

	h.describePrincipal(ctx, body, ns, lookup)
	writeJSON(w, http.StatusOK, body)
}

// describePrincipal fills in what the caller may do here.
//
// A subject is a wallet or a credential, and which one it is decides where the
// grant is looked up. A subject with no grant is not an error: a wallet that
// has been removed from a namespace still holds a valid token until it expires,
// and "you are signed in and hold nothing here" is the true answer rather than
// a 500.
func (h *Handlers) describePrincipal(ctx context.Context, body map[string]any, namespace, lookup string) {
	if lookup == "" || h.authService == nil {
		return
	}

	ptype := authsvc.PrincipalServiceAccount
	switch {
	case strings.HasPrefix(lookup, authsvc.WorkloadSubjectPrefix):
		ptype = authsvc.PrincipalApp
	case authsvc.IsWalletSubject(lookup):
		ptype = authsvc.PrincipalWallet
	}
	body["principal"] = string(ptype)

	grant, err := h.authService.WhoIs(ctx, namespace, ptype, lookup)
	if err != nil || grant == nil {
		// Distinguished from "reader": holding nothing and being listed as a
		// member holding nothing are different situations, and an operator
		// reading this needs to tell them apart.
		body["role"] = nil
		body["grants"] = []string{}
		return
	}

	body["role"] = string(grant.Role)
	body["grants"] = grant.Scopes().List()
	if grant.Resource != "" {
		body["resource"] = grant.Resource
	}
	if grant.ExpiresAt != "" {
		body["grant_expires_at"] = grant.ExpiresAt
	}
}
