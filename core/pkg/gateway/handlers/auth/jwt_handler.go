package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
)

// APIKeyToJWTHandler issues a short-lived JWT from a valid API key.
// This allows API key holders to obtain JWT tokens for use with the gateway.
//
// POST /v1/auth/token
// Requires: Authorization header with API key (Bearer, ApiKey, or X-API-Key header)
// Response: { "access_token", "token_type", "expires_in", "namespace" }
func (h *Handlers) APIKeyToJWTHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	key := extractAPIKey(r)
	if strings.TrimSpace(key) == "" {
		writeError(w, http.StatusUnauthorized, "missing API key")
		return
	}

	ctx := r.Context()

	// Resolve the caller's namespace and effective scopes.
	//
	// The auth middleware has ALREADY validated this API key against the
	// global/core API-key registry — either directly (auth middleware's
	// lookupAPIKeyEntry) or, when this handler runs on a NAMESPACE gateway
	// reached via the main gateway's proxy hop, via the trusted
	// X-Internal-Auth-* headers the main gateway sets after validating and
	// before proxying. It stores the resolved namespace + scope set on the
	// request context.
	//
	// We prefer that context here to avoid a redundant re-query. When it's
	// absent (e.g. this handler reached directly, without the domain-routing
	// proxy hop), the fallback below re-resolves the key — against
	// h.apiKeyDB, the SAME global-registry querier the auth middleware uses
	// (wired by the gateway package via SetAPIKeyDB). API keys are stored
	// ONLY in the global/core registry, HMAC-hashed; a namespace's own
	// RQLite api_keys table is never authoritative, and querying it is what
	// caused main-gateway and namespace-gateway validation to disagree
	// (bugboard #147/#148 regression).
	ns := ""
	if v, ok := ctx.Value(ctxkeys.NamespaceOverride).(string); ok {
		ns = strings.TrimSpace(v)
	}
	scopesCanonical := ""
	if s, ok := ctx.Value(ctxkeys.Scopes).(authsvc.ScopeSet); ok {
		scopesCanonical = s.Canonical()
	}

	// Fallback: no pre-validated identity on the context (e.g. the handler was
	// reached on the main gateway without the middleware resolving a namespace,
	// or in a direct unit-test invocation). Resolve against the DB. Prefer
	// h.apiKeyDB (the global/core registry querier); fall back to
	// netClient.Database() only when apiKeyDB was never wired (e.g. direct
	// unit-test construction of Handlers) — netClient is also core-bound, so
	// this fallback still resolves against the global registry, never a
	// namespace's own RQLite. API keys are stored HMAC-hashed
	// (Service.HashAPIKey), so try the hashed key first and fall back to the
	// raw key for legacy unhashed rows during a rolling upgrade. Revoked keys
	// (revoked_at IS NOT NULL) resolve to invalid. The minted JWT carries the
	// SAME scope set as the key so a narrow runtime key cannot exchange for a
	// JWT and escalate to admin (bugboard #148).
	if ns == "" {
		db := h.apiKeyDB
		if db == nil && h.netClient != nil {
			db = h.netClient.Database()
		}
		internalCtx := h.internalAuthFn(ctx)
		const q = "SELECT namespaces.name, api_keys.scopes FROM api_keys JOIN namespaces ON api_keys.namespace_id = namespaces.id WHERE api_keys.key = ? AND api_keys.revoked_at IS NULL LIMIT 1"
		rawScopes := ""
		for _, candidate := range apiKeyLookupCandidates(key, h.authService.HashAPIKey(key)) { // hashed only; raw fallback removed (bugboard #163)
			res, err := db.Query(internalCtx, q, candidate)
			if err != nil || res == nil || res.Count == 0 || len(res.Rows) == 0 {
				continue
			}
			row, ok := res.Rows[0].([]interface{})
			if !ok || len(row) == 0 {
				continue
			}
			if s, ok := row[0].(string); ok && s != "" {
				ns = s
				if len(row) > 1 {
					if sc, ok := row[1].(string); ok {
						rawScopes = sc
					}
				}
				break
			}
		}
		// Embed the key's scopes. An empty column is an empty set, so an
		// unscoped key cannot be exchanged for a JWT that reaches anything.
		scopesCanonical = authsvc.ScopesFromStored(rawScopes).Canonical()
	}

	if ns == "" {
		writeError(w, http.StatusUnauthorized, "invalid API key")
		return
	}

	// Embed the key's effective scopes so the gateway scope gate enforces them on
	// the exchanged JWT exactly as on the raw key (bugboard #148).
	custom := map[string]string{"scopes": scopesCanonical}

	// The subject is the key as it is STORED, never the key as it was
	// presented. A JWT payload is base64, not encryption, so a token minted
	// with the raw key carries a live 90-day credential to everywhere a
	// 15-minute token goes: an access log, a proxy trace, a browser devtools
	// tab, the internal-auth header on the hop to a namespace gateway, and the
	// `subject` field /v1/auth/whoami echoes back.
	//
	// The stored form is what everything else already uses: RevokeKey writes
	// the hash, the revocation check looks under it, and the grant lookup tries
	// it first. It looks under the raw form too, which is what carries tokens
	// minted before this through their remaining 15 minutes.
	subject := h.authService.HashAPIKey(key)
	token, expUnix, err := h.authService.GenerateJWT(ns, subject, authsvc.AccessTokenLifetime, custom)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(expUnix - time.Now().Unix()),
		"namespace":    ns,
	})
}

