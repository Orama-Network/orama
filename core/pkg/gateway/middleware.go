package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/constants"
	"github.com/Orama-Network/orama/core/pkg/deployments"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/clientkey"
	"github.com/Orama-Network/orama/core/pkg/gateway/corsorigins"
	"github.com/Orama-Network/orama/core/pkg/gateway/routepolicy"
	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"go.uber.org/zap"
)

// isLongRunningProxyPath returns true for paths whose expected work bound
// exceeds the default 30s proxy timeout. New classes go here, alphabetically.
//
//   - /v1/functions (deploy)                              — WASM upload + IPFS pin
//   - /v1/functions/.../invoke and /v1/invoke/...        — up to 300s per fn
//   - /v1/functions/.../ws                                — long-lived WS
//   - /v1/storage/upload, /v1/storage/pin                 — IPFS add can be slow
//   - /v1/storage/get/, /v1/storage/relayed/              — a stored object of any size, streamed
//   - /v1/namespace/backup, /v1/namespace/restore         — a database of up to 256 MiB, sealed or applied
//   - /v1/rqlite/export, /v1/rqlite/import                — the whole database, streamed
//
// Adding a path here is preferable to bumping the global timeout: each
// path's bound is documented in one grep-able place.
func isLongRunningProxyPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/v1/storage/upload"),
		strings.HasPrefix(p, "/v1/storage/pin"),
		// A download's body is relayed under the proxy's whole-request budget:
		// at 30 s, a fetch that took 18 s left 12 s to relay the object, and
		// a 200 already sent was cut off (bugboard #428).
		strings.HasPrefix(p, "/v1/storage/get/"),
		strings.HasPrefix(p, "/v1/storage/relayed/"),
		isWholeDatabasePath(p),
		// Deploys reach a namespace gateway through this proxy since bugboard
		// #427, and the handler itself allows the upload a minute.
		p == "/v1/functions",
		isFunctionInvokePath(p):
		return true
	}
	return false
}

// namespaceGatewayUnavailableMessage is what a client is told when no member of
// its namespace's gateway could be reached. The cause (the member's overlay
// address, port, and the request's path and query) stays in the node log: it
// is the operator's to read, not the caller's.
const namespaceGatewayUnavailableMessage = "namespace gateway unavailable: none of the namespace's gateways could be reached; retry shortly, and contact the network operator if it persists"

// proxyTimeoutMessage explains a proxy timeout in terms of the request that
// timed out. Only a function invocation has a timeout its caller can raise;
// telling an SDK storage read to edit function.yaml sent the reader to a file
// that does not exist for them (bugboard #414).
func proxyTimeoutMessage(path string, budget time.Duration) string {
	if isFunctionInvokePath(path) {
		return "function exceeded the proxy budget (" + budget.String() +
			"). Increase the function's timeout: in function.yaml (max 300s) or split the work into smaller invocations."
	}
	return "the namespace gateway did not answer within the proxy budget (" + budget.String() + "); a write may already have taken effect, so retry only what is safe to repeat"
}

// isFunctionInvokePath reports whether a path invokes a function, over HTTP
// or WebSocket.
func isFunctionInvokePath(p string) bool {
	return strings.HasPrefix(p, "/v1/invoke/") ||
		strings.HasPrefix(p, "/v1/functions/") && (strings.HasSuffix(p, "/invoke") || strings.HasSuffix(p, "/ws"))
}

// isProxyTimeoutErr returns true when an HTTP client error is a timeout —
// either the http.Client overall timeout or context.DeadlineExceeded
// propagating from a parent context.
func isProxyTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return true
	}
	return false
}

// Note: context keys (ctxKeyAPIKey, ctxKeyJWT, CtxKeyNamespaceOverride) are now defined in context.go

// Internal auth headers for trusted inter-gateway communication.
// When the main gateway proxies to a namespace gateway, it validates auth first
// and passes the validated namespace via these headers. The namespace gateway
// trusts these headers when they come from internal IPs (WireGuard 10.0.0.x).
const (
	// HeaderInternalAuthNamespace contains the validated namespace name
	HeaderInternalAuthNamespace = "X-Internal-Auth-Namespace"
	// HeaderInternalAuthValidated indicates the request was pre-authenticated by main gateway
	HeaderInternalAuthValidated = "X-Internal-Auth-Validated"
	// HeaderInternalAuthJWTSub carries the validated JWT `sub` claim across the
	// internal proxy hop. Bug #215: without this, namespace gateways received
	// only the namespace and host functions saw empty `caller_jwt_subject`.
	// Trusted only when X-Internal-Auth-Validated=true AND the source IP is
	// internal (WireGuard mesh / loopback) — same trust model as the
	// namespace header.
	HeaderInternalAuthJWTSub = "X-Internal-Auth-JWT-Sub"
	// HeaderInternalAuthJWTCustom carries the validated JWT custom-claims map
	// as a base64(JSON) blob. Optional; absent means the original token had
	// no custom claims. Same trust gate as HeaderInternalAuthJWTSub.
	HeaderInternalAuthJWTCustom = "X-Internal-Auth-JWT-Custom"
	// HeaderInternalAuthScopes carries the effective API-key grant set (canonical
	// comma-separated, grandfather-applied) across the namespace-gateway proxy
	// hop for API-key callers (bugboard #148). Without it, the namespace gateway
	// — which trusts pre-validation and does not re-look-up the key — computes an
	// empty scope set and 403s every admin op. Same trust gate as the JWT headers.
	HeaderInternalAuthScopes = "X-Internal-Auth-Scopes"
)

// setInternalAuthJWTHeaders writes the validated JWT subject and custom claims
// onto an outbound proxy request so the destination gateway can hydrate
// ctxKeyJWT. Called only after auth has been validated AND we're proxying over
// the WireGuard mesh (the destination still gates trust by source IP).
//
// No-op when claims is nil (caller authenticated with API key, not JWT).
func setInternalAuthJWTHeaders(h http.Header, claims *auth.JWTClaims) {
	if claims == nil {
		return
	}
	// A pending device's subject goes in a field only a gateway that knows
	// about pending devices reads (v8): to an older one the hop has no caller.
	subjectHeader := HeaderInternalAuthJWTSub
	if claims.PendingDevice() {
		subjectHeader = HeaderInternalAuthJWTPendingSub
	}
	if sub := strings.TrimSpace(claims.Sub); sub != "" {
		h.Set(subjectHeader, sub)
	}
	if len(claims.Custom) > 0 {
		buf, err := json.Marshal(claims.Custom)
		if err == nil {
			h.Set(HeaderInternalAuthJWTCustom, base64.StdEncoding.EncodeToString(buf))
		}
	}
	// The token's times and id, so the namespace gateway can hold a socket this
	// request opens to the token's expiry and to its revocation.
	h.Set(HeaderInternalAuthJWTExp, strconv.FormatInt(claims.Exp, 10))
	h.Set(HeaderInternalAuthJWTIat, strconv.FormatInt(claims.Iat, 10))
	if jti := strings.TrimSpace(claims.Jti); jti != "" {
		h.Set(HeaderInternalAuthJWTJti, jti)
	}
	// The device and session the token is bound to, so the namespace gateway
	// can hand the device to the function and refuse the socket when either
	// is revoked.
	if did := strings.TrimSpace(claims.Did); did != "" {
		h.Set(HeaderInternalAuthJWTDid, did)
	}
	if sid := strings.TrimSpace(claims.Sid); sid != "" {
		h.Set(HeaderInternalAuthJWTSid, sid)
	}
	// What the device's attestation proved, so the namespace gateway judges
	// the request by its namespace's policy and tells the function.
	if claims.Att != nil {
		if buf, err := json.Marshal(claims.Att); err == nil {
			h.Set(HeaderInternalAuthJWTAtt, base64.StdEncoding.EncodeToString(buf))
		}
	}
}

// setInternalAuthKeyHeaders writes an API-key caller's effective scopes (#148),
// the resources narrowing them and the key's fingerprint onto an outbound proxy
// request. No-op for a caller that is not a key.
func setInternalAuthKeyHeaders(h http.Header, key keyAuthority) {
	if key.scopes == "" {
		return
	}
	h.Set(HeaderInternalAuthScopes, key.scopes)
	if len(key.resources) > 0 {
		h.Set(HeaderInternalAuthKeyResources, key.resources.Stored())
	}
	if key.fingerprint != "" {
		h.Set(HeaderInternalAuthKeyFingerprint, key.fingerprint)
	}
	if key.id > 0 {
		h.Set(HeaderInternalAuthKeyID, strconv.FormatInt(key.id, 10))
	}
}

// stripInboundInternalAuthHeaders deletes the X-Internal-Auth-* headers from
// the supplied header set. MUST be called on every outbound request the main
// gateway proxies to a namespace gateway BEFORE we (conditionally) re-set
// them with the validated values.
//
// SECURITY (bug #215 follow-up — critical): without this, an external client
// could send `X-Internal-Auth-Validated: true` + `X-Internal-Auth-JWT-Sub:
// <victim-wallet>` directly. The header-copy loop at the proxy hop would
// forward those headers verbatim. The namespace gateway, seeing the request
// arrive from the main gateway's WireGuard IP, would trust them and hydrate
// ctxKeyJWT with attacker-controlled subject — full cross-namespace identity
// spoofing on every public path.
//
// Stripping FIRST and conditionally setting AFTER closes that hole regardless
// of which auth path won (JWT, API key, or no creds on a public path).
func stripInboundInternalAuthHeaders(h http.Header) {
	h.Del(HeaderInternalAuthValidated)
	h.Del(HeaderInternalAuthNamespace)
	h.Del(HeaderInternalAuthJWTSub)
	h.Del(HeaderInternalAuthJWTCustom)
	h.Del(HeaderInternalAuthScopes)
	for _, name := range internalAuthVersionedHeaders {
		h.Del(name)
	}
}

// maxQueryJWTLength caps the size of a JWT accepted via `?jwt=` query
// param. EdDSA + RS256 JWTs minted by this gateway are well under 2 KB;
// 4 KB is a generous ceiling that still cheaply rejects DoS attempts
// that try to feed multi-MB tokens through the verifier.
const maxQueryJWTLength = 4096

// stripJWTQueryParam removes the `jwt` key from the URL's query string
// (if present), mutating r in place. Called after a successful WS-upgrade
// JWT-via-query verification so the token doesn't propagate to:
//   - the namespace-gateway proxy hop (`r.URL.RawQuery` is forwarded)
//   - downstream handler logs that record `r.URL.RequestURI()`
//   - any inner `r.URL.Query()` lookups in business logic
//
// Idempotent: safe to call on requests without a `jwt` param.
func stripJWTQueryParam(r *http.Request) {
	q := r.URL.Query()
	if !q.Has("jwt") {
		return
	}
	q.Del("jwt")
	r.URL.RawQuery = q.Encode()
}

// claimsFromInternalAuthHeaders rebuilds a *auth.JWTClaims from the trusted
// internal-auth headers. Returns nil if no JWT subject was forwarded (the
// caller used an API key, or the request didn't carry validated JWT data).
//
// SECURITY: this MUST only be called after the caller has confirmed
// HeaderInternalAuthValidated == "true" AND the source IP is internal AND
// the proxy hop has stripped any inbound copies of these headers (see
// stripInboundInternalAuthHeaders). Skipping any of those would let any
// client forge JWT identities.
//
// The claims carry the token's exp, iat and jti, which is what holds a socket
// opened by this request to the token's expiry and revocation (see
// hopTokenTimes for a hop that cannot say). A time that is present and
// unreadable asserts something that cannot be honoured, so the identity is
// refused with an error.
func claimsFromInternalAuthHeaders(h http.Header, namespace string, now time.Time) (*auth.JWTClaims, error) {
	sub := strings.TrimSpace(h.Get(HeaderInternalAuthJWTSub))
	audience := auth.AudienceGateway
	if sub == "" {
		sub = strings.TrimSpace(h.Get(HeaderInternalAuthJWTPendingSub))
		audience = auth.AudiencePendingDevice
	}
	if sub == "" {
		return nil, nil
	}
	did := strings.TrimSpace(h.Get(HeaderInternalAuthJWTDid))
	if audience == auth.AudiencePendingDevice && did == "" {
		return nil, fmt.Errorf("the forwarded pending session names no device")
	}
	exp, iat, err := hopTokenTimes(h, now)
	if err != nil {
		return nil, err
	}
	claims := &auth.JWTClaims{
		Aud:       audience,
		Sub:       sub,
		Namespace: namespace,
		Jti:       strings.TrimSpace(h.Get(HeaderInternalAuthJWTJti)),
		Did:       did,
		Sid:       strings.TrimSpace(h.Get(HeaderInternalAuthJWTSid)),
		Exp:       exp,
		Iat:       iat,
	}
	if raw := strings.TrimSpace(h.Get(HeaderInternalAuthJWTCustom)); raw != "" {
		if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
			var custom map[string]string
			if json.Unmarshal(decoded, &custom) == nil && len(custom) > 0 {
				claims.Custom = custom
			}
		}
	}
	// An attestation is only ever of the device the token is bound to; one
	// with no device asserts nothing.
	if raw := strings.TrimSpace(h.Get(HeaderInternalAuthJWTAtt)); raw != "" && claims.Did != "" {
		att, err := decodeHopAttestation(raw)
		if err != nil {
			return nil, err
		}
		claims.Att = att
	}
	return claims, nil
}

// decodeHopAttestation reads HeaderInternalAuthJWTAtt. The MAC verified it, so
// a value that cannot be read is the main gateway's own bug, and the identity
// is refused rather than served without the attestation it was sent with.
func decodeHopAttestation(raw string) (*auth.AttestationClaim, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("the forwarded attestation is not base64: %w", err)
	}
	var att auth.AttestationClaim
	if err := json.Unmarshal(decoded, &att); err != nil {
		return nil, fmt.Errorf("the forwarded attestation is not one: %w", err)
	}
	return &att, nil
}

// sessionRevokedMessage is what a refused revoked token says. The namespace
// proxy carries a refusal as a message, so the message is also how the caller
// tells a revoked session from an unknown credential (namespaceProxyAuthCode).
const sessionRevokedMessage = "this session was ended, or the key it came from was revoked"

