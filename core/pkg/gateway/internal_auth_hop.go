package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/secrets"
	"go.uber.org/zap"
)

// The main gateway validates a request and forwards the result to a namespace
// gateway in the X-Internal-Auth-* headers: the namespace it resolved, the JWT
// subject it verified, and the grant set of the API key it looked up. The
// namespace gateway believes all three without re-checking anything.
//
// Whether to believe them was answered by the source IP, and the source IP of
// every public request is 127.0.0.1. Caddy terminates TLS and reverse-proxies
// to localhost, and it forwards client headers verbatim, so anyone on the
// internet could send
//
//	X-Internal-Auth-Validated: true
//	X-Internal-Auth-Namespace: <any namespace>
//	X-Internal-Auth-Scopes: admin
//
// and be an admin of any namespace on any gateway, holding no credential at
// all. The ownership gate skipped its checks for the same reason.
//
// The headers now carry a MAC over exactly what they assert, keyed by a value
// derived from the cluster secret, and the source IP is not consulted at all.
// A caller who does not hold the cluster secret cannot produce one, and a MAC
// captured from a harmless request cannot be replayed onto a different method,
// path, namespace, subject or grant set.
//
// The MAC comes in versions, each covering what the one before did and more. v1
// covers the fields above. v2 adds the verified token's exp, iat and jti, which
// a namespace gateway needs to hold an open WebSocket to the token that opened
// it: without them a socket proxied from the main gateway had no expiry, and no
// revocation could reach it. v3 adds the device and session the token is bound
// to. v4 adds the resources an API key is narrowed to, without which a key
// limited to some functions reached every function through a namespace
// gateway. v5 adds the key's fingerprint, without which the namespace
// gateway's invoke rate limiter could not tell one key's callers from another's
// and bucketed every key-only caller of a namespace together. v6 adds the key's
// registry id, without which the namespace gateway's request log could not say
// which key made a request. v7 adds what the token's device attestation proved
// (its `att` claim), without which a namespace gateway could not judge the
// request against its namespace's attestation policy, nor tell a function who
// called (get_caller_attestation). v8 adds the subject of a pending device's
// restricted session (feat-973), which travels in a field of its own instead
// of the v1 subject: a namespace gateway that predates pending devices reads
// such a hop as one with no caller, and runs for it only what it would run for
// anybody, never what the account may.
//
// A signer stamps every version, so a namespace gateway that predates the
// newest still accepts what an upgraded main gateway sends it. A verifier
// judges a request by the newest MAC it carries, with no second chance under an
// older one, and deletes whatever that version does not cover before anything
// reads it — so a hop from a main gateway that predates v2 or v3, which exists
// only during a rolling upgrade, is believed exactly as far as it was signed.
// hopTokenTimes gives a hop with no token times the most time a token could
// have left, so a socket opened through it still ends.
//
// Accepting an older MAC gives nobody anything they did not have. Stripping the
// newer MAC off a genuine hop takes a position inside the WireGuard mesh, which
// is a node, and every node holds the cluster secret every version is keyed
// from — so whoever could downgrade a hop could sign one outright.
const (
	// HeaderInternalAuthMAC authenticates the v1 fields. Its value is
	// "<unix seconds>.<hex hmac>".
	HeaderInternalAuthMAC = "X-Internal-Auth-MAC"

	// HeaderInternalAuthMACv2 to HeaderInternalAuthMACv8 authenticate the v2
	// to v8 fields, in the same format.
	HeaderInternalAuthMACv2 = "X-Internal-Auth-MAC-V2"
	HeaderInternalAuthMACv3 = "X-Internal-Auth-MAC-V3"
	HeaderInternalAuthMACv4 = "X-Internal-Auth-MAC-V4"
	HeaderInternalAuthMACv5 = "X-Internal-Auth-MAC-V5"
	HeaderInternalAuthMACv6 = "X-Internal-Auth-MAC-V6"
	HeaderInternalAuthMACv7 = "X-Internal-Auth-MAC-V7"
	HeaderInternalAuthMACv8 = "X-Internal-Auth-MAC-V8"

	// HeaderInternalAuthJWTExp and HeaderInternalAuthJWTIat carry the verified
	// token's exp and iat (unix seconds), HeaderInternalAuthJWTJti its jti.
	// Covered by the v2 MAC only.
	HeaderInternalAuthJWTExp = "X-Internal-Auth-JWT-Exp"
	HeaderInternalAuthJWTIat = "X-Internal-Auth-JWT-Iat"
	HeaderInternalAuthJWTJti = "X-Internal-Auth-JWT-Jti"

	// HeaderInternalAuthJWTDid and HeaderInternalAuthJWTSid carry the device
	// and session a verified token is bound to. Covered by the v3 MAC only.
	HeaderInternalAuthJWTDid = "X-Internal-Auth-JWT-Did"
	HeaderInternalAuthJWTSid = "X-Internal-Auth-JWT-Sid"

	// HeaderInternalAuthKeyResources carries the resources an API-key caller
	// is narrowed to (auth.KeyResources, stored form), beside the scopes they
	// narrow. Covered by the v4 MAC only.
	HeaderInternalAuthKeyResources = "X-Internal-Auth-Key-Resources"

	// HeaderInternalAuthKeyFingerprint carries auth.KeyFingerprint of an
	// API-key caller's stored key, which the invoke rate limiter buckets the
	// caller by. Covered by the v5 MAC only.
	HeaderInternalAuthKeyFingerprint = "X-Internal-Auth-Key-Fingerprint"

	// HeaderInternalAuthKeyID carries the registry id (api_keys.id) of an
	// API-key caller's key, which request_logs.api_key_id records. Covered by
	// the v6 MAC only.
	HeaderInternalAuthKeyID = "X-Internal-Auth-Key-ID"

	// HeaderInternalAuthJWTAtt carries a verified token's attestation
	// (auth.AttestationClaim) as base64(JSON). Covered by the v7 MAC only.
	HeaderInternalAuthJWTAtt = "X-Internal-Auth-JWT-Att"

	// HeaderInternalAuthJWTPendingSub carries the subject of a verified token
	// that is a pending device's restricted session, in place of
	// HeaderInternalAuthJWTSub, which such a hop leaves empty. Covered by the
	// v8 MAC only.
	HeaderInternalAuthJWTPendingSub = "X-Internal-Auth-JWT-Pending-Sub"

	// internalAuthKeyPurpose is the HKDF domain separator for the hop key, so
	// it is unrelated to the TURN, push and secrets keys derived from the same
	// cluster secret.
	internalAuthKeyPurpose = "internal-auth-hop"

	// internalAuthMaxSkew bounds replay. A proxy hop is a single request over
	// the WireGuard mesh, so a minute is generous; it exists to tolerate clock
	// drift between nodes, not network delay.
	internalAuthMaxSkew = 60 * time.Second

	// The version prefixes of the payloads, so one cannot be confused with
	// another.
	internalAuthPayloadV1 = "orama-internal-auth-v1"
	internalAuthPayloadV2 = "orama-internal-auth-v2"
	internalAuthPayloadV3 = "orama-internal-auth-v3"
	internalAuthPayloadV4 = "orama-internal-auth-v4"
	internalAuthPayloadV5 = "orama-internal-auth-v5"
	internalAuthPayloadV6 = "orama-internal-auth-v6"
	internalAuthPayloadV7 = "orama-internal-auth-v7"
	internalAuthPayloadV8 = "orama-internal-auth-v8"
)