// RefreshHandler refreshes an access token using a refresh token.
//
// POST /v1/auth/refresh
// Request body: RefreshRequest
// Response: { "access_token", "token_type", "expires_in", "refresh_token", "subject", "namespace" }
func (h *Handlers) RefreshHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64KB
	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		writeError(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	// Feature #68 / RFC 9700 §4.12: refresh-token rotation.
	// Every successful refresh mints a NEW refresh token and revokes the
	// supplied one atomically. The response carries the rotated value;
	// the SDK persists it (bug #239 fix) and uses it on the next refresh.
	token, newRefreshToken, subject, expUnix, err := h.authService.RefreshToken(r.Context(), req.RefreshToken, req.Namespace, req.DeviceProof)
	if err != nil {
		// A device-bound session refused for the device's sake says so: the
		// client's next move — sign a proof, or accept that this device is
		// done — depends on which it was.
		if writeDeviceRefusal(w, err) {
			return
		}
		// Bugboard #125: a TRANSIENT rotation failure (rqlite leader briefly
		// unavailable during a rolling restart) must surface as a retryable
		// 503 — NOT a 401 — so the client retries within the call-ring window
		// instead of tearing the session down to a full SIWE re-auth, which is
		// impossible on a locked device answering a VoIP-woken call.
		if errors.Is(err, authsvc.ErrRefreshTransient) || errors.Is(err, authsvc.ErrRotationNotConfigured) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "refresh temporarily unavailable, retry")
			return
		}
		// Genuine bad/expired/replayed token. The service emits a WARN log on
		// replay (ErrRefreshTokenReplay) so the operator can investigate. We
		// surface a generic 401 regardless — leaking "your token was already
		// used" would help an attacker confirm a stolen token was rotated.
		//
		// The replay tripwire is the one refusal worth a durable record: it
		// means either two clients raced or somebody is using a token they
		// should not have, and a WARN in a log nobody reads is not a record.
		action := authsvc.AuditRefreshed
		if errors.Is(err, authsvc.ErrRefreshTokenReplay) {
			action = authsvc.AuditRefreshReplayed
		}
		h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
			Namespace: req.Namespace,
			Action:    action,
			Result:    authsvc.AuditFailure,
		})
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	h.authService.Audit().RecordFromRequest(r.Context(), r, authsvc.AuditEvent{
		Namespace: req.Namespace,
		Actor:     authsvc.RedactSubject(subject),
		Action:    authsvc.AuditRefreshed,
		Result:    authsvc.AuditSuccess,
	})

	body := map[string]any{
		"access_token":  token,
		"token_type":    "Bearer",
		"expires_in":    int(expUnix - time.Now().Unix()),
		"refresh_token": newRefreshToken,
		"subject":       subject,
		"namespace":     req.Namespace,
	}
	// A device-bound session says what its device is now: a pending device's
	// refresh after a function approved it is the first full session.
	if state := authsvc.MintedDeviceState(token); state != "" {
		body["device_state"] = state
	}
	writeJSON(w, http.StatusOK, body)
}

