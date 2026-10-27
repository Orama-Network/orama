package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// A device as a client holds it: its public JWK and a way to sign.
func signingDevice(t *testing.T) (jwk json.RawMessage, id string, sign func(string) string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	raw := json.RawMessage(`{"kty":"OKP","crv":"Ed25519","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`)
	key, err := authsvc.ParseDeviceKey(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return raw, key.ID(), func(message string) string {
		return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(message)))
	}
}

// Property 1: the device is not what the client says it is. The message the
// wallet signed names it, and the key presented must be that device and must
// have signed the same bytes.
func TestProvenDeviceKey_theWalletAndTheDeviceSignedForTheSameDevice(t *testing.T) {
	jwk, id, sign := signingDevice(t)
	message := "the sign-in message, naming urn:orama:device:" + id

	key, err := provenDeviceKey(id, VerifyRequest{Message: message, DeviceKey: jwk, DeviceSignature: sign(message)})
	if err != nil || key.ID() != id {
		t.Fatalf("a device that signed for itself was refused: %v", err)
	}

	otherJWK, otherID, otherSign := signingDevice(t)
	for name, tc := range map[string]struct {
		named string
		req   VerifyRequest
		want  error
	}{
		"a key the wallet did not sign for": {id, VerifyRequest{Message: message, DeviceKey: otherJWK, DeviceSignature: otherSign(message)}, authsvc.ErrDeviceKeyInvalid},
		"a message naming no device":        {"", VerifyRequest{Message: message, DeviceKey: jwk, DeviceSignature: sign(message)}, authsvc.ErrDeviceKeyInvalid},
		"a device named but no key":         {otherID, VerifyRequest{Message: message}, authsvc.ErrDeviceKeyInvalid},
		"a signature over other bytes":      {id, VerifyRequest{Message: message, DeviceKey: jwk, DeviceSignature: sign("something else")}, authsvc.ErrDeviceSignatureInvalid},
		"no signature":                      {id, VerifyRequest{Message: message, DeviceKey: jwk}, authsvc.ErrDeviceSignatureInvalid},
	} {
		if _, err := provenDeviceKey(tc.named, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestWriteDeviceRefusal_saysWhichDeviceProblemItIs(t *testing.T) {
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		authsvc.ErrDeviceRequired:         {http.StatusForbidden, ErrCodeDeviceRequired},
		authsvc.ErrDeviceRevoked:          {http.StatusForbidden, ErrCodeDeviceRevoked},
		authsvc.ErrDevicePending:          {http.StatusForbidden, ErrCodeDevicePending},
		authsvc.ErrDeviceProofRequired:    {http.StatusUnauthorized, ErrCodeDeviceProofRequired},
		authsvc.ErrDeviceProofInvalid:     {http.StatusUnauthorized, ErrCodeDeviceProofInvalid},
		authsvc.ErrDeviceProofClockSkew:   {http.StatusUnauthorized, ErrCodeDeviceProofClockSkew},
		authsvc.ErrDeviceKeyInvalid:       {http.StatusBadRequest, ErrCodeDeviceKeyInvalid},
		authsvc.ErrDeviceBelongsToAnother: {http.StatusForbidden, ErrCodeDeviceKeyTaken},
		authsvc.ErrDeviceNotFound:         {http.StatusNotFound, ErrCodeDeviceNotFound},
	} {
		rec := httptest.NewRecorder()
		if !writeDeviceRefusal(rec, err) {
			t.Errorf("%v was not answered as a device refusal", err)
			continue
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != want.status || body["code"] != want.code || body["hint"] == "" {
			t.Errorf("%v: %d %v, want %d %s with a hint", err, rec.Code, body, want.status, want.code)
		}
	}
	if writeDeviceRefusal(httptest.NewRecorder(), errors.New("database down")) {
		t.Error("an unrelated error was answered as a device refusal")
	}
}

// A proof refused for its time carries the gateway's clock, which is what the
// device stamps its next proof on; no other refusal does.
func TestWriteDeviceRefusal_aClockRefusalCarriesTheGatewaysTime(t *testing.T) {
	rec := httptest.NewRecorder()
	before := time.Now().Unix()
	writeDeviceRefusal(rec, fmt.Errorf("%w: iat is 10m0s from this gateway's clock", authsvc.ErrDeviceProofClockSkew))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	got, ok := body["server_time"].(float64)
	if !ok || int64(got) < before || int64(got) > time.Now().Unix() {
		t.Errorf("server_time = %v, want the gateway's unix time", body["server_time"])
	}
	rec = httptest.NewRecorder()
	writeDeviceRefusal(rec, authsvc.ErrDeviceProofInvalid)
	var invalid map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &invalid)
	if _, has := invalid["server_time"]; has {
		t.Error("an invalid proof's refusal carries server_time")
	}
	if writeDeviceRefusal(httptest.NewRecorder(), errors.New("database down")) {
		t.Error("an unrelated error was answered as a device refusal")
	}
}
