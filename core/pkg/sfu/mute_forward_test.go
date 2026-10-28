package sfu

import (
	"sync/atomic"
	"testing"
)

type countingSink struct{ writes atomic.Int64 }

func (c *countingSink) Write(b []byte) (int, error) { c.writes.Add(1); return len(b), nil }

func TestForwardRTP_dropsWhileMutedAndResumesAfter(t *testing.T) {
	var muted atomic.Bool
	sink := &countingSink{}

	muted.Store(true)
	forwardRTP(&scriptedSource{n: 5}, sink, testLogger(), muted.Load)
	if got := sink.writes.Load(); got != 0 {
		t.Fatalf("a muted publisher's packets reached the subscribers: %d writes", got)
	}

	muted.Store(false)
	forwardRTP(&scriptedSource{n: 5}, sink, testLogger(), muted.Load)
	if got := sink.writes.Load(); got != 5 {
		t.Fatalf("writes after unmuting = %d, want 5", got)
	}
}

// feat-972: a peer that joins on a ticket muting both media has its flags set
// before it is in the room, so the forwarding of each track it publishes drops
// its packets from the first: no mute call has to follow the join.
func TestTicketMutes_dropEveryPacketOfBothMediaFromTheFirst(t *testing.T) {
	s := newAuthServer(t)
	tk := testTicket(t, s, "r1", "alice")
	tk.Muted, tk.VideoMuted = true, true
	joinAs(t, s, tk, "")

	p := peerOf(s, "r1", "alice")
	for _, m := range []media{mediaAudio, mediaVideo} {
		sink := &countingSink{}
		forwardRTP(&scriptedSource{n: 5}, sink, testLogger(), p.mutedFlag(m).Load)
		if got := sink.writes.Load(); got != 0 {
			t.Errorf("%s: %d packets of a peer admitted muted reached the subscribers", m, got)
		}
	}
}

func TestForwardRTP_nilDropForwardsEverything(t *testing.T) {
	sink := &countingSink{}
	forwardRTP(&scriptedSource{n: 3}, sink, testLogger(), nil)
	if got := sink.writes.Load(); got != 3 {
		t.Fatalf("writes = %d, want 3", got)
	}
}
