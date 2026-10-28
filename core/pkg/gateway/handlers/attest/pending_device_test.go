package attest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// realReissue answers attestations as fakeService does, and mints the token
// with the gateway's own auth service.
type realReissue struct {
	*fakeService
	auth *authsvc.Service
}

func (r realReissue) ReissueAttested(c *authsvc.JWTClaims, a *authsvc.AttestationClaim) (string, int64, error) {
	return r.auth.ReissueAttested(c, a)
}

// A pending device's session that attests (feat-973) is answered with a token
// that is still a pending device's: attestation does not admit the device.
func TestAttest_aPendingDevicesSessionStaysPending(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	auth, err := authsvc.NewService(nil, nil, string(keyPEM), "anchat", "test-hmac-secret")
	if err != nil {
		t.Fatal(err)
	}
	// The middleware's verified claims of a pending device's session.
	claims := &authsvc.JWTClaims{Sub: deviceSession().Sub, Namespace: "anchat", Aud: authsvc.AudiencePendingDevice, Did: device, Sid: "s"}
	h := NewHandlers(realReissue{fakeService: &fakeService{}, auth: auth}, nil, nil)
	b64 := base64.StdEncoding.EncodeToString
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		req     any
	}{
		"android": {h.AndroidHandler, AndroidRequest{Challenge: "c", Chain: []string{b64([]byte("leaf"))}}},
		"ios":     {h.IOSHandler, IOSRequest{Challenge: "c", KeyID: b64([]byte("key")), Attestation: b64([]byte("object"))}},
	} {
		rec, body := call(tc.handler, http.MethodPost, "/v1/attest/"+name, claims, tc.req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %v", name, rec.Code, body)
		}
		answered, _ := body["access_token"].(string)
		got := payloadOf(t, answered)
		if got.Aud != authsvc.AudiencePendingDevice || got.Did != device || got.Att == nil {
			t.Errorf("%s: a pending device's attested token is aud %q, did %q, att %v", name, got.Aud, got.Did, got.Att)
		}
	}
}

// payloadOf reads a token's claims. Its signature is the auth service's own,
// which the auth package's tests verify.
func payloadOf(t *testing.T, jwt string) authsvc.JWTClaims {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c authsvc.JWTClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
