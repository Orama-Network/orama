package sfu

import (
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
)

// kickSkewMargin is how far two gateways' clocks may disagree. A kick carries
// the clock of the gateway that revoked (AtMs) and a ticket the clock of the
// gateway that issued it (IssuedAtMs); they may be different gateways.
const kickSkewMargin = 10 * time.Second

// kickWindow is how long after a kick this SFU refuses tickets of the kicked
// user to the room: a ticket lives TicketTTL, and the margin covers the clocks.
const kickWindow = ctrlauth.TicketTTL + kickSkewMargin

// kickLog remembers when a user was last removed from a room, so a join whose
// ticket was issued while the admission was still valid is refused when it
// arrives after the kick. Where the namespace admits users, the kick and the
// ticket each carry the generation of an admission (ctrlauth.Ticket.AdmitGen)
// and a ticket is refused exactly when its generation is not newer than the
// kick's: no clock is involved, so a user admitted again right after a kick,
// whose ticket is of a newer generation, is let in. Otherwise (a namespace
// without admission, a kick from a gateway that predates generations, a ticket
// issued on an admission that predates them) two clocks are involved and they
// are not the same clock: a ticket is refused when this SFU receives it within
// kickWindow of the kick (this SFU's own clock), unless its issue time is later
// than the kick's by more than kickSkewMargin (the gateways' clocks), which only
// a ticket issued after the revocation can have. Entries older than the window
// can refuse nothing, and are dropped.
//
// A kick names the user (every device of theirs) or one device of the user. A
// ticket is refused by a kick of its user, and by a kick of the device it was
// issued for; a kick of one device refuses no other device's ticket.
type kickLog struct {
	mu    sync.Mutex
	now   func() time.Time
	kicks map[string]kickRecord // kickKey, or deviceKickKey for a device kick
}

type kickRecord struct {
	atMs int64     // the kicking gateway's clock, unix ms
	gen  int64     // the newest admission generation a kick revoked (0: none known)
	seen time.Time // this SFU's clock when it received the latest kick
}

func newKickLog() *kickLog { return &kickLog{now: time.Now, kicks: make(map[string]kickRecord)} }

func kickKey(room, user string) string { return room + "\x00" + user }

// deviceKickKey keys a kick of one device. Room, user and device ids hold no
// NUL, so it collides with no kickKey and with no other device's key.
func deviceKickKey(room, user, device string) string { return kickKey(room, user) + "\x00" + device }

// logKey is the key of a kick of device ("": every device) of user in room.
func logKey(room, user, device string) string {
	if device == "" {
		return kickKey(room, user)
	}
	return deviceKickKey(room, user, device)
}

// record logs a kick of user from room: of every device of theirs when device
// is "", of that device alone otherwise.
func (k *kickLog) record(room, user, device string, atMs, gen int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	for key, rec := range k.kicks {
		if now.Sub(rec.seen) > kickWindow {
			delete(k.kicks, key)
		}
	}
	key := logKey(room, user, device)
	if prev, ok := k.kicks[key]; ok {
		atMs = max(atMs, prev.atMs)
		// A kick that carries no generation (an old gateway, or no admission
		// on record) puts the entry back on the clock rule. Keeping the
		// earlier kick's generation instead let a ticket of an admission made
		// between the two kicks through the second one.
		if gen > 0 {
			gen = max(gen, prev.gen)
		}
	}
	k.kicks[key] = kickRecord{atMs: atMs, gen: gen, seen: now}
}

// refuses reports whether a ticket for (room, user) from device ("": a session
// bound to none), issued at issuedMs by a gateway on the admission of
// generation gen (0: none) and received now, predates a kick of the user or of
// that device.
func (k *kickLog) refuses(room, user, device string, issuedMs, gen int64) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.refusedBy(kickKey(room, user), issuedMs, gen) {
		return true
	}
	return device != "" && k.refusedBy(deviceKickKey(room, user, device), issuedMs, gen)
}

// refusedBy applies one logged kick to a ticket. The caller holds k.mu.
func (k *kickLog) refusedBy(key string, issuedMs, gen int64) bool {
	rec, ok := k.kicks[key]
	if !ok || k.now().Sub(rec.seen) > kickWindow {
		return false
	}
	if gen > 0 && rec.gen > 0 {
		return gen <= rec.gen
	}
	return issuedMs <= rec.atMs+kickSkewMargin.Milliseconds()
}