// deviceRevokedMessage is what a token refused because its device was revoked
// says. validateAuthForNamespaceProxy returns it with the refused token's
// claims, and the refusal is answered by refuseRevokedDevice.
const deviceRevokedMessage = "this device was revoked; it cannot sign in or hold a session again"

// refuseRevokedDevice answers a token refused because its device was revoked:
// 403 DEVICE_REVOKED with the device's revocation, for the client to check
// before it acts on it. The revocation is read here, for a request already
// refused, never on a healthy one; a registry that cannot be read is a
// retryable 503, as a revocation list that cannot be read is.
func (g *Gateway) refuseRevokedDevice(w http.ResponseWriter, r *http.Request, claims *auth.JWTClaims) {
	rec, err := g.authService.DeviceRevocationRecord(r.Context(), claims.Namespace, claims.Did)
	if err != nil {
		g.logger.ComponentWarn("gateway", "could not read a revoked device's revocation",
			zap.String("device_id", claims.Did), zap.Error(err))
		unavailable(w, CodeAuthUnavailable, authUnavailableMessage)
		return
	}
	forbidden(w, CodeDeviceRevoked, deviceRevokedMessage, map[string]any{"revocation": rec})
}

// refuseProxyCredential answers a credential validateAuthForNamespaceProxy
// refused (errMsg); claims are what it returned beside the refusal.
func (g *Gateway) refuseProxyCredential(w http.ResponseWriter, r *http.Request, errMsg string, claims *auth.JWTClaims) {
	switch errMsg {
	case deviceRevokedMessage:
		g.refuseRevokedDevice(w, r, claims)
	case authUnavailableMessage:
		unavailable(w, CodeAuthUnavailable, errMsg)
	default:
		unauthorized(w, namespaceProxyAuthCode(errMsg), errMsg, nil)
	}
}

// authUnavailableMessage is what a request answers when whether its credential
// was revoked cannot be told (auth.ErrRevocationsUnavailable). The namespace
// proxy carries a refusal as a message, so this is also how it is told apart
// from a refused credential: it answers 503, not 401, and the client retries.
const authUnavailableMessage = "cannot check whether this credential was revoked; retry shortly"

// tokenExpiredMessage is what a refused expired token says. The namespace proxy
// carries a refusal as a message, so the message is also how the caller tells an
// expired token from an unknown credential (namespaceProxyAuthCode).
const tokenExpiredMessage = "this token has expired"

// namespaceProxyAuthCode is the code a refused namespace-proxy credential
// answers with: a revoked session is AUTH_REVOKED, any other refusal names a
// credential this cluster does not know.
func namespaceProxyAuthCode(errMsg string) string {
	if errMsg == sessionRevokedMessage {
		return CodeAuthRevoked
	}
	if errMsg == authUnavailableMessage {
		return CodeAuthUnavailable
	}
	if errMsg == tokenExpiredMessage {
		return CodeAuthExpired
	}
	return CodeAuthInvalidKey
}

// validateAuthForNamespaceProxy validates the request's auth credentials against the MAIN
// cluster RQLite and returns the namespace the credentials belong to plus the
// JWT claims when the caller authenticated with a Bearer JWT.
// This is used by handleNamespaceGatewayRequest to pre-authenticate before proxying to
// namespace gateways (which have isolated RQLites without API keys).
//
// Returns:
//   - ("", claims, deviceRevokedMessage) if the token was refused because its
//     device was revoked: claims are the refused token's, which name the
//     device (refuseRevokedDevice).
//   - (namespace, claims, "") if auth is valid via JWT — claims may be used to
//     populate internal-auth headers so the namespace gateway can hydrate
//     `caller_jwt_subject` for serverless host functions (bug #215).
//   - (namespace, nil, "") if auth is valid via API key.
//   - ("", nil, errorMessage) if auth is invalid.
//   - ("", nil, "") if no auth credentials provided (for public paths).
//

func (g *Gateway) validateAuthForNamespaceProxy(r *http.Request) (namespace string, claims *auth.JWTClaims, key keyAuthority, errMsg string) {
	// 1) Try JWT Bearer first
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		lower := strings.ToLower(authHeader)
		if strings.HasPrefix(lower, "bearer ") {
			tok := strings.TrimSpace(authHeader[len("Bearer "):])
			if strings.Count(tok, ".") == 2 {
				c, err := g.authService.ParseAndVerifyJWT(tok)
				if err == nil {
					if ns := strings.TrimSpace(c.Namespace); ns != "" {
						key, _, keyErr := g.exchangedKeyScopes(r.Context(), c)
						if keyErr != "" {
							return "", nil, keyAuthority{}, keyErr
						}
						return ns, c, key, ""
					}
				}
				// A revoked token is not an absent one: falling through would
				// have the request answered "no credential was presented".
				// One refused for its device carries its claims, which name
				// the device whose revocation the refusal shows.
				var revokedDevice *auth.DeviceTokenRevokedError
				if errors.As(err, &revokedDevice) {
					return "", revokedDevice.Claims, keyAuthority{}, deviceRevokedMessage
				}
				if errors.Is(err, auth.ErrTokenRevoked) {
					return "", nil, keyAuthority{}, sessionRevokedMessage
				}
				if errors.Is(err, auth.ErrRevocationsUnavailable) {
					return "", nil, keyAuthority{}, authUnavailableMessage
				}
				if errors.Is(err, auth.ErrTokenExpired) {
					return "", nil, keyAuthority{}, tokenExpiredMessage
				}
				// JWT verification failed - fall through to API key check
			}
		}
	}

	// 1b) WS upgrade fallback: JWT via `?jwt=` query. Same rationale as in
	// authMiddleware — browser / React Native WS clients can't set custom
	// headers reliably. Bug #240. The request is not modified here: the
	// cluster gateway's serverless routing validates a request it may still
	// serve itself (bugboard #427), and authMiddleware must then find the
	// token where the client put it. proxyToNamespaceGateway strips it before
	// the proxy hop instead.
	if isWebSocketUpgrade(r) {
		tok := strings.TrimSpace(r.URL.Query().Get("jwt"))
		if tok != "" && len(tok) <= maxQueryJWTLength && strings.Count(tok, ".") == 2 {
			c, err := g.authService.ParseAndVerifyJWT(tok)
			if err == nil {
				if ns := strings.TrimSpace(c.Namespace); ns != "" {
					key, _, keyErr := g.exchangedKeyScopes(r.Context(), c)
					if keyErr != "" {
						return "", nil, keyAuthority{}, keyErr
					}
					return ns, c, key, ""
				}
			}
			var revokedDevice *auth.DeviceTokenRevokedError
			if errors.As(err, &revokedDevice) {
				return "", revokedDevice.Claims, keyAuthority{}, deviceRevokedMessage
			}
			if errors.Is(err, auth.ErrTokenRevoked) {
				return "", nil, keyAuthority{}, sessionRevokedMessage
			}
			if errors.Is(err, auth.ErrRevocationsUnavailable) {
				return "", nil, keyAuthority{}, authUnavailableMessage
			}
			if errors.Is(err, auth.ErrTokenExpired) {
				return "", nil, keyAuthority{}, tokenExpiredMessage
			}
		}
	}

	// 2) Try API key
	rawKey := extractAPIKey(r)
	if rawKey == "" {
		return "", nil, keyAuthority{}, "" // No credentials provided
	}

	// Resolve namespace AND the key's scopes and resources so they can be
	// forwarded to the namespace gateway, which does not re-look-up the key
	// (bugboard #148).
	entry, err := g.lookupAPIKeyEntry(r.Context(), rawKey, g.apiKeyDB())
	if err != nil {
		if errors.Is(err, auth.ErrRevocationsUnavailable) {
			return "", nil, keyAuthority{}, authUnavailableMessage
		}
		return "", nil, keyAuthority{}, "invalid API key"
	}
	return entry.namespace, nil, entry.authority(), ""
}

// lookupAPIKeyNamespace resolves an API key to its namespace using cache and DB.
// q is the querier to use — always the global/core API-key registry (see
// apiKeyDB in apikey_querier.go), never a namespace's own RQLite.
// Returns the namespace name or an error if the key is invalid.
//
// The key is looked up by its HMAC hash only; api_keys never holds a raw key.
func (g *Gateway) lookupAPIKeyNamespace(ctx context.Context, key string, q apiKeyQuerier) (string, error) {
	entry, err := g.lookupAPIKeyEntry(ctx, key, q)
	return entry.namespace, err
}

// apiKeyEntry is what a key's row says: its registry id, the namespace it
// belongs to, its raw api_keys.scopes value, and the resources it is narrowed
// to (migration 082).
type apiKeyEntry struct {
	id        int64
	namespace string
	scopes    string
	resources auth.KeyResources
	// stored is the key as api_keys.key holds it (HashAPIKey of the raw key,
	// or an exchanged token's subject), which both spellings of one key share.
	stored string
}

// authority is the part of the row that says what the key may reach, in the
// form the proxy hop forwards, with the fingerprint that names the key.
func (e apiKeyEntry) authority() keyAuthority {
	return keyAuthority{
		scopes:      auth.ScopesFromStored(e.scopes).Canonical(),
		resources:   e.resources,
		fingerprint: auth.KeyFingerprint(e.stored),
		id:          e.id,
	}
}

// keyAuthority is an API key as the main gateway forwards it to a namespace
// gateway: its canonical scopes and the resources narrowing them (what it may
// reach), its fingerprint (what the invoke rate limiter buckets it by) and its
// registry id (what request_logs.api_key_id records). All empty for a caller
// that is not a key.
type keyAuthority struct {
	scopes      string
	resources   auth.KeyResources
	fingerprint string
	id          int64
}

// lookupAPIKeyEntry resolves an API key to its row (namespace, scopes,
// resources) using cache and DB. Revoked keys (revoked_at IS NOT NULL) are
// treated as invalid (bugboard #148).
//
// q must be the global/core API-key registry querier (apiKeyDB()) — API keys
// are created by `orama namespace keys create` into the global/core RQLite,
// so that is the only store that can ever resolve them. A namespace's own
// RQLite is never authoritative for API keys.
func (g *Gateway) lookupAPIKeyEntry(ctx context.Context, key string, q apiKeyQuerier) (apiKeyEntry, error) {
	// A revoked key must stop working now, not when the cache entry ages out.
	//
	// The entry below is held for a minute, so revoking a key left it working
	// for up to that long — on every gateway that had seen it. The revocation
	// list is replicated and reloaded every ten seconds, so consulting it first
	// is both shorter and cluster-wide. It is checked under both spellings
	// because RevokeKey only ever has the hash.
	if g.authService != nil {
		denied, err := g.authService.Revocations().DeniesSubject(key, g.authService.HashAPIKey(key))
		if err != nil {
			return apiKeyEntry{}, fmt.Errorf("lookupAPIKeyEntry: %w", err)
		}
		if denied {
			return apiKeyEntry{}, fmt.Errorf("invalid API key")
		}
	}

	// Cache uses raw key as cache key (in-memory only, never persisted)
	if g.mwCache != nil {
		if cached, ok := g.mwCache.GetAPIKeyEntry(key); ok {
			return cached, nil
		}
	}

	if q == nil {
		return apiKeyEntry{}, fmt.Errorf("lookupAPIKeyEntry: no database querier configured for this gateway")
	}

	internalCtx := client.WithInternalAuth(ctx)
	// Revoked and expired keys resolve to "invalid". The revocation check above
	// is what bounds the cache; this is what makes the row itself the answer.
	// expires_at is NOT NULL from migration 051: a key with no expiry is what
	// that migration exists to end, so the comparison is unconditional.
	hashedKey := g.authService.HashAPIKey(key)
	res, err := q.Query(internalCtx, apiKeyByStoredSQL, hashedKey)
	if err != nil {
		return apiKeyEntry{}, fmt.Errorf("lookupAPIKeyEntry: hashed-key query failed: %w", err)
	}
	if entry, ok := apiKeyEntryFromRow(res); ok {
		entry.stored = hashedKey
		if g.mwCache != nil {
			g.mwCache.SetAPIKeyEntry(key, entry)
		}
		return entry, nil
	}

	orphanSQL := "SELECT api_keys.id FROM api_keys LEFT JOIN namespaces ON api_keys.namespace_id = namespaces.id WHERE api_keys.key = ? AND namespaces.id IS NULL LIMIT 1"
	if ores, oerr := q.Query(internalCtx, orphanSQL, hashedKey); oerr == nil && ores != nil && ores.Count > 0 && g.logger != nil {
		g.logger.ComponentWarn("gateway", "API key row exists but namespace is gone (orphaned key)",
			zap.Int64("hits", ores.Count))
	}

	return apiKeyEntry{}, fmt.Errorf("invalid API key")
}

// apiKeyByStoredSQL resolves a key by the value stored in api_keys.key. That
// value is the HMAC when one is configured and the raw key when it is not.
// An exchanged token's subject is already that value, so it must not be
// hashed a second time.
const apiKeyByStoredSQL = "SELECT namespaces.name, api_keys.scopes, api_keys.resources, api_keys.id FROM api_keys JOIN namespaces ON api_keys.namespace_id = namespaces.id WHERE api_keys.key = ? AND api_keys.revoked_at IS NULL AND api_keys.expires_at > datetime('now') LIMIT 1"

// apiKeyEntryFromRow reads the row apiKeyByStoredSQL returns. ok is false when
// it names no namespace.
func apiKeyEntryFromRow(res *client.QueryResult) (apiKeyEntry, bool) {
	if res == nil || res.Count == 0 || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return apiKeyEntry{}, false
	}
	row := res.Rows[0]
	entry := apiKeyEntry{namespace: getString(row[0])}
	if entry.namespace == "" {
		return apiKeyEntry{}, false
	}
	if len(row) > 1 {
		entry.scopes = getString(row[1])
	}
	if len(row) > 2 {
		entry.resources = auth.KeyResourcesFromStored(getString(row[2]))
	}
	if len(row) > 3 {
		entry.id = int64(getInt(row[3]))
	}
	return entry, true
}

