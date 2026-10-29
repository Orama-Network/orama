package auth

import (
	"reflect"
	"testing"
	"time"
)

// Every field of a session binding survives a mint and a verify: a token
// minted again from presented claims (Binding) is bound exactly as the one it
// came from. A field added to SessionBinding without a claim to carry it
// fails here instead of being dropped by the next re-mint.
func TestBinding_everyFieldRoundTripsThroughAToken(t *testing.T) {
	s := createTestService(t)
	want := SessionBinding{
		DeviceID:    attestedDevice,
		SessionID:   "session-1",
		Attestation: &AttestationClaim{Platform: "ios", AppID: "TEAM.chat.anchat", AttestedAt: time.Now().Unix()},
		Pending:     true,
	}
	v := reflect.ValueOf(want)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Fatalf("set SessionBinding.%s in this test so its round trip is checked", v.Type().Field(i).Name)
		}
	}
	token, _, err := s.GenerateBoundJWT("anchat", "0xwallet", AccessTokenLifetime, nil, want)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ParseAndVerifyJWT(token)
	if err != nil {
		t.Fatal(err)
	}
	if got := claims.Binding(); !reflect.DeepEqual(got, want) {
		t.Errorf("a token's binding read back %+v, want %+v", got, want)
	}
}

// Attesting proves the app, not that the namespace admitted the device: a
// pending device's session that attests gets back a token as restricted as
// the one it presented, and a full session stays full.
func TestReissueAttested_keepsAPendingDevicesSessionPending(t *testing.T) {
	s := createTestService(t)
	att := &AttestationClaim{Platform: "android", AppID: "chat.anchat", AttestedAt: time.Now().Unix()}
	for _, pending := range []bool{true, false} {
		presented, _, err := s.GenerateBoundJWT("anchat", "0xwallet", AccessTokenLifetime, nil,
			SessionBinding{DeviceID: attestedDevice, SessionID: "s", Pending: pending})
		if err != nil {
			t.Fatal(err)
		}
		claims, err := s.ParseAndVerifyJWT(presented)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := s.ReissueAttested(claims, att)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ParseAndVerifyJWT(token)
		if err != nil {
			t.Fatal(err)
		}
		if got.PendingDevice() != pending || got.Att == nil || got.Did != attestedDevice || got.Sid != "s" {
			t.Errorf("pending=%v: the attested token is aud %q, att %v, did %q, sid %q", pending, got.Aud, got.Att, got.Did, got.Sid)
		}
	}
}
