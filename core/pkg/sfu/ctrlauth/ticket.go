// Package ctrlauth is how a namespace's gateways and its SFUs trust each other.
//
// The SFU listens on the WireGuard overlay, where every namespace's services
// live, so being on the overlay proves nothing about who is calling. Three
// things cross between a gateway and an SFU, and each carries a MAC keyed by a
// value derived from the namespace's own TURN secret (which both ends already
// hold, and no end user does):
//
//   - a join ticket, gateway to SFU, on the signalling upgrade: who the
//     gateway authenticated, for which room, and whether the namespace
//     admitted them. The SFU takes the peer's identity from the ticket and
//     from nowhere else.
//   - a control request, gateway to SFU (kick, mute).
//   - a membership event, SFU to gateway (join, leave).
//
// The key is per namespace, so an SFU of one namespace can neither forge a
// ticket for another's nor report into it. It is derived under its own
// purpose, so it is unrelated to the TURN credentials the same secret signs.
package ctrlauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/constants"
	"github.com/Orama-Network/orama/core/pkg/secrets"
)

const (
	// TicketHeader carries a join ticket on the signalling upgrade.
	TicketHeader = "X-Orama-SFU-Ticket"

	// TicketTTL is how long a ticket is good for. It covers one dial, so it is
	// short: a revoked admission is refused at the next ticket, and a ticket
	// that leaks is useless within the minute.
	TicketTTL = 30 * time.Second

	// keyPurpose is the HKDF domain separator of the key.
	keyPurpose = "webrtc-sfu-control"

	// maxTicketBytes bounds what the SFU decodes from a header.
	maxTicketBytes = 4096
)

var (
	// ErrNoTicket means the upgrade carried no ticket: it did not come through a gateway.
	ErrNoTicket = errors.New("no join ticket")
	// ErrBadTicket means the ticket is malformed or its MAC does not verify.
	ErrBadTicket = errors.New("join ticket is not valid")
	// ErrExpiredTicket means the ticket is past its expiry.
	ErrExpiredTicket = errors.New("join ticket has expired")
)

// Key derives the namespace's control key from its TURN secret. The secret is
// trimmed first: it is read from files, and one end's copy having a trailing
// newline would derive a different key and fail every call with no visible reason.
func Key(turnSecret string) ([]byte, error) {
	key, err := secrets.DeriveKey(strings.TrimSpace(turnSecret), keyPurpose)
	if err != nil {
		return nil, fmt.Errorf("no SFU control key, the namespace has no TURN secret: %w", err)
	}
	return key, nil
}

// Ticket is what a gateway vouches for when it lets a socket through to an SFU.
type Ticket struct {
	Namespace string `json:"ns"`
	Room      string `json:"room"`
	// UserID is who the client is to the namespace: the account its claims
	// provider signed into the token, else the token's authenticated subject.
	UserID string `json:"uid"`
	// DeviceID is the device the session is bound to ("" when bound to none).
	DeviceID string `json:"did,omitempty"`
	// Muted is set when the namespace has muted this user in this room: the
	// SFU forwards none of their audio from the start.
	Muted bool `json:"muted,omitempty"`
	// VideoMuted is Muted for video: the SFU forwards none of the user's video
	// tracks (camera or screen) from the start.
	VideoMuted bool `json:"vmuted,omitempty"`
	// EventSink is the internal URL of a namespace gateway the SFU reports
	// membership to.
	EventSink string `json:"sink,omitempty"`
	// IssuedAtMs is when the gateway made the ticket, in unix milliseconds. The
	// SFU refuses a ticket issued before a kick of the same user in the same
	// room, so a join already in flight when the kick landed is not let in.
	IssuedAtMs int64 `json:"iat"`
	// AdmitExp is the unix second the admission this ticket was issued on ends,
	// set only when the namespace requires admission (0: the session is not
	// bounded by one). The SFU closes the peer then, so an admission's TTL holds
	// for a live session and not only at the next join.
	AdmitExp int64 `json:"aexp,omitempty"`
	// AdmitGen is the generation of the admission this ticket was issued on, set
	// only when the namespace requires admission (0: none, or the admission
	// predates generations). A kick carries the generation it revoked, and the
	// SFU refuses a ticket whose generation is not newer than it: a number
	// compared with a number, so a user admitted again right after a kick is not
	// taken for the kicked one by two gateways' clocks.
	AdmitGen int64 `json:"agen,omitempty"`
	// Expires is the unix second the ticket stops being valid.
	Expires int64 `json:"exp"`
}

// Seal signs the ticket: base64url(json) "." hex(hmac-sha256).
func (t Ticket) Seal(key []byte) (string, error) {
	if t.Namespace == "" || t.Room == "" || t.UserID == "" || t.Expires == 0 || t.IssuedAtMs == 0 {
		return "", errors.New("a join ticket needs a namespace, a room, a user, an issue time and an expiry")
	}
	if err := ValidateSink(t.EventSink); err != nil {
		return "", err
	}
	body, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("failed to encode the join ticket: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + hex.EncodeToString(mac(key, payload)), nil
}

// OpenTicket verifies a ticket and returns it. now is the clock the expiry is
// judged by.
func OpenTicket(key []byte, token string, now time.Time) (Ticket, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Ticket{}, ErrNoTicket
	}
	if len(token) > maxTicketBytes {
		return Ticket{}, ErrBadTicket
	}
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Ticket{}, ErrBadTicket
	}
	presented, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(presented, mac(key, payload)) {
		return Ticket{}, ErrBadTicket
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Ticket{}, ErrBadTicket
	}
	var t Ticket
	if err := json.Unmarshal(body, &t); err != nil || t.Namespace == "" || t.Room == "" || t.UserID == "" {
		return Ticket{}, ErrBadTicket
	}
	if now.Unix() >= t.Expires {
		return Ticket{}, ErrExpiredTicket
	}
	if err := ValidateSink(t.EventSink); err != nil {
		return Ticket{}, ErrBadTicket
	}
	return t, nil
}

// ValidateSink checks an event sink: empty (no reporting) or an http URL whose
// host is an address on the WireGuard overlay. Reports carry a MAC over plain
// HTTP, so they go to the overlay or nowhere.
func ValidateSink(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("event sink %q must be http://<overlay address>:<port>", raw)
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !constants.WireGuardOverlay().Contains(addr) {
		return fmt.Errorf("event sink %q is not an address on the WireGuard overlay", raw)
	}
	return nil
}

func mac(key []byte, payload string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