// lookupStoredAPIKey resolves the key an exchanged token names. The subject is
// the stored form. Hashing it again finds nothing, which is how a token the
// gateway itself minted was refused on every route that reads scopes.
func (g *Gateway) lookupStoredAPIKey(ctx context.Context, stored string) (apiKeyEntry, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return apiKeyEntry{}, fmt.Errorf("invalid API key")
	}
	if g.authService != nil {
		denied, err := g.authService.Revocations().DeniesSubject(stored)
		if err != nil {
			return apiKeyEntry{}, fmt.Errorf("lookupStoredAPIKey: %w", err)
		}
		if denied {
			return apiKeyEntry{}, fmt.Errorf("invalid API key")
		}
	}
	if g.mwCache != nil {
		if cached, ok := g.mwCache.GetAPIKeyEntry(stored); ok {
			return cached, nil
		}
	}
	q := g.apiKeyDB()
	if q == nil {
		return apiKeyEntry{}, fmt.Errorf("lookupStoredAPIKey: no database querier configured for this gateway")
	}
	internalCtx := client.WithInternalAuth(ctx)
	res, err := q.Query(internalCtx, apiKeyByStoredSQL, stored)
	if err != nil {
		return apiKeyEntry{}, fmt.Errorf("lookupStoredAPIKey: query failed: %w", err)
	}
	if entry, ok := apiKeyEntryFromRow(res); ok {
		entry.stored = stored
		if g.mwCache != nil {
			g.mwCache.SetAPIKeyEntry(stored, entry)
		}
		return entry, nil
	}
	return apiKeyEntry{}, fmt.Errorf("invalid API key")
}

// exchangedKeyScopes is the scope set, and the resources narrowing it, a
// verified token may use when its subject is a stored API key. The scopes
// claim inside the token is not read: it was copied when the token was minted,
// and the row is what a later change to the key has to meet. A wallet or a workload returns empty with no error; a
// workload's authority is its own grant.
//
// errMsg is set when the key cannot be used. keyNS is the row's namespace,
// which is what a mismatch report names.
func (g *Gateway) exchangedKeyScopes(ctx context.Context, claims *auth.JWTClaims) (key keyAuthority, keyNS, errMsg string) {
	if claims == nil || !auth.IsStoredAPIKeySubject(claims.Sub) {
		return keyAuthority{}, "", ""
	}
	entry, err := g.lookupStoredAPIKey(ctx, claims.Sub)
	if err != nil {
		if errors.Is(err, auth.ErrRevocationsUnavailable) {
			return keyAuthority{}, "", authUnavailableMessage
		}
		return keyAuthority{}, "", "this API key is not one this cluster knows"
	}
	if tokenNS := strings.TrimSpace(claims.Namespace); tokenNS != "" && entry.namespace != tokenNS {
		return keyAuthority{}, entry.namespace, "this credential belongs to another namespace"
	}
	return entry.authority(), entry.namespace, ""
}

// withExchangedKeyScopes attaches exchangedKeyScopes to the request. ok is
// false when the response has already been written.
func (g *Gateway) withExchangedKeyScopes(w http.ResponseWriter, ctx context.Context, claims *auth.JWTClaims, isPublic bool) (context.Context, bool) {
	if claims == nil || !auth.IsStoredAPIKeySubject(claims.Sub) {
		return ctx, true
	}
	key, keyNS, errMsg := g.exchangedKeyScopes(ctx, claims)
	if errMsg != "" {
		if isPublic {
			return ctx, true
		}
		if errMsg == authUnavailableMessage {
			unavailable(w, CodeAuthUnavailable, errMsg)
			return ctx, false
		}
		if keyNS != "" {
			forbidden(w, CodeNamespaceMismatch, errMsg,
				map[string]any{"namespace": strings.TrimSpace(claims.Namespace), "credential_namespace": keyNS})
			return ctx, false
		}
		unauthorized(w, CodeAuthInvalidKey, errMsg, nil)
		return ctx, false
	}
	markRequestKey(ctx, key.id)
	return withKeyFingerprint(withKeyAuthority(ctx, key.scopes, key.resources), key.fingerprint), true
}

// withKeyAuthority attaches what an API key may reach: its scopes, and the
// resources narrowing them when it has any.
func withKeyAuthority(ctx context.Context, scopes string, resources auth.KeyResources) context.Context {
	ctx = context.WithValue(ctx, ctxKeyScopes, auth.ParseScopes(scopes))
	if len(resources) > 0 {
		ctx = context.WithValue(ctx, ctxKeyKeyResources, resources)
	}
	return ctx
}

// markHopRequestKey records, for request_logs, the registry id of the API key
// the main gateway authenticated, as the hop carries it. The MAC verified, so
// a value that is not an id is the main gateway's own bug; it is reported and
// the row records no key rather than a guessed one. The id is also put on the
// context, where a socket the key opens is held to its revocation (bug-702).
func (g *Gateway) markHopRequestKey(ctx context.Context, r *http.Request) context.Context {
	raw := strings.TrimSpace(r.Header.Get(HeaderInternalAuthKeyID))
	if raw == "" {
		return ctx
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		g.logger.ComponentWarn("gateway", "dropped a hop API key id that is not one",
			zap.String("path", r.URL.Path), zap.String("key_id", raw))
		return ctx
	}
	markRequestKey(ctx, id)
	return context.WithValue(ctx, ctxKeyKeyID, id)
}

// withKeyFingerprint attaches the fingerprint of the API key that
// authenticated the request, when there is one.
func withKeyFingerprint(ctx context.Context, fingerprint string) context.Context {
	if fingerprint = strings.TrimSpace(fingerprint); fingerprint != "" {
		ctx = context.WithValue(ctx, ctxKeyKeyFingerprint, fingerprint)
	}
	return ctx
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request
func isWebSocketUpgrade(r *http.Request) bool {
	connection := strings.ToLower(r.Header.Get("Connection"))
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))
	return strings.Contains(connection, "upgrade") && upgrade == "websocket"
}

// proxyWebSocket proxies a WebSocket connection by hijacking the client connection
// and tunneling bidirectionally to the backend. A backend that cannot be dialed
// is answered with a typed, retryable 503 (see tunnelWebSocket).
func (g *Gateway) proxyWebSocket(w http.ResponseWriter, r *http.Request, targetHost string) bool {
	result, dialErr := g.tunnelWebSocket(w, r, targetHost, nil)
	if dialErr != nil {
		httputil.WriteRPCError(w, http.StatusServiceUnavailable, httputil.ErrCodeServiceUnavailable,
			"WebSocket backend unavailable, retry", httputil.WithRetryable())
		return false
	}
	return result == tunnelEstablished
}

// tunnelResult is how tunnelWebSocket ended when the backend could be dialed.
type tunnelResult int

const (
	// tunnelEstablished: the handshake request reached the backend and the
	// tunnel ran until one side closed it.
	tunnelEstablished tunnelResult = iota
	// tunnelBackendFailed: the connection opened but the handshake request
	// could not be written to it.
	tunnelBackendFailed
	// tunnelNotServed: this gateway could not tunnel (the response writer
	// cannot be hijacked, or the client connection could not be taken over);
	// the backend did nothing wrong.
	tunnelNotServed
)

// tunnelWebSocket does the work of proxyWebSocket. A non-nil dialErr means the
// backend could not be dialed: nothing was sent to it and nothing was written
// to w, so the caller may try another backend or answer the client itself.
// Every other failure has already been answered on w.
//
// onEstablished, when not nil, is called once the handshake request is with the
// backend and the client connection is taken over, before the tunnel starts to
// run. A tunnel lasts as long as its connection does - hours - so a caller that
// waited for it to end before reporting the backend's health would hold a
// circuit breaker's half-open probe slot that long.
func (g *Gateway) tunnelWebSocket(w http.ResponseWriter, r *http.Request, targetHost string, onEstablished func()) (result tunnelResult, dialErr error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket proxy not supported", http.StatusInternalServerError)
		return tunnelNotServed, nil
	}

	// Connect to backend
	backendConn, err := net.DialTimeout("tcp", targetHost, 10*time.Second)
	if err != nil {
		g.logger.ComponentError(logging.ComponentGeneral, "WebSocket backend dial failed",
			zap.String("target", targetHost),
			zap.Error(err),
		)
		return tunnelBackendFailed, fmt.Errorf("failed to dial WebSocket backend %s: %w", targetHost, err)
	}

	// Write the original request to backend (this initiates the WebSocket handshake)
	if err := r.Write(backendConn); err != nil {
		backendConn.Close()
		g.logger.ComponentError(logging.ComponentGeneral, "WebSocket handshake write failed",
			zap.Error(err),
		)
		http.Error(w, "Failed to initiate WebSocket", http.StatusBadGateway)
		return tunnelBackendFailed, nil
	}

	// Hijack client connection
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		backendConn.Close()
		g.logger.ComponentError(logging.ComponentGeneral, "WebSocket hijack failed",
			zap.Error(err),
		)
		return tunnelNotServed, nil
	}

	// Flush any buffered data from the client
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		clientBuf.Read(buffered)
		backendConn.Write(buffered)
	}

	if onEstablished != nil {
		onEstablished()
	}

	// Bidirectional copy between client and backend
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		io.Copy(clientConn, backendConn)
		clientConn.Close()
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		io.Copy(backendConn, clientConn)
		backendConn.Close()
	}()

	// Wait for one side to close
	<-done
	clientConn.Close()
	backendConn.Close()
	<-done

	return tunnelEstablished, nil
}

// withMiddleware adds CORS, security headers, rate limiting, and logging middleware
func (g *Gateway) withMiddleware(next http.Handler) http.Handler {
	// Order: internal-auth -> route policy -> logging -> security headers ->
	// CORS -> rate limit -> readiness -> domain routing -> cluster serverless
	// routing -> auth -> pending-device gate -> traffic attribution -> attestation -> authorization ->
	// scope -> namespace rate limit -> long-request deadlines -> handler
	//
	// CORS is above the rate limit so that a refusal by the limiter (a 429)
	// carries the same CORS headers as any answer: a browser page from an
	// origin the gateway allows can then read the status and Retry-After
	// instead of seeing an opaque failed fetch. The decision about which
	// origins are allowed is unchanged (allowedOriginFor); only where in the
	// chain it is made moved. A preflight is answered by corsMiddleware before
	// the limiter and so is not counted by it: it does no work and reaches
	// nothing behind it.
	//
	// Cluster serverless routing sits beside domain routing and does the same
	// kind of thing: on the cluster gateway it proxies a tenant's function
	// traffic to the tenant's gateway, whatever host the request arrived on
	// (bugboard #427). It is above auth because the proxy validates the
	// credential itself, against the registry.
	//
	// The route-policy gate resolves what the matched route requires and puts
	// it on the request, so the four places that ask cannot answer differently.
	// It sits above domain routing because that is the first of them.
	//
	// The internal-auth gate is first, before anything else can read a header.
	// It deletes every X-Internal-Auth-* header that did not arrive with a
	// valid MAC, so no middleware below it has to ask whether the ones it sees
	// are authentic — they are, or they are not there.
	//
	// The readiness gate sits inside CORS (and the rate limiter), not above it. A gateway
	// that is still starting refuses with a 503 carrying the reason, and a
	// browser can only read that if the response has CORS headers and the
	// preflight was answered — above corsMiddleware the SDK would see an
	// opaque "failed to fetch" instead of the reason this change exists to
	// deliver. It stays above domain routing and auth, so no handler and no
	// WebSocket upgrade is reached while the gateway cannot serve.
	//
	// The pending-device gate sits directly inside auth, the first point that
	// knows the caller, so a pending device's restricted session is refused on
	// every route that does not admit one before anything acts for it
	// (pending_device_gate.go).
	//
	// The scope gate (bugboard #148) runs after ownership so it only ever
	// tightens an already-authorized request; it never authorizes on its own.
	//
	// The traffic attribution step sits directly inside auth, the first point
	// that knows the request's namespace, and notes it for loggingMiddleware's
	// request metrics (see traffic.go).
	//
	// The Caddy proof is checked and deleted before even that: X-Forwarded-For is believed
	// only on a request that carries it, and no header holding the secret may travel to a
	// handler or a proxied service.
	return g.callerProof.Middleware(g.internalAuthMiddleware(
		g.routePolicyMiddleware(
			g.loggingMiddleware(
				g.securityHeadersMiddleware(
					g.corsMiddleware(
						g.rateLimitMiddleware(
							g.readinessGate(
								g.domainRoutingMiddleware(
									g.clusterServerlessRoutingMiddleware(
										g.authMiddleware(
											g.pendingDeviceGate(
												g.trafficAttributionMiddleware(
													g.attestationMiddleware(
														g.authorizationMiddleware(
															g.scopeMiddleware(
																g.namespaceRateLimitMiddleware(longRequestDeadlines(next))))))))))))))))))
}

// securityHeadersMiddleware adds standard security headers to all responses
func (g *Gateway) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
		// HSTS only when behind TLS (Caddy)
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		noStoreAPIResponses(next).ServeHTTP(w, r)
	})
}

// loggingMiddleware logs basic request info and duration
func (g *Gateway) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		srw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		r, attribution := withTrafficAttribution(r)
		r, phases := withRequestPhases(r, start)
		next.ServeHTTP(srw, r)
		end := time.Now()
		dur := end.Sub(start)
		policy := g.policyFor(r)
		if policy.RequestLog == routepolicy.LogNone {
			// Counted by status, with no size and no duration, and written nowhere.
			g.countTraffic(r, attribution, srw.status)
			return
		}
		g.recordTraffic(r, attribution, srw.status, srw.bytes, dur)
		g.logger.ComponentInfo(logging.ComponentGeneral, "request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", srw.status),
			zap.Int("bytes", srw.bytes),
			zap.String("duration", dur.String()),
		)
		// A WebSocket's duration is the life of its connection, not a stall.
		if dur >= slowRequestThreshold && !isWebSocketUpgrade(r) {
			g.logger.ComponentWarn(logging.ComponentGeneral, "slow request",
				append([]zap.Field{zap.String("method", r.Method), zap.String("path", r.URL.Path),
					zap.Int("status", srw.status), zap.Int64("duration_ms", dur.Milliseconds())},
					phases.fields(end)...)...)
		}

		// Enqueue log entry for batched persistence (replaces per-request DB writes)
		if g.logBatcher != nil {
			g.logBatcher.Add(newRequestLogEntry(r, policy, srw.status, srw.bytes, dur, attribution.keyID))
		}
	})
}