// hopVersion is one version of the hop MAC: its payload prefix, the header it
// travels in, and the fields it adds to the version before it.
type hopVersion struct {
	payload string
	header  string
	adds    []string
}

// hopVersions are every version, oldest first. A version covers its own
// fields and every older version's; a payload lists them in this order.
var hopVersions = []hopVersion{
	{internalAuthPayloadV1, HeaderInternalAuthMAC, nil},
	{internalAuthPayloadV2, HeaderInternalAuthMACv2,
		[]string{HeaderInternalAuthJWTExp, HeaderInternalAuthJWTIat, HeaderInternalAuthJWTJti}},
	{internalAuthPayloadV3, HeaderInternalAuthMACv3,
		[]string{HeaderInternalAuthJWTDid, HeaderInternalAuthJWTSid}},
	{internalAuthPayloadV4, HeaderInternalAuthMACv4,
		[]string{HeaderInternalAuthKeyResources}},
	{internalAuthPayloadV5, HeaderInternalAuthMACv5,
		[]string{HeaderInternalAuthKeyFingerprint}},
	{internalAuthPayloadV6, HeaderInternalAuthMACv6,
		[]string{HeaderInternalAuthKeyID}},
	{internalAuthPayloadV7, HeaderInternalAuthMACv7,
		[]string{HeaderInternalAuthJWTAtt}},
	{internalAuthPayloadV8, HeaderInternalAuthMACv8,
		[]string{HeaderInternalAuthJWTPendingSub}},
}

