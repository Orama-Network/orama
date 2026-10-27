package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/gateway/routepolicy"
	"go.uber.org/zap"
)

// Device attestation at the gateway (feat-941): a namespace that attests
// admits to its app surface only callers whose session proved, through its
// device, that it is the genuine app (auth.AttestationPolicy).
//
// What is judged is the app surface: invoking a function (HTTP and
// WebSocket) and every data-plane route — storage, pub/sub, cache, push,
// WebRTC, the anonymity proxy. A bundled key or an end user's session reaches
// nothing else. Not judged, and documented as such:
//   - signing in and everything under /v1/auth/, and the attestation
//     endpoints themselves: a device has to sign in and attest before it can
//     have attested;
//   - the open routes (health, status, version, JWKS), which serve no
//     namespace;
//   - the control plane: deploying, managing keys, members and policies. It
//     needs a control-plane grant, which no app holds, and judging it would
//     lock out the operators who set the policy.
//
// Callers who are not an app's end users are not judged on the app surface
// either: a wallet that operates the namespace (owner, admin, developer), a
// deployed app's own workload token, and a key holding a control-plane grant.
//
// Judging a request costs a map lookup: the verdict is the token's own `att`
// claim and the policy is the gateway's in-memory copy. Only a caller the
// policy would refuse pays more — the cached grant read that tells whether
// it operates the namespace.

const (
	// attestationLogInterval is how often one namespace's monitor-mode
	// "would refuse" line is logged; every one is counted.
	attestationLogInterval = time.Minute

	// attestationPlatformNone labels a refused caller that carried no
	// attestation at all.
	attestationPlatformNone = "none"
)

// attestationCodes maps a refusal to its wire code.
var attestationCodes = map[auth.AttestationRefusal]string{
	auth.AttestationRequired:       CodeAttestationRequired,
	auth.AttestationInvalid:        CodeAttestationInvalid,
	auth.AttestationUpdateRequired: CodeAppUpdateRequired,
}

// attestationGated reports whether a route is on the app surface.
//
// A function socket opened with a capability is not judged here: it carries
// no credential, and the capability was judged when it was minted — only an
// attested invocation mints one in a namespace that enforces.
func attestationGated(r *http.Request, policy routepolicy.Policy) bool {
	if isCapabilityUpgrade(r) {
		return false
	}
	if isFunctionInvokePath(r.URL.Path) {
		return true
	}
	if policy.Operator || policy.Domain == "" {
		return false
	}
	return !auth.HoldsControlPlane(auth.PermissionSet{{
		Domain: auth.Domain(policy.Domain), Action: auth.Action(policy.Action),
	}})
}

// attestationMiddleware judges every request on the app surface against its
// namespace's attestation policy. It sits after trafficAttributionMiddleware
// and before authorizationMiddleware: the credential and its namespace are
// known, and nothing has been read on the caller's behalf yet.
func (g *Gateway) attestationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || g.authService == nil || !attestationGated(r, g.policyFor(r)) {
			next.ServeHTTP(w, r)
			return
		}
		// A hop the main gateway signed was judged, and counted, there; it is
		// judged again here, where the request is served, and not counted
		// twice.
		firstHand := r.Header.Get(HeaderInternalAuthValidated) != "true"
		r, ok := g.judgeAttestation(w, r, g.requestNamespace(r), firstHand)
		if ok {
			next.ServeHTTP(w, r)
		}
	})
}

// admitAttestedProxy judges a request the index gateway is about to forward to
// a namespace gateway, with the credential it validated for the proxy. The
// index gateway is where the cluster's request metrics are kept, so this is
// where a namespace's refusals are counted; refusing here also spares the
// namespace gateway the request. It reports whether the request may go on.
func (g *Gateway) admitAttestedProxy(w http.ResponseWriter, r *http.Request, namespace string, a namespaceProxyAuth) bool {
	if g.authService == nil || !attestationGated(r, g.policyFor(r)) {
		return true
	}
	ctx := context.WithValue(r.Context(), CtxKeyNamespaceOverride, namespace)
	if a.claims != nil {
		ctx = context.WithValue(ctx, ctxKeyJWT, a.claims)
	}
	if a.key.scopes != "" {
		ctx = withKeyAuthority(ctx, a.key.scopes, a.key.resources)
	}
	_, ok := g.judgeAttestation(w, r.WithContext(ctx), namespace, true)
	return ok
}

