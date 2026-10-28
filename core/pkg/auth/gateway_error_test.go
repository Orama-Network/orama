package auth

import (
	"net/http"
	"strings"
	"testing"
)

// A refusal has to say what to do about it. The gateway's sentence describes
// what it decided — "insufficient scope" — and leaves the reader to work out
// that the fix is a key minted with a different --scope.
func TestGatewayError_saysWhatToDoNext(t *testing.T) {
	for _, tc := range []struct {
		code     string
		body     string
		contains string
	}{
		{CodeAuthMissing, `{"code":"AUTH_MISSING","error":"missing credential"}`, "orama auth login"},
		{CodeAuthRevoked, `{"code":"AUTH_REVOKED","error":"revoked"}`, "revoked"},
		{CodeAuthExpired, `{"code":"AUTH_EXPIRED","error":"expired"}`, "orama auth login"},
		{CodeScopeMissing, `{"code":"INSUFFICIENT_SCOPE","error":"insufficient scope: storage"}`,
			"--scope storage"},
		{CodeUserJWTRequired, `{"code":"USER_JWT_REQUIRED","error":"user jwt required"}`, "signed-in wallet"},
		{CodeOwnershipRequired, `{"code":"OWNERSHIP_REQUIRED","error":"not the owner"}`, "orama auth whoami"},
		{CodeOperatorRequired, `{"code":"NOT_AN_OPERATOR","error":"nope"}`, "operator list"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := GatewayErrorFrom(http.StatusForbidden, []byte(tc.body))
			if err.Code != tc.code {
				t.Fatalf("code = %q, want %q", err.Code, tc.code)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("the message does not contain %q: %s", tc.contains, err.Error())
			}
		})
	}
}

// OWNERSHIP_REQUIRED is both "this credential is not a member" and "only the
// owner may do this". The gateway's sentence is the part that says which, so it
// is kept, and the advice never points at a command the caller could not run
// either (`orama members list` needs the authority it was just refused).
func TestGatewayError_ownershipKeepsTheGatewaysReason(t *testing.T) {
	for _, reason := range []string{
		"this credential holds no grant in this namespace",
		"only the namespace's owner may transfer it; an admin grant is not enough",
	} {
		err := GatewayErrorFrom(http.StatusForbidden, []byte(`{"code":"OWNERSHIP_REQUIRED","error":"`+reason+`"}`))
		message := err.Error()
		if !strings.Contains(message, reason) {
			t.Errorf("the gateway's reason is lost: %s", message)
		}
		if strings.Contains(message, "members list") {
			t.Errorf("the advice points at a command the caller cannot run: %s", message)
		}
	}
}

// A namespace mismatch is never fixed by asking for more grants, so the message
// has to name both namespaces rather than talk about permissions.
func TestGatewayError_namesBothNamespacesOnAMismatch(t *testing.T) {
	err := GatewayErrorFrom(http.StatusForbidden, []byte(
		`{"code":"NAMESPACE_MISMATCH","error":"wrong namespace","namespace":"anchat","credential_namespace":"rootwallet"}`))

	message := err.Error()
	if !strings.Contains(message, "anchat") || !strings.Contains(message, "rootwallet") {
		t.Errorf("the message names neither namespace: %s", message)
	}
}

// A code this binary has never heard of has to fall through to whatever the
// gateway said, because the CLI talks to gateways it was not built alongside.
func TestGatewayError_passesThroughACodeItDoesNotKnow(t *testing.T) {
	err := GatewayErrorFrom(http.StatusForbidden, []byte(`{"code":"SOMETHING_NEWER","error":"a newer gateway said this"}`))

	if err.Error() != "a newer gateway said this" {
		t.Errorf("message = %q, want the gateway's own", err.Error())
	}
}

