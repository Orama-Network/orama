package sfu

import (
	"net/http"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
)

// feat-964: a namespace removes one device of a user from a room; the user's
// other devices stay, and only that device's in-flight joins are refused.

func ticketFrom(t *testing.T, s *Server, room, user, device string) ctrlauth.Ticket {
	t.Helper()
	tk := testTicket(t, s, room, user)
	tk.DeviceID = device
	return tk
}

func TestKickDevice_removesThatDeviceAndNoOther(t *testing.T) {
	s := newAuthServer(t)
	phone, _ := joinAs(t, s, ticketFrom(t, s, "r1", "alice", "phone"), "")
	joinAs(t, s, ticketFrom(t, s, "r1", "alice", "laptop"), "")

	w := control(t, s, ctrlauth.KickDevicePath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", DeviceID: "phone", AtMs: nowMs()})

	if n := affected(t, w); n != 1 {
		t.Fatalf("affected = %d, want the phone alone", n)
	}
	readType(t, phone, MessageTypeKicked)
	left := s.roomManager.GetRoom("r1").peersOfUser("alice")
	if len(left) != 1 || left[0].DeviceID != "laptop" {
		t.Fatalf("peers of alice left = %d, want the laptop alone", len(left))
	}
}

func TestKickDevice_refusesThatDevicesInFlightJoinAndNoOtherDevices(t *testing.T) {
	s := newAuthServer(t)
	phoneInFlight := ticketFrom(t, s, "r1", "alice", "phone")
	phoneInFlight.AdmitGen = 3
	laptop := ticketFrom(t, s, "r1", "alice", "laptop")
	laptop.AdmitGen = 3

	control(t, s, ctrlauth.KickDevicePath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", DeviceID: "phone", AtMs: nowMs(), AdmitGen: 4})

	_, resp, err := tryDial(t, s, "room=r1", http.Header{ctrlauth.TicketHeader: {sealTicket(t, s, phoneInFlight)}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the kicked device's in-flight ticket was let in: err=%v resp=%v", err, resp)
	}
	joinAs(t, s, laptop, "")                          // another device of the user
	joinAs(t, s, testTicket(t, s, "r1", "alice"), "") // a session bound to no device: a device kick is not a user kick
	readmitted := ticketFrom(t, s, "r1", "alice", "phone")
	readmitted.AdmitGen = 5 // admitted again after the kick
	joinAs(t, s, readmitted, "")
}

func TestKickDevice_needsADevice(t *testing.T) {
	s := newAuthServer(t)
	w := control(t, s, ctrlauth.KickDevicePath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: nowMs()})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a device kick naming no device: status %d, want 400 (a user kick has its own route)", w.Code)
	}
	if s.kicks.refuses("r1", "alice", "", 0, 0) {
		t.Error("the refused request left a kick on record")
	}
}

func TestKickLog_aDeviceKickRefusesThatDeviceAndAUserKickEveryDevice(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	at := now.UnixMilli()

	k.record("r", "alice", "phone", at, 0)
	if !k.refuses("r", "alice", "phone", at-1000, 0) {
		t.Error("a device kick did not refuse that device's earlier ticket")
	}
	for _, device := range []string{"laptop", ""} {
		if k.refuses("r", "alice", device, at-1000, 0) {
			t.Errorf("a kick of the phone refused device %q", device)
		}
	}
	if k.refuses("r", "bob", "phone", at-1000, 0) {
		t.Error("a kick of alice's phone refused bob's device of the same id")
	}

	k.record("r", "alice", "", at, 0)
	for _, device := range []string{"phone", "laptop", ""} {
		if !k.refuses("r", "alice", device, at-1000, 0) {
			t.Errorf("a kick of the user did not refuse device %q", device)
		}
	}
}