// internalAuthVersionedHeaders are the fields only a MAC newer than v1 covers.
var internalAuthVersionedHeaders = headersAddedAfter(0)

// headersAddedAfter lists the fields versions newer than hopVersions[v] add:
// what a hop verified at version v did not authenticate.
func headersAddedAfter(v int) []string {
	var out []string
	for _, version := range hopVersions[v+1:] {
		out = append(out, version.adds...)
	}
	return out
}

// internalAuthKey derives the key both ends of a proxy hop use.
//
// Every node in a cluster holds the same cluster secret, which is what makes
// this work across the mesh — and what makes it useless to anyone outside it.
func internalAuthKey(clusterSecret string) ([]byte, error) {
	return secrets.DeriveKey(clusterSecret, internalAuthKeyPurpose)
}

// internalAuthPayload is the exact string a MAC covers.
//
// Method and path are in it so a MAC observed on a GET cannot be replayed onto
// a DELETE, and every field the receiving gateway trusts is in it so none can
// be edited in flight.
func internalAuthPayload(v int, method, path string, h http.Header, ts int64) string {
	fields := []string{
		hopVersions[v].payload,
		strings.ToUpper(method),
		path,
		h.Get(HeaderInternalAuthNamespace),
		h.Get(HeaderInternalAuthJWTSub),
		h.Get(HeaderInternalAuthJWTCustom),
		h.Get(HeaderInternalAuthScopes),
	}
	for _, version := range hopVersions[1 : v+1] {
		for _, name := range version.adds {
			fields = append(fields, h.Get(name))
		}
	}
	return strings.Join(append(fields, strconv.FormatInt(ts, 10)), "\n")
}

func internalAuthMAC(key []byte, v int, method, path string, h http.Header, ts int64) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(internalAuthPayload(v, method, path, h, ts)))
	return mac.Sum(nil)
}

// signInternalAuthHeaders stamps every version's MAC over the internal-auth
// headers already set on h. Call it last, after every X-Internal-Auth-* value
// is final.
func signInternalAuthHeaders(key []byte, h http.Header, method, path string, now time.Time) error {
	if len(key) == 0 {
		return fmt.Errorf("no internal-auth key: this gateway has no cluster secret, " +
			"so it cannot prove to a namespace gateway that it validated the request")
	}

	ts := now.Unix()
	stamp := strconv.FormatInt(ts, 10) + "."
	for v, version := range hopVersions {
		h.Set(version.header, stamp+hex.EncodeToString(internalAuthMAC(key, v, method, path, h, ts)))
	}
	return nil
}

