package serverless

import (
	"context"
	"net/http"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/capability"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/serverless/persistent"
	"github.com/Orama-Network/orama/core/pkg/serverless/triggers"
	"github.com/Orama-Network/orama/core/pkg/serverless/wsbridge"
	"go.uber.org/zap"
)

// JWTVerifier is the subset of *auth.Service the serverless handlers
// need for mid-session token refresh on persistent WS (bugboard #321).
// Kept as an interface so tests can pass a fake without standing up
// the full auth service.
type JWTVerifier interface {
	ParseAndVerifyJWT(token string) (*auth.JWTClaims, error)
	// DeviceRevocationRecord is the revocation of a device whose token
	// ParseAndVerifyJWT refused for it, which an auth.refresh ack carries.
	DeviceRevocationRecord(ctx context.Context, namespace, id string) (*auth.DeviceRevocationRecord, error)
}

// ServerlessHandlers contains handlers for serverless function endpoints.
// It's a separate struct to keep the Gateway struct clean.
type ServerlessHandlers struct {
	// servedNamespace is the namespace a namespace gateway serves; "" on the
	// cluster gateway (SetServedNamespace).
	servedNamespace string
	// maxMemoryLimitMB and maxTimeoutSeconds are the ceilings a deploy is
	// checked against (SetFunctionLimits); they start at the serverless
	// defaults so a handler nobody configured still refuses an absurd limit.
	maxMemoryLimitMB  int
	maxTimeoutSeconds int
	maxRetryCount     int
	invoker           *serverless.Invoker
	engine            *serverless.Engine // for persistent WS instantiation
	registry          serverless.FunctionRegistry
	wsManager         *serverless.WSManager
	triggerStore      *triggers.PubSubTriggerStore
	cronStore         *triggers.CronTriggerStore // optional; nil = cron triggers unavailable
	dispatcher        *triggers.PubSubDispatcher
	persistentMgr     *persistent.Manager // optional; when nil persistent WS rejects 503
	wsBridge          *wsbridge.Bridge    // optional; nil = no client→ns registration
	secretsManager    serverless.SecretsManager
	jwtVerifier       JWTVerifier // optional; when nil, mid-session auth.refresh is disabled
	// sessions holds every token-authorized function WebSocket to its token's
	// expiry and revocation. It is the gateway's one registry, the one its
	// sweeper runs over.
	sessions *wssession.Registry
	// capabilities and capabilityRevocations check the capabilities a
	// function's WebSocket may be opened with (feat-264). Set once at gateway
	// start via SetCapabilities; nil refuses every capability.
	capabilities          *capability.Authority
	capabilityRevocations CapabilityRevocations
	capabilitySockets     *socketCounter
	audit                 *auth.AuditLog
	logger                *zap.Logger
}

// NewServerlessHandlers creates a new ServerlessHandlers instance.
//
// audit records the deploy, delete and secret events; a nil one drops them,
// which is the test case.
//
// sessions is the gateway's WebSocket session registry: every token-authorized
// socket these handlers open is registered there, and the gateway's sweeper
// closes the ones whose token expires or is revoked. It is required.
//
// engine, persistentMgr, and wsBridge may be nil — persistent-WS
// functions then return 503 on upgrade, and bridged WS clients can't
// be tracked (the host call returns "unknown client_id"). All other
// endpoints continue to work via the invoker.
func NewServerlessHandlers(
	invoker *serverless.Invoker,
	engine *serverless.Engine,
	registry serverless.FunctionRegistry,
	wsManager *serverless.WSManager,
	sessions *wssession.Registry,
	triggerStore *triggers.PubSubTriggerStore,
	cronStore *triggers.CronTriggerStore,
	dispatcher *triggers.PubSubDispatcher,
	persistentMgr *persistent.Manager,
	wsBridge *wsbridge.Bridge,
	secretsManager serverless.SecretsManager,
	audit *auth.AuditLog,
	logger *zap.Logger,
) *ServerlessHandlers {
	defaults := serverless.DefaultConfig()
	return &ServerlessHandlers{
		maxMemoryLimitMB:  defaults.MaxMemoryLimitMB,
		maxTimeoutSeconds: defaults.MaxTimeoutSeconds,
		maxRetryCount:     defaults.MaxRetryCount,
		invoker:           invoker,
		engine:            engine,
		registry:          registry,
		wsManager:         wsManager,
		triggerStore:      triggerStore,
		cronStore:         cronStore,
		dispatcher:        dispatcher,
		persistentMgr:     persistentMgr,
		wsBridge:          wsBridge,
		secretsManager:    secretsManager,
		sessions:          sessions,
		audit:             audit,
		logger:            logger,
	}
}