// refuseRevokedToken answers a token the revocation list refused, and reports
// whether it did. A token refused because its device was revoked is answered
// DEVICE_REVOKED with the device's revocation on every route, public ones
// included: served as anonymous, the device would go on working with nothing
// telling it it was revoked. The main gateway in front of a namespace refuses
// it the same way (proxyToNamespaceGateway). Any other revoked token is
// AUTH_REVOKED, on a route that is not public.
func (g *Gateway) refuseRevokedToken(w http.ResponseWriter, r *http.Request, err error, isPublic bool) bool {
	var revokedDevice *auth.DeviceTokenRevokedError
	switch {
	case errors.As(err, &revokedDevice):
		g.refuseRevokedDevice(w, r, revokedDevice.Claims)
		return true
	case isPublic || !errors.Is(err, auth.ErrTokenRevoked):
		return false
	}
	unauthorized(w, CodeAuthRevoked, sessionRevokedMessage, nil)
	return true
}

// authMiddleware enforces auth when enabled via config.
// Accepts:
//   - Authorization: Bearer <JWT> (RS256 / EdDSA issued by this gateway)
//   - Authorization: Bearer <API key> or ApiKey <API key>
//   - X-API-Key: <API key>
//   - ?api_key=<key> or ?token=<key> query string (WebSocket upgrade only)
//   - ?jwt=<token> query string (WebSocket upgrade only — bug #240; needed
//     because browser/RN WS clients can't reliably set custom headers)
//   - X-Internal-Auth-Validated: true (from internal IPs only - pre-authenticated by main gateway)
func (g *Gateway) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow preflight without auth
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		isPublic := g.policyFor(r).Access.Anonymous()

		// 0) Trust the internal-auth headers the main gateway forwarded.
		//
		// These reached this point only because internalAuthMiddleware verified
		// a MAC over them keyed by the cluster secret; anything unsigned was
		// deleted before this middleware ran. The source IP is not consulted:
		// it used to be the only check, and the source IP of every public
		// request is 127.0.0.1 because Caddy proxies to localhost.
		if r.Header.Get(HeaderInternalAuthValidated) == "true" {
			ns := strings.TrimSpace(r.Header.Get(HeaderInternalAuthNamespace))
			// The MAC says a gateway of this cluster vouched for the caller;
			// it does not say the caller belongs here. A namespace gateway
			// serves one namespace, and an identity forwarded for another —
			// a stale target row sending one tenant's traffic to another's
			// gateway — is refused rather than served.
			if ns != "" && g.cfg != nil && !g.servesCoreRegistry() && ns != ownNamespace(g.cfg) {
				forbidden(w, CodeNamespaceMismatch, "this credential belongs to another namespace",
					map[string]any{"namespace": ownNamespace(g.cfg), "credential_namespace": ns})
				return
			}
			if ns != "" {
				// Pre-authenticated by main gateway - trust the namespace
				reqCtx := context.WithValue(r.Context(), CtxKeyNamespaceOverride, ns)
				// Bug #215: also rebuild ctxKeyJWT from the trusted
				// internal-auth headers (subject + custom claims) so
				// serverless host functions see a non-empty
				// caller_jwt_subject. We deliberately do NOT re-verify
				// the JWT here — namespace gateways may not share the
				// signing key with the main gateway, and the main gateway
				// already verified before forwarding. The trust gate is
				// the MAC, checked in internalAuthMiddleware.
				claims, err := claimsFromInternalAuthHeaders(r.Header, ns, time.Now())
				if err != nil {
					// The MAC verified, so this is the main gateway's own
					// bug rather than a forgery; the identity is dropped
					// rather than guessed at.
					g.logger.ComponentWarn("gateway", "dropped a hop identity whose token times cannot be read",
						zap.String("path", r.URL.Path), zap.Error(err))
				}
				if claims != nil {
					reqCtx = context.WithValue(reqCtx, ctxKeyJWT, claims)
				}
				// #148: hydrate the API-key caller's effective scopes from the
				// trusted internal-auth header so scopeMiddleware can enforce
				// them (the namespace gateway does not re-look-up the key).
				// The resources narrowing those scopes travel with them, so a
				// key limited to some functions is limited here too.
				if raw := strings.TrimSpace(r.Header.Get(HeaderInternalAuthScopes)); raw != "" {
					reqCtx = withKeyAuthority(reqCtx, raw,
						auth.KeyResourcesFromStored(r.Header.Get(HeaderInternalAuthKeyResources)))
					reqCtx = withKeyFingerprint(reqCtx, r.Header.Get(HeaderInternalAuthKeyFingerprint))
					reqCtx = g.markHopRequestKey(reqCtx, r)
				}
				next.ServeHTTP(w, r.WithContext(reqCtx))
				return
			}
			// A validated header with no namespace asserts nothing usable, so
			// the request authenticates on its own credentials or is refused.
		}

		// 1) Try JWT Bearer first if Authorization looks like one
		if authHeader := r.Header.Get("Authorization"); authHeader != "" {
			lower := strings.ToLower(authHeader)
			if strings.HasPrefix(lower, "bearer ") {
				tok := strings.TrimSpace(authHeader[len("Bearer "):])
				if strings.Count(tok, ".") == 2 {
					claims, err := g.authService.ParseAndVerifyJWT(tok)
					if err == nil {
						// Attach JWT claims and namespace to context
						ctx := context.WithValue(r.Context(), ctxKeyJWT, claims)
						if ns := strings.TrimSpace(claims.Namespace); ns != "" {
							ctx = context.WithValue(ctx, CtxKeyNamespaceOverride, ns)
						}
						// An exchanged key's authority is its row, not the
						// scopes claim copied into the token at mint.
						ctx, ok := g.withExchangedKeyScopes(w, ctx, claims, isPublic)
						if !ok {
							return
						}
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
					// A revoked token is not a malformed one. Falling through
					// would have it looked up as an API key and refused as an
					// unknown credential, which tells the holder nothing about
					// why their session stopped working.
					if g.refuseRevokedToken(w, r, err, isPublic) {
						return
					}
					if errors.Is(err, auth.ErrRevocationsUnavailable) && !isPublic {
						unavailable(w, CodeAuthUnavailable, authUnavailableMessage)
						return
					}
					if errors.Is(err, auth.ErrTokenExpired) && !isPublic {
						unauthorized(w, CodeAuthExpired, tokenExpiredMessage, nil)
						return
					}
					// If it looked like a JWT but failed verification, fall through to API key check
				}
			}
		}

		// 1b) WebSocket-only fallback: JWT in the `?jwt=` query parameter.
		//
		// Browser and React Native WebSocket clients can't reliably set custom
		// headers on the upgrade request — the WebSocket constructor either
		// ignores the headers argument (browsers) or silently strips
		// Authorization (RN iOS). Without a fallback, every authenticated WS
		// endpoint is unreachable from those platforms. Bug #240.
		//
		// We gate this ONLY on WS upgrade requests to keep JWTs out of normal
		// HTTP URLs (where they end up in access logs, referrer headers, and
		// browser history). For WS, the upgrade URL is only emitted on
		// connection establishment — much smaller exposure surface — and TLS
		// (wss://) keeps it off the wire in transit.
		//
		// After a successful verify, we STRIP the `jwt` query param from the
		// request before passing downstream (`stripJWTQueryParam`). This
		// shrinks the replay window: the token doesn't propagate through the
		// proxy hop to the namespace gateway, doesn't reach the backend
		// handler's logs, and doesn't show up in any downstream `r.URL`
		// inspection. Belt-and-suspenders given the trust we've already
		// established by verifying the signature.
		if isWebSocketUpgrade(r) {
			tok := strings.TrimSpace(r.URL.Query().Get("jwt"))
			// Cheap length sanity-check before invoking the verifier. Real
			// EdDSA / RS256 JWTs issued by this gateway are well under 4 KB.
			// Anything larger is either malformed or a DoS attempt.
			if tok != "" && len(tok) <= maxQueryJWTLength && strings.Count(tok, ".") == 2 {
				claims, err := g.authService.ParseAndVerifyJWT(tok)
				if g.refuseRevokedToken(w, r, err, isPublic) {
					return
				}
				if errors.Is(err, auth.ErrRevocationsUnavailable) && !isPublic {
					unavailable(w, CodeAuthUnavailable, authUnavailableMessage)
					return
				}
				if errors.Is(err, auth.ErrTokenExpired) && !isPublic {
					unauthorized(w, CodeAuthExpired, tokenExpiredMessage, nil)
					return
				}
				if err == nil {
					stripJWTQueryParam(r)
					ctx := context.WithValue(r.Context(), ctxKeyJWT, claims)
					if ns := strings.TrimSpace(claims.Namespace); ns != "" {
						ctx = context.WithValue(ctx, CtxKeyNamespaceOverride, ns)
					}
					ctx, ok := g.withExchangedKeyScopes(w, ctx, claims, isPublic)
					if !ok {
						return
					}
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Invalid JWT in query — fall through to API key check
				// rather than 401-ing here, in case the caller also supplied
				// a valid api_key as belt-and-suspenders.
			}
		}

		// 2) Fallback to API key (validate against DB)
		key, form := extractAPIKeyAndForm(r)
		// Said on the response whether or not the key turns out to be valid:
		// the spelling is going away regardless of what it carries.
		markDeprecatedCredential(w, form)
		if key == "" {
			if isPublic {
				next.ServeHTTP(w, r)
				return
			}
			unauthorized(w, CodeAuthMissing, "no credential was presented", nil)
			return
		}

		// Look up API key → namespace against the core/index registry
		// (g.apiKeyDB(): authClient when GlobalRQLiteDSN is set, else
		// g.client). Keys are created only in that registry (bugboard #162).
		// A namespace gateway's own RQLite is never authoritative for keys.
		entry, err := g.lookupAPIKeyEntry(r.Context(), key, g.apiKeyDB())
		if err != nil {
			if isPublic {
				next.ServeHTTP(w, r)
				return
			}
			if errors.Is(err, auth.ErrRevocationsUnavailable) {
				unavailable(w, CodeAuthUnavailable, authUnavailableMessage)
				return
			}
			unauthorized(w, CodeAuthInvalidKey, "this API key is not one this cluster knows", nil)
			return
		}

		// Attach auth metadata to context for downstream use. The scope set
		// (bugboard #148) is applied by scopeMiddleware; a key whose scopes
		// column is empty grants nothing. Its resources narrow it wherever
		// its scopes are read (callerPermissions).
		reqCtx := context.WithValue(r.Context(), ctxKeyAPIKey, key)
		reqCtx = context.WithValue(reqCtx, CtxKeyNamespaceOverride, entry.namespace)
		reqCtx = withKeyAuthority(reqCtx, entry.scopes, entry.resources)
		reqCtx = withKeyFingerprint(reqCtx, entry.authority().fingerprint)
		reqCtx = context.WithValue(reqCtx, ctxKeyKeyID, entry.id)
		markRequestKey(reqCtx, entry.id)
		g.recordDeprecatedCredential(r, entry.namespace, form)
		next.ServeHTTP(w, r.WithContext(reqCtx))
	})
}

// extractAPIKey reads the API key a request presents.
//
// The query string is read only on a WebSocket upgrade, where a browser cannot
// set a header. Everywhere else a credential in a URL ends up in the access
// log, in the Referer of the next request the page makes, and in history.
func extractAPIKey(r *http.Request) string {
	return auth.APIKeyFromRequest(r, isWebSocketUpgrade(r))
}

// extractAPIKeyAndForm reads the key and the spelling it arrived in.
func extractAPIKeyAndForm(r *http.Request) (string, auth.KeyForm) {
	return auth.APIKeyAndFormFromRequest(r, isWebSocketUpgrade(r))
}