// verifyInternalAuthHeaders reports whether the internal-auth headers on this
// request were stamped by a gateway holding the cluster secret.
//
// A request is judged by the newest MAC it carries alone, with no second chance
// under an older one. It answers false for every reason: no key configured on
// this side, no MAC, a malformed MAC, a stale or future timestamp, or a MAC
// over different values than the headers now carry.
func verifyInternalAuthHeaders(key []byte, r *http.Request, now time.Time) bool {
	if len(key) == 0 {
		return false
	}
	v := newestHopVersion(r.Header)

	raw := strings.TrimSpace(r.Header.Get(hopVersions[v].header))
	stamp, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return false
	}
	// Both directions: a future timestamp is as much a sign of a forged or
	// replayed stamp as an old one.
	if skew := now.Sub(time.Unix(ts, 0)); skew > internalAuthMaxSkew || skew < -internalAuthMaxSkew {
		return false
	}
	presented, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(presented, internalAuthMAC(key, v, r.Method, r.URL.Path, r.Header, ts))
}

// newestHopVersion is the newest version whose MAC a request carries; v1 when
// it carries none, which then fails to verify.
func newestHopVersion(h http.Header) int {
	for v := len(hopVersions) - 1; v > 0; v-- {
		if strings.TrimSpace(h.Get(hopVersions[v].header)) != "" {
			return v
		}
	}
	return 0
}

// hopTokenTimes reads the forwarded token's exp and iat.
//
// A hop that carries no exp — one from a main gateway that predates v2, whose
// v2-only headers were deleted as unauthenticated — cannot say when its token
// expires. Its claims are given the most the token can have left,
// MaxTokenLifetime from now, rather than no expiry, so a socket opened through
// it still ends. Its iat is put one revocation-list staleness back: the main
// gateway applied the list when it verified the token, so only a revocation
// newer than that could have been missed, and putting it further back would
// have an older "log out everywhere" close the fresh session that followed it.
// An exp of zero or less is treated the same way: no token this cluster mints
// has one.
func hopTokenTimes(h http.Header, now time.Time) (exp, iat int64, err error) {
	raw := strings.TrimSpace(h.Get(HeaderInternalAuthJWTExp))
	if raw == "" {
		return now.Add(auth.MaxTokenLifetime).Unix(), now.Add(-auth.RevocationStaleness).Unix(), nil
	}
	exp, err = strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("the forwarded token expiry %q is not a unix time: %w", raw, err)
	}
	iatRaw := strings.TrimSpace(h.Get(HeaderInternalAuthJWTIat))
	iat, err = strconv.ParseInt(iatRaw, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("the forwarded token issue time %q is not a unix time: %w", iatRaw, err)
	}
	if exp <= 0 {
		exp = now.Add(auth.MaxTokenLifetime).Unix()
	}
	return exp, iat, nil
}

// internalAuthMiddleware is the first thing every request meets.
//
// Headers that arrived without a valid MAC are deleted before any other
// middleware can read them, so nothing downstream has to remember to ask
// whether they were authentic. A request that forges them is not rejected —
// it is simply treated as what it is, a request with no internal-auth headers,
// and goes on to authenticate normally or be refused for having no credential.
func (g *Gateway) internalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if verifyInternalAuthHeaders(g.internalAuthKey, r, time.Now()) {
			// A hop from an older gateway authenticated only its version's
			// fields; anything newer it carries is not read.
			for _, name := range headersAddedAfter(newestHopVersion(r.Header)) {
				r.Header.Del(name)
			}
		} else {
			if r.Header.Get(HeaderInternalAuthValidated) != "" {
				g.logger.ComponentWarn("gateway", "dropped unauthenticated internal-auth headers",
					zap.String("path", r.URL.Path),
					zap.String("remote", remoteAddrIP(r)))
			}
			stripInboundInternalAuthHeaders(r.Header)
		}
		// The MACs have served their purpose and must not travel further: a
		// deployed app or an outbound proxy target has no business seeing them.
		for _, version := range hopVersions {
			r.Header.Del(version.header)
		}
		next.ServeHTTP(w, r)
	})
}