// SetJWTVerifier wires the JWT verifier used for mid-session auth
// refresh on persistent WS (bugboard #321 control frame). Optional —
// when not set, the persistent WS handler rejects auth.refresh frames
// with a "not supported on this gateway" ack and the client falls back
// to the legacy close+reconnect path.
//
// Done as a setter rather than a constructor arg to avoid breaking
// existing call sites that don't yet have an auth service handy. Set
// once at gateway init, after construction.
func (h *ServerlessHandlers) SetJWTVerifier(v JWTVerifier) {
	h.jwtVerifier = v
}

// HealthStatus returns the health status of the serverless engine.
func (h *ServerlessHandlers) HealthStatus() map[string]interface{} {
	stats := h.wsManager.GetStats()
	return map[string]interface{}{
		"status":      "ok",
		"connections": stats.ConnectionCount,
		"topics":      stats.TopicCount,
	}
}

// getCallerClaimsFromRequest returns the JWT custom claims for the caller,
// or nil if the request was not JWT-authenticated. The map is safe to share
// (read-only on the engine side); we copy to avoid retaining the JWT struct.
func (h *ServerlessHandlers) getCallerClaimsFromRequest(r *http.Request) map[string]string {
	v := r.Context().Value(ctxkeys.JWT)
	if v == nil {
		return nil
	}
	claims, ok := v.(*auth.JWTClaims)
	if !ok || claims == nil || len(claims.Custom) == 0 {
		return nil
	}
	out := make(map[string]string, len(claims.Custom))
	for k, val := range claims.Custom {
		out[k] = val
	}
	return out
}

// getJWTSubjectFromRequest returns the Bearer JWT's `sub` claim if present,
// independent of the API-key-vs-JWT precedence used for general wallet
// resolution. Returns "" when the request was not JWT-authenticated.
//
// This is the source of truth for `oh.GetCallerJWTSubject()` inside WASM
// — bug #215. Functions that must bind on the JWT-signed identity (e.g.
// signup paths verifying the registering wallet matches the auth-challenge
// signer) read this instead of GetCallerWallet, which returns the namespace
// pseudo-identifier when the API key is the resolved auth.
func (h *ServerlessHandlers) getJWTSubjectFromRequest(r *http.Request) string {
	v := r.Context().Value(ctxkeys.JWT)
	if v == nil {
		return ""
	}
	claims, ok := v.(*auth.JWTClaims)
	if !ok || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

// getDeviceIDFromRequest returns the device the caller's session is bound to —
// the verified token's `did` — or "" when there is none. This is the source of
// truth for `get_caller_device_id` inside WASM, as getJWTSubjectFromRequest is
// for the account.
func (h *ServerlessHandlers) getDeviceIDFromRequest(r *http.Request) string {
	claims, _ := r.Context().Value(ctxkeys.JWT).(*auth.JWTClaims)
	if claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.Did)
}

// getDeviceStateFromRequest returns the state of the device the caller's
// session is bound to, as its token says: "pending" for a pending device's
// restricted session (feat-973), "active", or "" for none. It is the source of
// truth for `get_caller_device_state` inside WASM, and what the invoker
// refuses a pending session every function not open to it by.
func (h *ServerlessHandlers) getDeviceStateFromRequest(r *http.Request) string {
	claims, _ := r.Context().Value(ctxkeys.JWT).(*auth.JWTClaims)
	if claims == nil {
		return ""
	}
	return claims.DeviceState()
}