// authorizationMiddleware enforces that the authenticated actor owns the namespace
// for certain protected paths (e.g., apps CRUD and storage APIs).
// Also enforces cross-namespace access control:
// - "default" namespace: accessible by any valid API key
// - Other namespaces: API key must belong to that specific namespace
func (g *Gateway) authorizationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip for public/OPTIONS paths only. What is public is the route's
		// declared policy; whoami and namespace status used to need their own
		// exemptions here because the path lists disagreed about them.
		policy := g.policyFor(r)
		if r.Method == http.MethodOptions || policy.Access.Anonymous() {
			if r.Method != http.MethodOptions && policy.NarrowedByGrant {
				var ok bool
				if r, ok = g.narrowOpenRoute(w, r); !ok {
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// The raw-database routes on the gateway that serves the cluster
		// registry are an operator's, whoever vouched for the caller. The
		// cross-namespace check below runs only when this gateway serves a
		// named namespace, which the cluster gateway does not — so nothing
		// stopped a tenant's admin key from exporting the registry, or
		// importing over it. It runs before the signed-hop skip: a hop's MAC
		// says a gateway of this cluster authenticated the caller, not that
		// the caller is an operator, and every node holds the key it is made
		// with.
		if !g.requireOperatorForCoreRegistry(w, r) {
			return
		}

		if !credentialServesHostNamespace(w, r) {
			return
		}

		// A request pre-authenticated by the main gateway skips the grant
		// lookup when what it was forwarded with already reaches the route —
		// an API key's scopes, a wallet's data plane. The main gateway
		// validated the credential and resolved the namespace, and the lookup
		// is ~300ms of registry round trips on every publish.
		//
		// The hop carries identity, not the grant, so a forwarded JWT caller
		// on a control-plane route (deploying a function, managing secrets)
		// has its grant resolved here. Skipping that refused every one of
		// them with INSUFFICIENT_SCOPE, which left a namespace's functions
		// manageable only by an API key (bugboard #427 made the namespace
		// gateway the only place they are managed).
		//
		// The header is here only because internalAuthMiddleware verified its
		// MAC. It used to be believed on the strength of the source IP, which
		// made this the shortest unauthenticated path to any namespace's data.
		needsGrant := g.forwardedCallerNeedsGrant(r, policy)
		forwarded := r.Header.Get(HeaderInternalAuthValidated) == "true"
		if forwarded && !needsGrant {
			next.ServeHTTP(w, r)
			return
		}

		// Cross-namespace access control for namespace gateways
		// The gateway's ClientNamespace determines which namespace this gateway serves
		gatewayNamespace := ""
		if g.cfg != nil {
			gatewayNamespace = strings.TrimSpace(g.cfg.ClientNamespace)
		}

		// Get user's namespace from context (derived from API key/JWT)
		userNamespace := ""
		if v := r.Context().Value(CtxKeyNamespaceOverride); v != nil {
			if s, ok := v.(string); ok {
				userNamespace = strings.TrimSpace(s)
			}
		}

		// For non-default namespace gateways, the API key must belong to this namespace
		// This enforces physical isolation: alice's gateway only accepts alice's API keys
		if servesNamedNamespace(gatewayNamespace) && userNamespace != "" && userNamespace != gatewayNamespace {
			g.logger.ComponentWarn(logging.ComponentGeneral, "cross-namespace access denied",
				zap.String("user_namespace", userNamespace),
				zap.String("gateway_namespace", gatewayNamespace),
				zap.String("path", r.URL.Path),
			)
			forbidden(w, CodeNamespaceMismatch, "this credential belongs to another namespace",
				map[string]any{"namespace": gatewayNamespace, "credential_namespace": userNamespace})
			return
		}

		// A route that does not require a grant still has to carry one when the
		// scope gate will ask for more than the data plane, or when the caller
		// holds a grant narrowed to a resource in the route's domain. Without
		// the first, an owner holds only the data plane and namespace list,
		// deployments and the database answer 403. Without the second, a grant
		// narrowed to sessions/* is the whole cache, and a reader reaches it.
		//
		// A forwarded wallet on an owned data-plane route (publish, push) takes
		// the same path: the ownership gate below is ~300ms of registry round
		// trips a request, and a hop has never paid it there. Its grant is read
		// through the cache and its role decides, which is what the gate would
		// have concluded for a wallet that holds one.
		if !policy.Ownership || (forwarded && grantIsDataPlane(policy)) {
			if needsGrant {
				grant, err := g.resolveRequestGrant(r, policy)
				if err != nil {
					g.refuseUnreadableGrant(w, err)
					return
				}
				if grant != nil {
					r = markGrant(r, grant)
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// Determine namespace from context
		ctx := r.Context()
		ns := ""
		if v := ctx.Value(CtxKeyNamespaceOverride); v != nil {
			if s, ok := v.(string); ok {
				ns = strings.TrimSpace(s)
			}
		}
		if ns == "" && g.cfg != nil {
			ns = strings.TrimSpace(g.cfg.ClientNamespace)
		}
		if ns == "" {
			forbidden(w, CodeNamespaceMismatch, "the namespace this credential belongs to could not be resolved", nil)
			return
		}

		// Identify actor from context
		ownerType := ""
		ownerID := ""
		apiKeyFallback := ""

		if v := ctx.Value(ctxKeyJWT); v != nil {
			if claims, ok := v.(*auth.JWTClaims); ok && claims != nil && strings.TrimSpace(claims.Sub) != "" {
				// Determine subject type. A subject that is not
				// recognisably a wallet is a key: see auth.IsWalletSubject for
				// why that is the safe direction.
				subj := strings.TrimSpace(claims.Sub)
				ownerType = subjectOwnerType(subj)
				ownerID = subj
			}
		}
		// A key is identified by its stored form, the value api_keys.key and
		// its service-account principal hold. An exchanged token's subject is
		// already that form (the exchange mints it so); a bare key is the raw
		// key and is hashed here, once.
		if ownerType == "" && ownerID == "" {
			if v := ctx.Value(ctxKeyAPIKey); v != nil {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					ownerType = "api_key"
					ownerID = g.authService.HashAPIKey(strings.TrimSpace(s))
				}
			}
		} else if ownerType == "wallet" {
			// If we have a JWT wallet, also capture the API key as fallback
			if v := ctx.Value(ctxKeyAPIKey); v != nil {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					apiKeyFallback = g.authService.HashAPIKey(strings.TrimSpace(s))
				}
			}
		}

		if ownerType == "" || ownerID == "" {
			forbidden(w, CodeAuthMissing, "this route needs an identity and the credential carries none", nil)
			return
		}

		g.logger.ComponentInfo("gateway", "namespace auth check",
			zap.String("namespace", ns),
			zap.String("owner_type", ownerType),
			zap.String("owner_id", loggableOwnerID(ownerType, ownerID)),
		)

		// Check ownership in the registry, where grants live.
		db := g.grantDB()
		internalCtx := client.WithInternalAuth(ctx)
		// Ensure namespace exists and get id
		if _, err := db.Query(internalCtx, "INSERT OR IGNORE INTO namespaces(name) VALUES (?)", ns); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nres, err := db.Query(internalCtx, "SELECT id FROM namespaces WHERE name = ? LIMIT 1", ns)
		if err != nil || nres == nil || nres.Count == 0 || len(nres.Rows) == 0 || len(nres.Rows[0]) == 0 {
			forbidden(w, CodeNamespaceMismatch, "no such namespace", nil)
			return
		}
		nsID := nres.Rows[0][0]

		// grantFor reports the live grant (ot, oid) holds in this namespace, if
		// any. For a key, oid is its stored form, never the raw key.
		grantFor := func(ot, oid string) *auth.Grant {
			for _, c := range principalIdentifierCandidates(ot, oid) {
				grant, gerr := g.authService.GrantIn(internalCtx, db, nsID, grantPrincipalType(ot), c)
				if gerr == nil {
					return grant
				}
			}
			return nil
		}

		grant := grantFor(ownerType, ownerID)
		// A JWT wallet can also fall back to its API key (also stored hashed).
		if grant == nil && ownerType == "wallet" && apiKeyFallback != "" {
			grant = grantFor("api_key", apiKeyFallback)
		}

		if grant == nil {
			forbidden(w, CodeOwnershipRequired, "this credential holds no grant in this namespace", nil)
			return
		}

		// The grant travels with the request. It used to be a boolean — "a
		// SIWE wallet owner was confirmed" — which is why every human with
		// access was an admin: there was nothing else the flag could say.
		//
		// It is set for a key-authenticated caller too, not only a wallet. The
		// scope gate reads it only for a wallet JWT (a key's own scopes column
		// is what says what it may reach), but the data paths read its selector
		// for either — a service account narrowed to one topic pattern is as
		// real a thing as a teammate narrowed to one (feat-371).
		r = markGrant(r, grant)

		next.ServeHTTP(w, r)
	})
}

// requestNamespace is the namespace the credential names, or the one this
// gateway serves when it names none.
func (g *Gateway) requestNamespace(r *http.Request) string {
	if s, ok := r.Context().Value(CtxKeyNamespaceOverride).(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if g.cfg != nil {
		return strings.TrimSpace(g.cfg.ClientNamespace)
	}
	return ""
}

// lookupRequestGrant is the live grant the caller holds in the namespace the
// credential names, or nil when there is none. It does not write a response:
// the ownership gate is what turns "none" into a 403.
//
// A registry that cannot be read is an error, never "none": "none" leaves a
// wallet the whole data plane, so reading a failure as it would widen a
// narrowed grant for as long as the registry is unreachable.
func (g *Gateway) lookupRequestGrant(r *http.Request) (*auth.Grant, error) {
	if g.authService == nil {
		return nil, nil
	}
	ctx := r.Context()
	ns := g.requestNamespace(r)
	if ns == "" {
		return nil, nil
	}
	ownerType, ownerID := requestPrincipal(ctx)
	if ownerType == "" {
		return nil, nil
	}

	db := g.grantDB()
	internalCtx := client.WithInternalAuth(ctx)
	// Read only. The ownership gate is what creates a missing namespace row;
	// a list or a deploy must not insert one as a side effect of the check.
	nres, err := db.Query(internalCtx, "SELECT id FROM namespaces WHERE name = ? LIMIT 1", ns)
	if err != nil {
		return nil, fmt.Errorf("read namespace %q to resolve the caller's grant: %w", ns, err)
	}
	if nres == nil || nres.Count == 0 || len(nres.Rows) == 0 || len(nres.Rows[0]) == 0 {
		return nil, nil
	}
	nsID := nres.Rows[0][0]

	// The subject of an exchanged key's token is the key's stored form, which
	// is what its principal is recorded under.
	for _, c := range principalIdentifierCandidates(ownerType, ownerID) {
		grant, gerr := g.authService.GrantIn(internalCtx, db, nsID, grantPrincipalType(ownerType), c)
		switch {
		case gerr == nil:
			return grant, nil
		case !errors.Is(gerr, auth.ErrNotAMember):
			return nil, fmt.Errorf("read the caller's grant in %q: %w", ns, gerr)
		}
	}
	return nil, nil
}

// requestPrincipal is the kind ("wallet", "app" or "api_key") and identifier
// of the JWT's subject, or empty when the request carries none.
func requestPrincipal(ctx context.Context) (string, string) {
	claims, _ := ctx.Value(ctxKeyJWT).(*auth.JWTClaims)
	if claims == nil {
		return "", ""
	}
	subj := strings.TrimSpace(claims.Sub)
	if subj == "" {
		return "", ""
	}
	return subjectOwnerType(subj), subj
}

// subjectOwnerType is the kind of principal a JWT subject names. A workload's
// subject (app:<ns>/<name>) is not a wallet, and auth.IsAPIKeySubject is "not a
// wallet", so it used to be read as a key: its grant, written under the app
// principal, was looked for under a service account and never found — an
// app's selector was never applied and an owned route refused it.
func subjectOwnerType(subj string) string {
	switch {
	case auth.IsWorkloadSubject(subj):
		return "app"
	case auth.IsAPIKeySubject(subj):
		return "api_key"
	default:
		return "wallet"
	}
}

// grantPrincipalType is the principal type a grant for an owner kind is
// recorded under.
func grantPrincipalType(ownerType string) auth.PrincipalType {
	switch ownerType {
	case "app":
		return auth.PrincipalApp
	case "api_key":
		return auth.PrincipalServiceAccount
	default:
		return auth.PrincipalWallet
	}
}

// loggableOwnerID is how an owner appears in a log line. For an API key the
// owner id is the key's stored form, which is the raw key — a live credential —
// on a cluster with no HMAC secret configured, so its fingerprint is logged
// instead (auth.KeyFingerprint). A wallet address is public and logged as is
// (bugboard #2508).
func loggableOwnerID(ownerType, ownerID string) string {
	if ownerType != "api_key" {
		return ownerID
	}
	return auth.KeyFingerprint(ownerID)
}

// principalIdentifierCandidates returns the identifiers to look a principal up
// under.
//
// A key's principal is recorded under the key's stored form: the HMAC when one
// is configured, the raw key when it is not (MigratePlaintextAPIKeys moves any
// leftover raw-key principal before the gateway serves). ownerID must already
// be that form — an exchanged token's subject is, and a bare key is hashed by
// the caller — and it is the only candidate. Hashing it here as well hashed an
// exchanged token's subject a second time, so a key presented as a token held
// no grant, and the CLI, which sends key management a token rather than the
// key, was refused on every owned route (change-975).
//
// Wallets are stored normalised (auth.NormalizeWallet), but rows written before
// that was true kept whatever case the client sent — so a checksummed address
// could hold a grant under a spelling no later login matched. The normalised
// form is checked first and the presented form second, which keeps those rows
// working until migration 042 has run everywhere.
//
// For every other owner type the value is used as-is.
func principalIdentifierCandidates(ownerType, ownerID string) []string {
	if ownerType == "wallet" {
		if normalized := auth.NormalizeWallet(ownerID); normalized != ownerID {
			return []string{normalized, ownerID}
		}
	}
	return []string{ownerID}
}

// corsMiddleware applies CORS headers. Allows requests from the configured base
// domain and its subdomains, from this machine (getAllowedOrigin), from the
// origins the namespace a ns-<name> host names lists (cors.go), and from the
// public explorer's origin on the chain's read routes (cors_explorer.go). Any
// other origin is answered with the base domain, which a browser then refuses.
func (g *Gateway) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedOrigin := g.allowedOriginFor(r)
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		w.Header().Set("Access-Control-Expose-Headers", corsExposeHeaders)
		w.Header().Set("Access-Control-Max-Age", strconv.Itoa(600))
		if allowedOrigin != "*" {
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// getAllowedOrigin returns the allowed origin for CORS by the rule every
// gateway applies (corsorigins.BuiltIn): the base domain and any name under
// it, and loopback — localhost, any *.localhost name and the loopback
// addresses, which is where a dev server and a Tauri webview serve a page
// from (tauri://localhost, http(s)://tauri.localhost). A base domain is
// required (ValidateConfig); a gateway without one used to answer "*" here.
func (g *Gateway) getAllowedOrigin(origin string) string {
	if origin != "" && corsorigins.BuiltIn(origin, g.cfg.BaseDomain) {
		return origin
	}
	return "https://" + g.cfg.BaseDomain
}

// remoteAddrIP extracts the actual TCP peer IP from r.RemoteAddr, ignoring
// X-Forwarded-For and other proxy headers. Use this for security-sensitive
// checks like internal auth validation where we need to verify the direct
// connection source (e.g. WireGuard proxy IP), not the original client.
func remoteAddrIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// getClientIP is the address a request is attributed to (request log, namespace affinity, the
// X-Forwarded-For handed to a proxied service): the peer address, or, when the peer is the local
// reverse proxy or another node's gateway on the mesh, the last X-Forwarded-For entry. The first
// entry is whatever the caller wrote and is never used, and neither is X-Real-IP. See
// clientkey.Attribute; rate limits use clientkey.Resolve instead.
func getClientIP(r *http.Request) string {
	return clientkey.Attribute(r)
}

// domainRoutingMiddleware handles requests to deployment domains and namespace gateways
// This must come BEFORE auth middleware so deployment domains work without API keys
//
// Domain routing patterns:
// - ns-{namespace}.{baseDomain} -> Namespace gateway (proxy to namespace cluster)
// - {name}-{random}.{baseDomain} -> Deployment domain
// - {name}.{baseDomain} -> Deployment domain (legacy)
// - {name}.node-xxx.{baseDomain} -> Legacy format (deprecated, returns 404 for new deployments)
func (g *Gateway) domainRoutingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0] // Strip port

		baseDomain := g.cfg.BaseDomain

		// Only process base domain and its subdomains
		if !strings.HasSuffix(host, "."+baseDomain) && host != baseDomain {
			next.ServeHTTP(w, r)
			return
		}

		// Check for namespace gateway domain FIRST (before API path skip)
		// Namespace subdomains (ns-{name}.{baseDomain}) must be proxied to namespace gateways
		// regardless of path — including /v1/ paths
		suffix := "." + baseDomain
		if strings.HasSuffix(host, suffix) {
			subdomain := strings.TrimSuffix(host, suffix)
			if strings.HasPrefix(subdomain, "ns-") {
				namespaceName := strings.TrimPrefix(subdomain, "ns-")
				// Scoped API-key management (bugboard #148) operates on the MAIN
				// cluster RQLite, where API keys are validated. Namespace gateways
				// have isolated RQLites without the authoritative api_keys table,
				// so proxying these writes would land keys in the wrong DB (they'd
				// never authenticate). Serve them on the main gateway instead,
				// pinning the namespace from the subdomain.
				if g.policyFor(r).MainGateway {
					ctx := context.WithValue(r.Context(), CtxKeyNamespaceOverride, namespaceName)
					r = r.WithContext(context.WithValue(ctx, hostNamespaceKey{}, namespaceName))
					next.ServeHTTP(w, r)
					return
				}
				g.handleNamespaceGatewayRequest(w, r, namespaceName)
				return
			}
		}

		// Skip API paths (they should use JWT/API key auth on the main gateway)
		if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/.well-known/") {
			next.ServeHTTP(w, r)
			return
		}

		// The apex serves the platform's own pages — the public status page
		// and its assets, and the liveness probe — rather than looking for a
		// deployment on the bare domain, where none is allowed. Subdomains
		// belong to deployments, so /status on an app's domain is the app's.
		if host == baseDomain && isPlatformPage(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Check if deployment handlers are available
		if g.deploymentService == nil || g.staticHandler == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Try to find deployment by domain
		deployment, err := g.getDeploymentByDomain(r.Context(), host)
		if errors.Is(err, ErrAmbiguousLegacyHost) {
			// Answered as an unknown host: which namespaces share the name is
			// not the visitor's to learn.
			g.logger.ComponentWarn(logging.ComponentGeneral, "refused an ambiguous legacy deployment host",
				zap.String("host", host), zap.Error(err))
			http.NotFound(w, r)
			return
		}
		if err != nil {
			g.logger.ComponentError(logging.ComponentGeneral, "deployment host lookup failed",
				zap.String("host", host), zap.Error(err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if deployment == nil {
			// Domain matches .{baseDomain} but no deployment found
			http.NotFound(w, r)
			return
		}

		// Inject deployment context
		markTrafficNamespace(r, deployment.Namespace)
		ctx := context.WithValue(r.Context(), CtxKeyNamespaceOverride, deployment.Namespace)

		// Route based on deployment type
		if deployment.Port == 0 {
			// Static deployment - serve from IPFS
			g.staticHandler.HandleServe(w, r.WithContext(ctx), deployment)
		} else {
			// Dynamic deployment - proxy to local port
			g.proxyToDynamicDeployment(w, r.WithContext(ctx), deployment)
		}
	})
}

// handleNamespaceGatewayRequest proxies requests to a namespace's dedicated gateway cluster
// This enables physical isolation where each namespace has its own RQLite, Olric, and Gateway
//
// IMPORTANT: This function validates auth against the MAIN cluster RQLite before proxying.
// The validated namespace is passed to the namespace gateway via X-Internal-Auth-* headers.
// This is necessary because namespace gateways have their own isolated RQLite that doesn't
// contain API keys (API keys are stored in the main cluster RQLite only).
func (g *Gateway) handleNamespaceGatewayRequest(w http.ResponseWriter, r *http.Request, namespaceName string) {
	// Validate auth against main cluster RQLite BEFORE proxying
	// This ensures API keys work even though they're not in the namespace's RQLite
	markPhase(r, "routing")
	a := g.namespaceProxyAuthFor(r)
	markPhase(r, "auth")
	g.proxyToNamespaceGateway(w, r, namespaceName, a)
}

// namespaceProxyAuth is the outcome of validateAuthForNamespaceProxy, carried
// to the proxy so a caller that already validated the request does not do it
// twice.
type namespaceProxyAuth struct {
	namespace string
	claims    *auth.JWTClaims
	key       keyAuthority
	errMsg    string
}

func (g *Gateway) namespaceProxyAuthFor(r *http.Request) namespaceProxyAuth {
	ns, claims, key, errMsg := g.validateAuthForNamespaceProxy(r)
	return namespaceProxyAuth{namespace: ns, claims: claims, key: key, errMsg: errMsg}
}

// proxyToNamespaceGateway refuses a request whose credential does not belong
// to namespaceName and forwards the rest to that namespace's gateway, carrying
// the validated identity in signed internal-auth headers.
func (g *Gateway) proxyToNamespaceGateway(w http.ResponseWriter, r *http.Request, namespaceName string, a namespaceProxyAuth) {
	markTrafficNamespace(r, namespaceName)
	if refuseCrossSiteUpgrade(w, r) {
		return
	}
	validatedNamespace, validatedClaims, validatedKey, authErr := a.namespace, a.claims, a.key, a.errMsg
	markRequestKey(r.Context(), validatedKey.id)
	isWS := isWebSocketUpgrade(r)
	isPublic := g.policyFor(r).Access.Anonymous()

	// Bug #240/#249 root-cause hardening: previously, when
	// validateAuthForNamespaceProxy returned an empty namespace AND empty
	// error (i.e. "no credentials found"), the request fell through to a
	// silent forward to the namespace gateway WITHOUT internal-auth
	// headers. The namespace gateway then rejected the request with 401
	// "missing API key" in ~60µs. From the client's perspective the 401
	// appeared opaque; from our side the failure was logged only on the
	// namespace gateway (which itself can't validate API keys — they
	// live in the main cluster RQLite). This created a confusing
	// debugging experience and was the root cause of AnChat's
	// "intermittent 401" reports on the WS path.
	//
	// Two parts to the fix:
	//   1. Reject at MAIN when no credentials were extractable AND the
	//      path requires auth. Surfaces the failure with a clear message
	//      AT the gateway tier that actually knows about API keys.
	//   2. Log every WS upgrade auth outcome with enough context to
	//      diagnose the intermittent reports we've been seeing
	//      (presence of relevant query params, headers we care about,
	//      and the actor IP). Logged at debug level for success and
	//      warn for the reject path so steady-state noise stays low.
	// A revoked device's token is refused on a public route too
	// (refuseRevokedToken says why).
	if authErr != "" && (!isPublic || authErr == deviceRevokedMessage) {
		if isWS {
			g.logger.ComponentWarn(logging.ComponentGeneral,
				"namespace-proxy WS upgrade rejected: auth error",
				zap.String("namespace_target", namespaceName),
				zap.String("auth_err", authErr),
				zap.String("path", r.URL.Path),
				zap.String("client_ip", getClientIP(r)),
				zap.Bool("has_api_key_query", r.URL.Query().Get("api_key") != ""),
				zap.Bool("has_token_query", r.URL.Query().Get("token") != ""),
				zap.Bool("has_jwt_query", r.URL.Query().Get("jwt") != ""),
				zap.Bool("has_authz_header", r.Header.Get("Authorization") != ""),
				zap.Bool("has_xapikey_header", r.Header.Get("X-API-Key") != ""),
				zap.String("connection_header", r.Header.Get("Connection")),
				zap.String("upgrade_header", r.Header.Get("Upgrade")),
				zap.String("user_agent", r.Header.Get("User-Agent")),
			)
		}
		g.refuseProxyCredential(w, r, authErr, validatedClaims)
		return
	}

	// No-credentials path: previously fell through to silent forward.
	// Now: reject at main with diagnostic context. Namespace gateways
	// cannot validate API keys themselves (no shared rqlite for them),
	// so forwarding unauthenticated requests can only ever produce
	// opaque 401s downstream.
	if validatedNamespace == "" && !isPublic {
		g.logger.ComponentWarn(logging.ComponentGeneral,
			"namespace-proxy request rejected: no credentials extracted",
			zap.String("namespace_target", namespaceName),
			zap.String("path", r.URL.Path),
			zap.Bool("is_ws_upgrade", isWS),
			zap.String("client_ip", getClientIP(r)),
			zap.Bool("has_api_key_query", r.URL.Query().Get("api_key") != ""),
			zap.Bool("has_token_query", r.URL.Query().Get("token") != ""),
			zap.Bool("has_jwt_query", r.URL.Query().Get("jwt") != ""),
			zap.Bool("has_authz_header", r.Header.Get("Authorization") != ""),
			zap.Bool("has_xapikey_header", r.Header.Get("X-API-Key") != ""),
			zap.String("connection_header", r.Header.Get("Connection")),
			zap.String("upgrade_header", r.Header.Get("Upgrade")),
			zap.String("origin", r.Header.Get("Origin")),
			zap.String("user_agent", r.Header.Get("User-Agent")),
			zap.Int("raw_query_len", len(r.URL.RawQuery)),
		)
		unauthorized(w, CodeAuthMissing, "no credential was presented", nil)
		return
	}

	// A pending device's session goes no further than this gateway on a route
	// that does not admit it.
	if !g.admitsPendingDevice(w, r, validatedClaims) {
		return
	}

	// If auth succeeded, ensure the API key belongs to the target namespace
	if validatedNamespace != "" && validatedNamespace != namespaceName {
		g.logger.ComponentWarn(logging.ComponentGeneral,
			"namespace-proxy request rejected: API key namespace mismatch",
			zap.String("namespace_target", namespaceName),
			zap.String("validated_namespace", validatedNamespace),
			zap.String("path", r.URL.Path),
			zap.Bool("is_ws_upgrade", isWS),
			zap.String("client_ip", getClientIP(r)),
		)
		forbidden(w, CodeNamespaceMismatch, "this credential belongs to another namespace",
			map[string]any{"namespace": namespaceName, "credential_namespace": validatedNamespace})
		return
	}

	// The namespace's attestation policy, judged and counted here, where the
	// cluster's request metrics are kept (attestation_middleware.go).
	if !g.admitAttestedProxy(w, r, namespaceName, a) {
		return
	}

	// Success-path diagnostic for WS upgrades. Logged at debug to keep
	// the steady-state log volume low; flip the gateway log level to
	// `debug` to capture per-upgrade audit trail when reproducing
	// AnChat-style intermittent failures.
	if isWS {
		g.logger.ComponentDebug(logging.ComponentGeneral,
			"namespace-proxy WS upgrade authenticated, forwarding",
			zap.String("namespace", namespaceName),
			zap.String("path", r.URL.Path),
			zap.String("client_ip", getClientIP(r)),
			zap.Bool("has_jwt_claims", validatedClaims != nil),
		)
	}

	var targets []namespaceGatewayTarget
	if g.mwCache != nil {
		if cached, ok := g.mwCache.GetNamespaceTargets(namespaceName); ok {
			for _, t := range cached {
				targets = append(targets, namespaceGatewayTarget(t))
			}
		}
	}

	// Cache miss — look the namespace's live gateways up in the registry.
	if len(targets) == 0 {
		var err error
		targets, err = g.namespaceGatewayTargets(r.Context(), namespaceName)
		if err != nil {
			// A registry that cannot answer says nothing about the namespace:
			// reporting "not found" told a client with a perfectly live tenant
			// that it does not exist, and not to retry.
			g.logger.ComponentError(logging.ComponentGeneral, "could not read the namespace's gateways",
				zap.String("namespace", namespaceName), zap.Error(err))
			httputil.WriteRPCError(w, http.StatusServiceUnavailable,
				httputil.ErrCodeServiceUnavailable,
				"the namespace's gateways could not be looked up right now; retry shortly",
				httputil.WithRetryable())
			return
		}
		g.retainBreakers(namespaceName, targets)
		if len(targets) == 0 {
			g.logger.ComponentWarn(logging.ComponentGeneral, "namespace gateway not found",
				zap.String("namespace", namespaceName))
			http.Error(w, "Namespace gateway not found", http.StatusNotFound)
			return
		}
		if g.mwCache != nil {
			cacheTargets := make([]gatewayTarget, len(targets))
			for i, t := range targets {
				cacheTargets[i] = gatewayTarget(t)
			}
			g.mwCache.SetNamespaceTargets(namespaceName, cacheTargets)
		}
	}

	if len(targets) == 0 {
		http.Error(w, "Namespace gateway not available", http.StatusServiceUnavailable)
		return
	}
	markPhase(r, "targets")

	// Keep ordering deterministic before hashing, otherwise DB row order can vary.
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ip == targets[j].ip {
			return targets[i].port < targets[j].port
		}
		return targets[i].ip < targets[j].ip
	})

	// Build ordered target list: local gateway first, then hash-selected, then remaining.
	// This ordering is used by the circuit breaker fallback loop below.
	orderedTargets := make([]namespaceGatewayTarget, 0, len(targets))
	localIdx := -1
	if g.localWireGuardIP != "" {
		for i, t := range targets {
			if t.ip == g.localWireGuardIP {
				orderedTargets = append(orderedTargets, t)
				localIdx = i
				break
			}
		}
	}

	// Consistent hashing for affinity (keeps WS subscribe/publish on same node)
	var affinityKey string
	if apiKey := extractAPIKey(r); apiKey != "" {
		affinityKey = namespaceName + "|" + apiKey
	} else if authz := strings.TrimSpace(r.Header.Get("Authorization")); authz != "" {
		affinityKey = namespaceName + "|" + authz
	} else {
		affinityKey = namespaceName + "|" + getClientIP(r)
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(affinityKey))
	hashIdx := int(hasher.Sum32()) % len(targets)
	if hashIdx != localIdx {
		orderedTargets = append(orderedTargets, targets[hashIdx])
	}
	for i, t := range targets {
		if i != localIdx && i != hashIdx {
			orderedTargets = append(orderedTargets, t)
		}
	}

	// Select the first target whose circuit breaker allows a request through.
	// This provides automatic failover when a namespace gateway node is down.
	var selected namespaceGatewayTarget
	var cb *CircuitBreaker
	selectedIdx := -1
	for i, candidate := range orderedTargets {
		candidateCB := g.circuitBreakers.ForNamespaceGateway(namespaceName, candidate.ip)
		if candidateCB.Allow() {
			selected = candidate
			cb = candidateCB
			selectedIdx = i
			break
		}
	}
	if selected.ip == "" {
		// Bug #219: emit canonical envelope with retryable=true (transient).
		httputil.WriteRPCError(w, http.StatusServiceUnavailable,
			httputil.ErrCodeServiceUnavailable,
			"namespace gateway unavailable: all upstream circuits are open. "+
				"Wait a few seconds and retry, or check `orama status report` for unhealthy nodes.",
			httputil.WithRetryable())
		return
	}

	// Handle WebSocket upgrade requests specially (http.Client can't handle 101 Switching Protocols)
	if isWebSocketUpgrade(r) {
		// The identity travels in the signed headers below, so a `?jwt=` the
		// credential may have been read from is not carried over the hop, where
		// it would sit in the proxied request's RawQuery and the inner
		// gateway's logs.
		if validatedNamespace != "" {
			stripJWTQueryParam(r)
		}
		// Set forwarding headers on the original request
		r.Header.Set("X-Forwarded-For", getClientIP(r))
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", r.Host)
		// SECURITY (bug #215 follow-up): drop any X-Internal-Auth-* headers
		// the external client may have set BEFORE applying the trusted
		// values from this gateway. Prevents identity spoofing on the
		// namespace gateway, which gates on source IP only.
		stripInboundInternalAuthHeaders(r.Header)
		// Set internal auth headers if auth was validated
		if validatedNamespace != "" {
			r.Header.Set(HeaderInternalAuthValidated, "true")
			r.Header.Set(HeaderInternalAuthNamespace, validatedNamespace)
			// Bug #215: forward validated JWT subject + custom claims so the
			// namespace gateway's auth middleware can hydrate ctxKeyJWT and
			// host functions see a non-empty caller_jwt_subject.
			setInternalAuthJWTHeaders(r.Header, validatedClaims)
			setInternalAuthKeyHeaders(r.Header, validatedKey)
			// The MAC is what makes any of this believable on the other side.
			// Without it the namespace gateway would drop every header above,
			// so a hop that cannot be signed is refused rather than sent as an
			// assertion this gateway cannot back.
			if err := signInternalAuthHeaders(g.internalAuthKey, r.Header, r.Method, r.URL.Path, time.Now()); err != nil {
				cb.Abandon()
				g.logger.ComponentError("gateway", "cannot delegate auth to the namespace gateway",
					zap.String("namespace", validatedNamespace), zap.Error(err))
				writeError(w, http.StatusServiceUnavailable,
					"this gateway has no cluster secret, so it cannot authenticate itself to the namespace gateway")
				return
			}
		}
		g.proxyNamespaceWebSocket(w, r, orderedTargets, selectedIdx, cb, namespaceName)
		return
	}

	// A request refused here never reached the member, so it says nothing about
	// it, and must not hold the member's half-open probe slot.
	if refuseOversizedProxyBody(w, r) {
		cb.Abandon()
		return
	}
	// Only a caller whose credential the proxy validated gets the long
	// budget; an anonymous call to a public function keeps the server's.
	if validatedNamespace != "" && !extendTransferDeadlines(w, r) {
		cb.Abandon()
		return
	}

	// Proxy regular HTTP request to the namespace gateway. A member that cannot
	// be dialed (its gateway restarting, its node down) never received the
	// request, so the next member whose circuit allows it is tried instead of
	// answering 503 until that member's circuit opens (a soak restarting one
	// member's gateway saw another node answer "connection refused", stagenet
	// 2026-10-04). The body goes along only while nothing has read from it.
	// An empty body stays http.NoBody: the transport sends a length of 0 only
	// for that, and any other body of length 0 as one of unknown length.
	var body io.ReadCloser = http.NoBody
	var undialed *undialedBody
	if r.Body != nil && r.Body != http.NoBody {
		undialed = &undialedBody{ReadCloser: r.Body}
		body = undialed
	}
	// Pick the proxy timeout based on the path's expected work bound.
	// Defaults to 30s for fast paths; bumps to 300s for known long-running
	// classes (uploads, function invocations) so we don't truncate before
	// the namespace gateway's own per-function timeout fires.
	//
	// Bug #219: function invokes were truncated at 30s and surfaced as
	// "Namespace gateway unavailable" — misleading because the gateway IS
	// up, the FUNCTION just exceeded its budget. Aligning the proxy
	// timeout with the function-level cap ensures the namespace gateway's
	// proper TIMEOUT envelope reaches the client first.
	proxyTimeout := 30 * time.Second
	if isLongRunningProxyPath(r.URL.Path) {
		proxyTimeout = longProxyTimeout
	}

	httpClient := &http.Client{Timeout: proxyTimeout, Transport: g.proxyTransport}
	var resp *http.Response
	var lastErr error
	for i := selectedIdx; i < len(orderedTargets); i++ {
		candidate := orderedTargets[i]
		candidateCB := cb
		if i != selectedIdx {
			if candidateCB = g.circuitBreakers.ForNamespaceGateway(namespaceName, candidate.ip); !candidateCB.Allow() {
				continue
			}
		}
		proxyReq, err := g.namespaceProxyRequest(r, candidate.ip+":"+strconv.Itoa(candidate.port), body,
			validatedNamespace, validatedClaims, validatedKey)
		if errors.Is(err, errNoClusterSecret) {
			candidateCB.Abandon()
			g.logger.ComponentError("gateway", "cannot delegate auth to the namespace gateway",
				zap.String("namespace", validatedNamespace), zap.Error(err))
			writeError(w, http.StatusServiceUnavailable, errNoClusterSecret.Error())
			return
		}
		if err != nil {
			candidateCB.Abandon()
			g.logger.ComponentError(logging.ComponentGeneral, "failed to create namespace gateway proxy request",
				zap.String("namespace", namespaceName), zap.String("error", httputil.FailureReason(err)))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		resp, err = httpClient.Do(proxyReq)
		if err == nil {
			cb = candidateCB
			break
		}
		lastErr = err
		// A client that left says nothing about the member: its dial fails
		// with the cancelled context, and counting that against every member
		// would let aborted requests open their circuits. Nor does a body the
		// client failed to deliver: the transport returns the error that
		// reading it gave, and the member never saw the end of the request.
		if r.Context().Err() != nil || (undialed != nil && undialed.failedRead(err)) {
			candidateCB.Abandon()
			break
		}
		candidateCB.RecordFailure(httputil.FailureReason(err))
		g.logger.ComponentError(logging.ComponentGeneral, "namespace gateway proxy request failed",
			zap.String("namespace", namespaceName),
			zap.String("target", candidate.ip),
			zap.String("error", httputil.FailureReason(err)),
		)
		if !isDialFailure(err) || (undialed != nil && undialed.read) {
			break
		}
	}
	// Every attempt, failed-over members included, is one step.
	markPhase(r, "upstream")
	if resp == nil {
		// Distinguish timeout from connection failure so clients get an
		// actionable code (#219). "Namespace gateway unavailable" was
		// emitted indiscriminately and sent operators down the wrong
		// debug path.
		if isProxyTimeoutErr(lastErr) {
			httputil.WriteRPCError(w, http.StatusGatewayTimeout,
				httputil.ErrCodeTimeout, proxyTimeoutMessage(r.URL.Path, proxyTimeout),
				proxyFailureRetryable(r.Method, lastErr))
			return
		}
		httputil.WriteRPCError(w, http.StatusServiceUnavailable,
			httputil.ErrCodeServiceUnavailable, namespaceGatewayUnavailableMessage,
			proxyFailureRetryable(r.Method, lastErr))
		return
	}
	defer resp.Body.Close()

	if isNamespaceGatewayFailure(resp) {
		cb.RecordFailure(fmt.Sprintf("the namespace gateway answered %d", resp.StatusCode))
	} else {
		cb.RecordSuccess()
	}

	copyProxiedHeaders(w, resp)

	// Write status code and body
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// proxyToDynamicDeployment proxies requests to a dynamic deployment's local port
// If the deployment is on a different node, it forwards the request to that node.
// With replica support, it first checks if the current node is a replica and can
// serve the request locally using the replica's port.
func (g *Gateway) proxyToDynamicDeployment(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment) {
	if deployment.Port == 0 {
		http.Error(w, "Deployment has no assigned port", http.StatusServiceUnavailable)
		return
	}

	// Check if request was already forwarded by another node (loop prevention)
	proxyNode := r.Header.Get("X-Orama-Proxy-Node")

	// Check if this deployment is on the current node (primary)
	if g.nodePeerID != "" && deployment.HomeNodeID != "" &&
		deployment.HomeNodeID != g.nodePeerID {

		// Check if this node is a replica and can serve locally. This holds for
		// a request another node forwarded here as well: a replica's port is
		// allocated on its own node and is not the home node's, which is the
		// one the deployments row carries.
		var replicaErr error
		if g.replicaManager != nil {
			var replicaPort int
			replicaPort, replicaErr = g.replicaManager.GetReplicaPort(r.Context(), deployment.ID, g.nodePeerID)
			if replicaErr == nil && replicaPort > 0 {
				// This node is a replica — serve locally using the replica's port
				g.logger.Debug("Serving from local replica",
					zap.String("deployment", deployment.Name),
					zap.Int("replica_port", replicaPort),
				)
				deployment.Port = replicaPort
				// Fall through to local proxy below
				goto serveLocal
			}
		}

		// Forwarded here, yet this node runs no active replica of it: the
		// forwarding node's replica list is stale. The home node's port is
		// whatever else this machine allocated that number to, so it is not
		// dialed; the unmarked 503 tells the forwarder to try its next replica.
		if proxyNode != "" {
			g.warnStaleReplicaList(deployment, proxyNode, replicaErr)
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}

		// Not a replica on this node — proxy to the home node. localhost:port
		// is whatever else this machine allocated that number to, so a failed
		// forward must not be served from here.
		if g.proxyCrossNodeWithReplicas(w, r, deployment) {
			return
		}
		if r.Context().Err() != nil {
			return // The client left: not a gateway error, and nobody to answer.
		}
		g.logger.Error("Cross-node proxy failed",
			zap.String("deployment", deployment.Name),
			zap.String("home_node", deployment.HomeNodeID),
		)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

serveLocal:

	// Create a simple reverse proxy to localhost
	targetHost := "localhost:" + strconv.Itoa(deployment.Port)
	target := "http://" + targetHost

	// SECURITY (bug #215 follow-up): drop X-Internal-Auth-* headers before
	// forwarding to a customer-deployed app on localhost. The app has no
	// reason to see Orama's internal-auth metadata, and stripping prevents
	// any accidental leakage if the customer ever adds header-based trust.
	stripInboundInternalAuthHeaders(r.Header)
	// Set proxy headers
	r.Header.Set("X-Forwarded-For", getClientIP(r))
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", r.Host)

	// Handle WebSocket upgrade requests specially
	if isWebSocketUpgrade(r) {
		r.URL.Scheme = "http"
		r.URL.Host = targetHost
		r.Host = targetHost
		if g.proxyWebSocket(w, r, targetHost) {
			return
		}
		// WebSocket proxy failed - try cross-node replicas as fallback, unless
		// another node forwarded this request: it never goes on to a third.
		if g.replicaManager != nil && proxyNode == "" {
			if g.proxyCrossNodeWithReplicas(w, r, deployment) {
				return
			}
		}
		http.Error(w, "WebSocket connection failed", http.StatusServiceUnavailable)
		return
	}

	// Create a new request to the backend
	backendURL := proxyTargetURL(target, r.URL)

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, backendURL, r.Body)
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}
	keepBodyLength(proxyReq, r)

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Execute proxy request using shared transport
	httpClient := &http.Client{Timeout: 30 * time.Second, Transport: g.proxyTransport}
	resp, err := httpClient.Do(proxyReq)
	if err != nil {
		if r.Context().Err() != nil {
			return // The client left: not a gateway error, and nobody to answer.
		}
		g.logger.ComponentError(logging.ComponentGeneral, "local proxy request failed",
			zap.String("target", target),
			zap.String("error", httputil.FailureReason(err)),
		)

		// Local process is down — try other replica nodes before giving up.
		// A request another node forwarded here is not sent on: two replicas
		// with the app down would hand it back and forth. The unmarked 503
		// tells the forwarder to try its next replica, as for a stale list
		// above. A client that left has nobody to answer.
		if g.replicaManager != nil && proxyNode == "" && r.Context().Err() == nil {
			if g.proxyCrossNodeWithReplicas(w, r, deployment) {
				return
			}
		}

		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	// Copy response headers. The marker is the gateway's to set: one the app
	// sent (empty, to blank it, or a fake) is dropped on every path.
	copyProxiedHeaders(w, resp)
	// The app answered. The node that forwarded this request holds a 502, 503
	// or 504 against this node's circuit breaker, and what an app returns says
	// nothing about the node, so it is told whose answer this is. A client's own
	// request gets no marker.
	if proxyNode != "" {
		httputil.MarkTenantOrigin(w.Header())
	}

	// Write status code and body
	w.WriteHeader(resp.StatusCode)
	if _, err := w.(io.Writer).Write([]byte{}); err == nil {
		io.Copy(w, resp.Body)
	}
}

// staleReplicaLogInterval is how often one deployment's stale replica list is
// logged: every request forwarded under it hits the same condition.
const staleReplicaLogInterval = time.Minute

// warnStaleReplicaList logs that proxyNode forwarded a request for deployment
// to this node, which runs no active replica of it. The forwarder's replica
// list is out of date and every request it sends here ends the same way, so the
// line is a warning, once per interval per deployment, with the number held back.
func (g *Gateway) warnStaleReplicaList(deployment *deployments.Deployment, proxyNode string, replicaErr error) {
	ok, held := g.staleReplicaLog.allow(deployment.ID, time.Now(), staleReplicaLogInterval)
	if !ok {
		return
	}
	g.logger.Warn("Forwarded request for a deployment this node has no active replica of",
		zap.String("deployment", deployment.Name),
		zap.String("home_node", deployment.HomeNodeID),
		zap.String("proxy_node", proxyNode),
		zap.Int("held_back", held),
		zap.Error(replicaErr),
	)
}

// proxyCrossNode forwards a request to the home node of a deployment
// Returns true if the request was successfully forwarded, false otherwise
func (g *Gateway) proxyCrossNode(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment) bool {
	return g.proxyCrossNodeWithin(w, r, deployment, constants.GatewayProxyTimeout)
}

// proxyCrossNodeWithin is proxyCrossNode with the time the home node is given
// to answer. A route whose work on the home node outlasts GatewayProxyTimeout
// (an update waiting for its replicas) passes a longer one.
func (g *Gateway) proxyCrossNodeWithin(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment, timeout time.Duration) bool {
	// Get home node IP from dns_nodes table
	db := g.client.Database()
	internalCtx := client.WithInternalAuth(r.Context())

	query := "SELECT COALESCE(internal_ip, ip_address) FROM dns_nodes WHERE id = ? LIMIT 1"
	result, err := db.Query(internalCtx, query, deployment.HomeNodeID)
	if err != nil || result == nil || len(result.Rows) == 0 {
		g.logger.Warn("Failed to get home node IP",
			zap.String("home_node_id", deployment.HomeNodeID),
			zap.Error(err))
		return false
	}

	homeIP := getString(result.Rows[0][0])
	if homeIP == "" {
		g.logger.Warn("Home node IP is empty", zap.String("home_node_id", deployment.HomeNodeID))
		return false
	}

	g.logger.Info("Proxying request to home node",
		zap.String("deployment", deployment.Name),
		zap.String("home_node_id", deployment.HomeNodeID),
		zap.String("home_ip", homeIP),
		zap.String("current_node", g.nodePeerID),
	)

	// Proxy to the home node over the WireGuard overlay on the index gateway
	// port. This is node-to-node internal communication - no TLS needed.
	targetHost := net.JoinHostPort(homeIP, strconv.Itoa(constants.GatewayAPIPort))
	return g.forwardToHomeNode(w, r, deployment, homeIP, targetHost, timeout)
}

// forwardToHomeNode sends r to the gateway at targetHost, on the home node
// homeIP, and relays its answer: even a 502, 503 or 504, which is also held
// against this deployment's circuit breaker toward that node when the node
// itself produced it. Returns false only when the request was not sent or the
// node did not answer.
func (g *Gateway) forwardToHomeNode(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment,
	homeIP, targetHost string, timeout time.Duration) bool {
	// Handle WebSocket upgrade requests specially
	if isWebSocketUpgrade(r) {
		// SECURITY (bug #215 follow-up): drop X-Internal-Auth-* headers from
		// the inbound request before forwarding to another node. The target
		// node's authMiddleware gates internal-auth trust on source IP only;
		// without this strip, an external attacker could forge JWT-Sub here
		// and have the home node trust it after the cross-node hop.
		stripInboundInternalAuthHeaders(r.Header)
		r.Header.Set("X-Forwarded-For", getClientIP(r))
		r.Header.Set("X-Orama-Proxy-Node", g.nodePeerID)
		r.URL.Scheme = "http"
		r.URL.Host = targetHost
		// Keep original Host header for domain routing
		return g.proxyWebSocket(w, r, targetHost)
	}

	targetURL := proxyTargetURL("http://"+targetHost, r.URL)

	body, tracked := hopBody(r)
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, body)
	if err != nil {
		g.logger.Error("Failed to create cross-node proxy request", zap.String("error", httputil.FailureReason(err)))
		return false
	}
	keepBodyLength(proxyReq, r)

	// Copy headers and set Host header to original domain for routing
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	// SECURITY (bug #215 follow-up): drop any X-Internal-Auth-* headers the
	// inbound request may have carried. See WS branch above.
	stripInboundInternalAuthHeaders(proxyReq.Header)
	proxyReq.Host = r.Host // Keep original host for domain routing on target node
	proxyReq.Header.Set("X-Forwarded-For", getClientIP(r))
	proxyReq.Header.Set("X-Orama-Proxy-Node", g.nodePeerID) // Prevent loops

	// Circuit breaker: one per deployment and node, so one app failing on the
	// home node never refuses another's requests there.
	cb := g.circuitBreakers.ForDeployment(deployment.ID, deployment.Namespace, deployment.Name, homeIP)
	if !cb.Allow() {
		g.logger.Warn("Cross-node proxy skipped (circuit open)",
			zap.String("deployment", deployment.Name), zap.String("target_ip", homeIP))
		return false
	}

	// Internal node-to-node communication using shared transport
	httpClient := &http.Client{Timeout: timeout, Transport: g.proxyTransport}
	resp, err := httpClient.Do(proxyReq)
	if err != nil {
		recordHopError(cb, r, tracked, err)
		if r.Context().Err() != nil {
			return false // The client left: not a gateway error.
		}
		g.logger.Error("Cross-node proxy request failed",
			zap.String("target_ip", homeIP),
			zap.String("host", r.Host),
			zap.String("error", httputil.FailureReason(err)))
		return false
	}
	defer resp.Body.Close()

	if isUpstreamFailure(resp) {
		cb.RecordFailure(fmt.Sprintf("the node answered %d", resp.StatusCode))
	} else {
		cb.RecordSuccess()
	}

	copyProxiedHeaders(w, resp)

	// Write status code and body
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	return true
}

// proxyCrossNodeWithReplicas tries to proxy a request to any healthy replica node.
// It first tries the primary (home node), then falls back to other replicas.
// Returns true if the request was successfully proxied.
func (g *Gateway) proxyCrossNodeWithReplicas(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment) bool {
	if g.replicaManager == nil {
		// No replica manager — fall back to original single-node proxy
		return g.proxyCrossNode(w, r, deployment)
	}

	// Get all active replica nodes
	replicaNodes, err := g.replicaManager.GetActiveReplicaNodes(r.Context(), deployment.ID)
	if err != nil || len(replicaNodes) == 0 {
		// Fall back to original home node proxy
		return g.proxyCrossNode(w, r, deployment)
	}

	// Try each replica node (primary first if present)
	for _, nodeID := range replicaNodes {
		if nodeID == g.nodePeerID {
			continue // Skip self
		}
		if r.Context().Err() != nil {
			return false // The client left: no replica has anybody to answer.
		}

		nodeIP, err := g.replicaManager.GetNodeOverlayIP(r.Context(), nodeID)
		if err != nil {
			g.logger.Warn("Failed to get replica node IP",
				zap.String("node_id", nodeID),
				zap.Error(err),
			)
			continue
		}

		// Proxy using the same logic as proxyCrossNode
		proxyDeployment := *deployment
		proxyDeployment.HomeNodeID = nodeID
		if g.proxyCrossNodeToIP(w, r, &proxyDeployment, nodeIP) {
			return true
		}
	}

	return false
}

// proxyCrossNodeToIP forwards a request to a specific node IP.
// This is a variant of proxyCrossNode that takes a resolved IP directly.
func (g *Gateway) proxyCrossNodeToIP(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment, nodeIP string) bool {
	g.logger.Info("Proxying request to replica node",
		zap.String("deployment", deployment.Name),
		zap.String("node_id", deployment.HomeNodeID),
		zap.String("node_ip", nodeIP),
	)

	targetHost := net.JoinHostPort(nodeIP, strconv.Itoa(constants.GatewayAPIPort))
	return g.forwardToReplica(w, r, deployment, nodeIP, targetHost)
}

// forwardToReplica sends r to the gateway at targetHost, on the replica node
// nodeIP. Returns true when it relayed the node's answer, false when the
// request was not sent, the node did not answer, or its own proxy failed, so
// the caller may try the next replica.
func (g *Gateway) forwardToReplica(w http.ResponseWriter, r *http.Request, deployment *deployments.Deployment,
	nodeIP, targetHost string) bool {
	// Handle WebSocket upgrade requests specially
	if isWebSocketUpgrade(r) {
		// SECURITY (bug #215 follow-up): see proxyCrossNode for rationale.
		stripInboundInternalAuthHeaders(r.Header)
		r.Header.Set("X-Forwarded-For", getClientIP(r))
		r.Header.Set("X-Orama-Proxy-Node", g.nodePeerID)
		r.URL.Scheme = "http"
		r.URL.Host = targetHost
		return g.proxyWebSocket(w, r, targetHost)
	}

	targetURL := proxyTargetURL("http://"+targetHost, r.URL)

	body, tracked := hopBody(r)
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, body)
	if err != nil {
		g.logger.Error("Failed to create cross-node proxy request", zap.String("error", httputil.FailureReason(err)))
		return false
	}
	keepBodyLength(proxyReq, r)

	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	// SECURITY (bug #215 follow-up): see proxyCrossNode for rationale.
	stripInboundInternalAuthHeaders(proxyReq.Header)
	proxyReq.Host = r.Host
	proxyReq.Header.Set("X-Forwarded-For", getClientIP(r))
	proxyReq.Header.Set("X-Orama-Proxy-Node", g.nodePeerID)

	// Circuit breaker: skip this replica if it's been failing. One per
	// deployment and node, as in proxyCrossNodeWithin.
	cb := g.circuitBreakers.ForDeployment(deployment.ID, deployment.Namespace, deployment.Name, nodeIP)
	if !cb.Allow() {
		g.logger.Warn("Replica proxy skipped (circuit open)",
			zap.String("deployment", deployment.Name), zap.String("target_ip", nodeIP))
		return false
	}

	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: g.proxyTransport}
	resp, err := httpClient.Do(proxyReq)
	if err != nil {
		recordHopError(cb, r, tracked, err)
		if r.Context().Err() != nil {
			return false // The client left: not a gateway error.
		}
		g.logger.Warn("Replica proxy request failed",
			zap.String("target_ip", nodeIP),
			zap.String("error", httputil.FailureReason(err)),
		)
		return false
	}
	defer resp.Body.Close()

	// If the remote node's own proxy failed, try the next replica. What the
	// app answered is its answer, whatever the status.
	if isUpstreamFailure(resp) {
		cb.RecordFailure(fmt.Sprintf("the replica answered %d", resp.StatusCode))
		g.logger.Warn("Replica returned gateway error, trying next",
			zap.String("target_ip", nodeIP),
			zap.Int("status", resp.StatusCode),
		)
		return false
	}
	cb.RecordSuccess()

	copyProxiedHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	return true
}