// judgeAttestation decides one request. It returns the request to serve —
// carrying, for a WebSocket upgrade, the namespace whose policy holds the
// socket from then on — and false when it answered the request itself.
func (g *Gateway) judgeAttestation(w http.ResponseWriter, r *http.Request, namespace string, count bool) (*http.Request, bool) {
	policy, err := g.authService.AttestationPolicyFor(namespace)
	if err != nil {
		g.logger.ComponentWarn("gateway", "refusing a request: its namespace's attestation policy cannot be read",
			zap.String("namespace", namespace), zap.Error(err))
		unavailable(w, CodeAuthUnavailable, "cannot read this namespace's attestation policy right now; retry shortly")
		return r, false
	}
	// A namespace that does not attest judges nothing, and holds no socket
	// to a policy: a socket opened while the mode was off stays open when it
	// is turned on. Monitor first, then enforce, holds every socket opened in
	// between.
	if policy.Mode == auth.AttestationOff {
		return r, true
	}
	ws := isWebSocketUpgrade(r)
	claims, _ := r.Context().Value(ctxKeyJWT).(*auth.JWTClaims)
	var att *auth.AttestationClaim
	if claims != nil {
		att = claims.Att
	}
	judgement := auth.JudgeAttestation(policy, att, time.Now())
	refusal, message := judgement.Refusal, judgement.Message

	// Whether the caller is one attestation judges at all is asked only of a
	// caller it would refuse — and of an unattested socket, which a policy
	// enforced later must still be able to close.
	judged := true
	if refusal != auth.AttestationAdmitted || (ws && att == nil) {
		exempt, err := g.attestationExempt(r)
		if err != nil {
			g.refuseUnreadableGrant(w, err)
			return r, false
		}
		if exempt {
			judged, refusal = false, auth.AttestationAdmitted
		}
	}
	if ws && judged {
		r = r.WithContext(context.WithValue(r.Context(), ctxkeys.AttestationNamespace, namespace))
	}
	if refusal == auth.AttestationAdmitted {
		return r, true
	}

	platform := attestationPlatformNone
	if att != nil {
		platform = att.Platform
	}
	if count && g.traffic != nil {
		g.traffic.ObserveAttestation(attestationCodes[refusal], platform, string(policy.Mode))
	}
	if policy.Mode == auth.AttestationMonitor {
		g.logWouldRefuse(namespace, refusal, platform, r.URL.Path)
		return r, true
	}
	forbidden(w, attestationCodes[refusal], message, map[string]any{"namespace": namespace})
	return r, false
}

// attestationExempt reports whether the caller is one attestation does not
// judge: a wallet operating the namespace, a deployed app's workload token, or
// a key holding a control-plane grant.
func (g *Gateway) attestationExempt(r *http.Request) (bool, error) {
	ctx := r.Context()
	claims, _ := ctx.Value(ctxKeyJWT).(*auth.JWTClaims)
	if claims != nil && auth.IsWorkloadSubject(claims.Sub) {
		return true, nil
	}
	if claims != nil && !isAPIKeySubject(claims.Sub) {
		grant, err := g.cachedRequestGrant(r, claims.Sub)
		if err != nil {
			return false, err
		}
		return grant != nil && auth.OperatesNamespace(grant.Role), nil
	}
	if scopes, ok := ctx.Value(ctxKeyScopes).(auth.ScopeSet); ok {
		return auth.HoldsControlPlane(auth.PermissionsFromScopes(scopes.Canonical())), nil
	}
	return false, nil
}

// logWouldRefuse logs, at most once a minute per namespace and reason, a
// request monitor mode let through that enforcing would refuse.
func (g *Gateway) logWouldRefuse(namespace string, refusal auth.AttestationRefusal, platform, path string) {
	key := strings.Join([]string{namespace, refusal.String(), platform}, "\x00")
	allowed, held := g.attestationLogs.allow(key, time.Now(), attestationLogInterval)
	if !allowed {
		return
	}
	g.logger.ComponentInfo("gateway", "attestation monitor: enforcing would refuse this request",
		zap.String("namespace", namespace), zap.String("code", attestationCodes[refusal]),
		zap.String("platform", platform), zap.String("path", path), zap.Int("held_back", held))
}

// attestationSocketGate is the socket sweeper's wssession.Gate: an open socket
// is judged the way its upgrade was, by the token it now holds and the
// namespace's policy as it is now. Monitor mode closes nothing.
type attestationSocketGate struct{ authService *auth.Service }

func (g attestationSocketGate) AttestationRefusal(namespace string, claims *auth.JWTClaims, now time.Time) (string, error) {
	policy, err := g.authService.AttestationPolicyFor(namespace)
	if err != nil || policy.Mode != auth.AttestationEnforce {
		return "", err
	}
	return attestationCodes[auth.JudgeAttestation(policy, claims.Att, now).Refusal], nil
}