// getAttestationFromRequest returns what the caller's device attestation
// proved — the verified token's `att` claim — or nil. It is the source of
// truth for `get_caller_attestation` inside WASM.
func (h *ServerlessHandlers) getAttestationFromRequest(r *http.Request) *serverless.CallerAttestation {
	claims, _ := r.Context().Value(ctxkeys.JWT).(*auth.JWTClaims)
	if claims == nil {
		return nil
	}
	return callerAttestation(claims.Att)
}

// getJWTClaimsFromRequest returns the verified token claims the request was
// authorized with, or nil for an API key or no credential. A WebSocket is
// registered with these, so it is held to the token's expiry and revocation
// for as long as it is open.
func (h *ServerlessHandlers) getJWTClaimsFromRequest(r *http.Request) *auth.JWTClaims {
	claims, _ := r.Context().Value(ctxkeys.JWT).(*auth.JWTClaims)
	return claims
}

// getCallerHasInvokeFromRequest reports whether the caller may invoke a
// private function (bugboard #259). HTTP /invoke is a public path, so
// scopeMiddleware never runs; this is the grant check.
//
// True for: admin, an API key (or exchanged JWT) that holds ScopeInvoke,
// or a SIWE wallet JWT (non-ak_ subject). Storage-only API keys are false.
func (h *ServerlessHandlers) getCallerHasInvokeFromRequest(r *http.Request) bool {
	if h.getCallerIsAdminFromRequest(r) {
		return true
	}
	ctx := r.Context()
	if v := ctx.Value(ctxkeys.Scopes); v != nil {
		if s, ok := v.(auth.ScopeSet); ok && s.Has(auth.ScopeInvoke) {
			return true
		}
	}
	if v := ctx.Value(ctxkeys.JWT); v != nil {
		if claims, ok := v.(*auth.JWTClaims); ok && claims != nil {
			sub := strings.TrimSpace(claims.Sub)
			// A deployed app holds what its grant says now. Its token carries
			// the scopes of the grant at the moment its unit started, and an
			// app is started before its owner can grant it anything, so the
			// claim is the one answer that is wrong for as long as the app
			// has not renewed (docs/whitepaper/technical-reference/vol1/14-authorization.md, "A workload's identity").
			if auth.IsWorkloadSubject(sub) {
				grant, _ := ctx.Value(ctxkeys.Grant).(*auth.Grant)
				return grant != nil && grant.Scopes().Has(auth.ScopeInvoke)
			}
			if auth.IsAPIKeySubject(sub) {
				if claims.Custom != nil {
					if raw := strings.TrimSpace(claims.Custom["scopes"]); raw != "" && auth.ParseScopes(raw).Has(auth.ScopeInvoke) {
						return true
					}
				}
				return false
			}
			if sub != "" {
				return true
			}
		}
	}
	return false
}