// LogoutHandler revokes refresh tokens.
// If a refresh_token is provided, it will be revoked.
// If all=true is provided (and the request is authenticated via JWT),
// all tokens for the JWT subject within the namespace are revoked.
//
// POST /v1/auth/logout
// Request body: LogoutRequest
// Response: { "status": "ok" }
func (h *Handlers) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if h.authService == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service not initialized")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64KB
	var req LogoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	if strings.TrimSpace(req.RefreshToken) == "" && !req.All {
		writeError(w, http.StatusBadRequest, authsvc.ErrNothingToRevoke.Error())
		return
	}

	ctx := r.Context()
	var claims *authsvc.JWTClaims
	if v := ctx.Value(CtxKeyJWT); v != nil {
		claims, _ = v.(*authsvc.JWTClaims)
	}

	var subject string
	if req.All {
		if claims != nil {
			subject = strings.TrimSpace(claims.Sub)
		}
		if subject == "" {
			writeError(w, http.StatusUnauthorized, "jwt required for all=true")
			return
		}
		// A pending device ends its own session and no other.
		if claims.PendingDevice() {
			writeDeviceRefusal(w, authsvc.ErrDevicePendingApproval)
			return
		}
		// From a session bound to a device, ending every session of the
		// account is the device's act, as revoking a device or ending a
		// session is: the access token alone is a bearer token anybody who
		// copied it holds. A session bound to no device is the wallet's own.
		if claims.Did != "" && !h.verifyCallerDeviceProof(w, r, h.servedNamespace(r), claims,
			authsvc.DeviceProofLogoutAll, claims.Did, req.DeviceProof) {
			return
		}
	}

	if err := h.authService.RevokeToken(ctx, req.Namespace, req.RefreshToken, req.All, subject); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, authsvc.ErrNothingToRevoke) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}

	// Dropping the refresh token stops the caller getting a new access token.
	// It did nothing to the one they are holding, which stayed valid until it
	// expired — so logging out did not log anybody out.
	if req.All {
		if err := h.authService.RevokeAllSessions(ctx, subject); err != nil {
			writeError(w, http.StatusInternalServerError,
				"the refresh token was dropped but the access tokens already issued were not, "+
					"so they would keep working until they expire: "+err.Error())
			return
		}
	} else if claims != nil && claims.Jti != "" {
		if err := h.authService.RevokeSession(ctx, claims); err != nil {
			writeError(w, http.StatusInternalServerError,
				"the refresh token was dropped but this access token was not, "+
					"so it would keep working until it expires: "+err.Error())
			return
		}
	}

	actor := subject
	if actor == "" && claims != nil {
		actor = claims.Sub
	}
	// A subject is not necessarily an identity: the exchange endpoint mints
	// tokens whose subject is the API key itself.
	actor = authsvc.RedactSubject(actor)
	h.authService.Audit().RecordFromRequest(ctx, r, authsvc.AuditEvent{
		Namespace: req.Namespace,
		Actor:     actor,
		Action:    authsvc.AuditLoggedOut,
		Result:    authsvc.AuditSuccess,
		Metadata:  map[string]string{"all_sessions": boolText(req.All)},
	})

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// boolText keeps the metadata blob string-to-string.
func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// apiKeyLookupCandidates returns the api_keys.key values to try, hashed first
// (new keys are stored HMAC-hashed) then the raw key as a rolling-upgrade
// fallback for legacy unhashed rows. The raw fallback is skipped when hashing
// is a no-op (hashedKey == rawKey) so we never issue a duplicate query.
func apiKeyLookupCandidates(rawKey, hashedKey string) []string {
	if hashedKey == "" || hashedKey == rawKey {
		return []string{rawKey}
	}
	return []string{hashedKey}
}

// extractAPIKey reads the API key this handler's caller presents.
//
// It never reads the query string. Its own copy of this used to, unlike the
// middleware's, so a POST to /v1/auth/token could carry a key in its URL —
// into the access log, the Referer of anything the page loaded next, and the
// browser's history. This endpoint is never a WebSocket upgrade, which is the
// only place a query-string credential is defensible.
func extractAPIKey(r *http.Request) string {
	return authsvc.APIKeyFromRequest(r, false)
}