// Helper functions for type conversion
func getString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func getInt(v interface{}) int {
	if i, ok := v.(int); ok {
		return i
	}
	if i, ok := v.(int64); ok {
		return int(i)
	}
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// keepBodyLength gives a forwarded request the length of the body it was
// received with. http.NewRequest knows the length only of a body it can see the
// size of, so a forwarded r.Body went out chunked, and the next gateway then
// read ContentLength as "no body": a device link's key was dropped that way and
// the answer was for a login that named no device.
func keepBodyLength(out, in *http.Request) {
	out.ContentLength = in.ContentLength
}

// namespaceProxyRequest is the request r becomes on its hop to the namespace
// gateway at targetHost, carrying body and the identity this gateway validated.
func (g *Gateway) namespaceProxyRequest(r *http.Request, targetHost string, body io.ReadCloser,
	validatedNamespace string, validatedClaims *auth.JWTClaims, validatedKey keyAuthority) (*http.Request, error) {
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, proxyTargetURL("http://"+targetHost, r.URL), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create the namespace gateway proxy request: %w", err)
	}
	keepBodyLength(proxyReq, r)

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	proxyReq.Header.Set("X-Forwarded-For", getClientIP(r))
	proxyReq.Header.Set("X-Forwarded-Proto", "https")
	proxyReq.Header.Set("X-Forwarded-Host", r.Host)
	proxyReq.Header.Set("X-Original-Host", r.Host)

	// SECURITY (bug #215 follow-up): drop any X-Internal-Auth-* headers
	// the header-copy loop above may have inherited from the inbound
	// request BEFORE writing the trusted values. The namespace gateway
	// gates on source IP only; without this strip, an external attacker
	// could forge X-Internal-Auth-JWT-Sub and impersonate any wallet.
	stripInboundInternalAuthHeaders(proxyReq.Header)
	// Set internal auth headers if auth was validated by main gateway
	// This allows the namespace gateway to trust the authentication
	if validatedNamespace != "" {
		proxyReq.Header.Set(HeaderInternalAuthValidated, "true")
		proxyReq.Header.Set(HeaderInternalAuthNamespace, validatedNamespace)
		// Bug #215: forward validated JWT subject + custom claims so the
		// namespace gateway's auth middleware can hydrate ctxKeyJWT and
		// host functions see a non-empty caller_jwt_subject.
		setInternalAuthJWTHeaders(proxyReq.Header, validatedClaims)
		setInternalAuthKeyHeaders(proxyReq.Header, validatedKey)
		// The MAC is what makes any of this believable on the other side.
		// Without it the namespace gateway would drop every header above, so a
		// hop that cannot be signed is refused rather than sent as an assertion
		// this gateway cannot back.
		//
		// The MAC covers the proxied request's own method and path, not the
		// inbound one, because that is what the other end will verify against.
		if err := signInternalAuthHeaders(g.internalAuthKey, proxyReq.Header, proxyReq.Method, proxyReq.URL.Path, time.Now()); err != nil {
			return nil, fmt.Errorf("%w: %w", errNoClusterSecret, err)
		}
	}

	return proxyReq, nil
}