// getCallerIsAdminFromRequest reports whether the request holds the admin
// grant used to invoke `internal: true` functions (bugboard #152).
//
// A pending device's restricted session (feat-973) is never an admin, whatever
// its account's grant: admin passes over `internal` and `invocable_by`, and a
// pending session runs only what the namespace opened to pending devices.
func (h *ServerlessHandlers) getCallerIsAdminFromRequest(r *http.Request) bool {
	ctx := r.Context()
	if claims, _ := ctx.Value(ctxkeys.JWT).(*auth.JWTClaims); claims.PendingDevice() {
		return false
	}
	if v := ctx.Value(ctxkeys.Scopes); v != nil {
		if s, ok := v.(auth.ScopeSet); ok && s.IsAdmin() {
			return true
		}
	}
	// An API-key-exchanged JWT (from /v1/auth/token) carries the key's scopes in
	// a custom claim — the JWT-bearer auth path sets ctxkeys.JWT but NOT
	// ctxkeys.Scopes — so an admin key used via exchange would otherwise be
	// wrongly denied. Mirror Gateway.callerScopes: trust custom["scopes"] ONLY
	// for an ak_ subject; a SIWE wallet JWT must never self-assert admin here.
	// A workload is not a key: its token's scopes are the grant of the moment
	// its unit started, so its admin answer is the grant resolved below, which
	// a narrowed or revoked grant changes at once rather than at renewal.
	if v := ctx.Value(ctxkeys.JWT); v != nil {
		if claims, ok := v.(*auth.JWTClaims); ok && claims != nil {
			sub := strings.TrimSpace(claims.Sub)
			if auth.IsAPIKeySubject(sub) && !auth.IsWorkloadSubject(sub) && claims.Custom != nil {
				if raw := strings.TrimSpace(claims.Custom["scopes"]); raw != "" && auth.ParseScopes(raw).IsAdmin() {
					return true
				}
			}
		}
	}
	// The grant the authorization middleware resolved for this namespace. It
	// used to be a boolean meaning "owner confirmed", so every member of a
	// namespace was an admin here; a runtime or reader member is not.
	if grant, _ := ctx.Value(ctxkeys.Grant).(*auth.Grant); grant != nil {
		return grant.Scopes().IsAdmin()
	}
	return false
}

// getWalletFromRequest resolves the caller identity for an invoke from
// VERIFIED sources only: the JWT subject (a wallet), else the API-key-derived
// namespace. It deliberately does NOT trust any client-supplied identity header
// (bugboard #152): the invoke paths are public, so an unauthenticated caller
// could otherwise set a header and impersonate any wallet — including the
// namespace owner — defeating in-function admin gates.
func (h *ServerlessHandlers) getWalletFromRequest(r *http.Request) string {
	// Identity comes only from a VERIFIED JWT subject, else the API-key-derived
	// namespace. A client-supplied X-Wallet header is NOT trusted (bugboard
	// #152) — it let an unauthenticated caller impersonate any wallet on the
	// public invoke paths.
	if v := r.Context().Value(ctxkeys.JWT); v != nil {
		if claims, ok := v.(*auth.JWTClaims); ok && claims != nil {
			subj := strings.TrimSpace(claims.Sub)
			// Ensure it's not an API key (standard Orama logic)
			if auth.IsWalletSubject(subj) {
				return subj
			}
		}
	}

	// 3. Fallback to API key identity (namespace)
	if v := r.Context().Value(ctxkeys.NamespaceOverride); v != nil {
		if ns, ok := v.(string); ok && ns != "" {
			return ns
		}
	}

	return ""
}

// getCallerKeyFromRequest names the credential of a caller with no wallet, for
// the invoke rate limiter (bug-934): the fingerprint of the API key the auth
// middleware looked up (or the main gateway forwarded), else of the subject of
// the key's or workload's token. "" for a wallet, whose bucket is its own, and
// for a request with no credential, which is bucketed by address.
//
// It exists because getWalletFromRequest answers the namespace for every such
// caller, so every user of an app's shared key drew on one bucket.
func (h *ServerlessHandlers) getCallerKeyFromRequest(r *http.Request) string {
	ctx := r.Context()
	claims, _ := ctx.Value(ctxkeys.JWT).(*auth.JWTClaims)
	if claims != nil && auth.IsWalletSubject(claims.Sub) {
		return ""
	}
	if fp, _ := ctx.Value(ctxkeys.KeyFingerprint).(string); fp != "" {
		return fp
	}
	if claims != nil {
		return auth.KeyFingerprint(claims.Sub)
	}
	return ""
}

// recordAudit records one control-plane event for this namespace: who deployed,
// deleted, or changed a secret, and when. Invokes are not recorded — they are
// the data plane, and one row per call would fill a replicated table.
func (h *ServerlessHandlers) recordAudit(r *http.Request, namespace, action, resource string) {
	h.audit.RecordFromRequest(r.Context(), r, auth.AuditEvent{
		Namespace: namespace,
		Actor:     auth.ActorFromRequest(r),
		Action:    action,
		Resource:  resource,
		Result:    auth.AuditSuccess,
	})
}