// A body that is not the gateway's error shape is something in front of the
// gateway — a proxy, a load balancer — and reporting it as a refusal sends
// people to the wrong place.
func TestGatewayError_readsABodyThatIsNotItsOwnShape(t *testing.T) {
	err := GatewayErrorFrom(http.StatusBadGateway, []byte("<html>502 Bad Gateway</html>"))
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("the status is not in the message: %s", err.Error())
	}

	empty := GatewayErrorFrom(http.StatusBadGateway, nil)
	if !strings.Contains(empty.Error(), "502") {
		t.Errorf("an empty body lost the status: %s", empty.Error())
	}

	long := GatewayErrorFrom(http.StatusBadGateway, []byte(strings.Repeat("x", 5000)))
	if len(long.Error()) > 400 {
		t.Errorf("a 5KB body was pasted into the error whole (%d chars)", len(long.Error()))
	}
}

func TestGatewayError_isUnauthorized(t *testing.T) {
	for body, want := range map[string]bool{
		`{"code":"AUTH_REVOKED"}`:       true,
		`{"code":"AUTH_EXPIRED"}`:       true,
		`{"code":"AUTH_MISSING"}`:       true,
		`{"code":"INSUFFICIENT_SCOPE"}`: false,
		`{"code":"OWNERSHIP_REQUIRED"}`: false,
	} {
		if got := GatewayErrorFrom(http.StatusForbidden, []byte(body)).IsUnauthorized(); got != want {
			t.Errorf("%s: IsUnauthorized = %v, want %v", body, got, want)
		}
	}

	// A 401 with no code at all is still about the credential.
	if !GatewayErrorFrom(http.StatusUnauthorized, []byte(`{"error":"nope"}`)).IsUnauthorized() {
		t.Error("a bare 401 was not read as an authentication failure")
	}
}

// The grant name is pulled out of a sentence, and a sentence that is not one
// must not be read as a grant called "this credential lacks storage".
func TestScopeFromMessage(t *testing.T) {
	for message, want := range map[string]string{
		"insufficient scope: storage":             "storage",
		"insufficient scope: db:table=posts:read": "db:table=posts:read",
		"insufficient scope":                      "",
		"forbidden: you need more than this":      "",
		"":                                        "",
	} {
		if got := scopeFromMessage(message); got != want {
			t.Errorf("scopeFromMessage(%q) = %q, want %q", message, got, want)
		}
	}
}

// The rate limiter and most of the gateway answer in the nested shape; it was
// read as an unparseable body and the retry advice was lost.
func TestGatewayErrorFrom_nestedShapeKeepsCodeAndRetry(t *testing.T) {
	e := GatewayErrorFrom(http.StatusTooManyRequests, []byte(
		`{"ok":false,"error":{"code":"RATE_LIMITED","message":"too many authentication attempts","retryable":true,"retry_after":60}}`))
	if e.Code != "RATE_LIMITED" || e.Message != "too many authentication attempts" || !e.Retryable || e.RetryAfter != 60 {
		t.Fatalf("parsed %+v", e)
	}
	if !e.IsRetryable() || e.IsUnauthorized() {
		t.Errorf("a rate limit is retryable and not about the credential: %+v", e)
	}
}

func TestGatewayError_isRetryable(t *testing.T) {
	cases := map[string]struct {
		e    *GatewayError
		want bool
	}{
		"429":           {&GatewayError{Status: http.StatusTooManyRequests}, true},
		"503":           {&GatewayError{Status: http.StatusServiceUnavailable}, true},
		"flagged 500":   {&GatewayError{Status: http.StatusInternalServerError, Retryable: true}, true},
		"401":           {&GatewayError{Status: http.StatusUnauthorized, Code: CodeAuthInvalidKey}, false},
		"403 signature": {&GatewayError{Status: http.StatusForbidden, Code: "AUTH_SIGNATURE_INVALID"}, false},
		"nil":           {nil, false},
	}
	for name, c := range cases {
		if got := c.e.IsRetryable(); got != c.want {
			t.Errorf("%s: IsRetryable = %v, want %v", name, got, c.want)
		}
	}
}

// The flat auth-refusal shape still parses as before.
func TestGatewayErrorFrom_flatShapeUnchanged(t *testing.T) {
	e := GatewayErrorFrom(http.StatusUnauthorized, []byte(`{"error":"no credential was presented","code":"AUTH_MISSING"}`))
	if e.Code != CodeAuthMissing || e.Message != "no credential was presented" || e.Retryable {
		t.Fatalf("parsed %+v", e)
	}
}
