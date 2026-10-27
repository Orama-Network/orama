package sfu

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"github.com/Orama-Network/orama/core/pkg/sfu/roomid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// joinReadTimeout bounds the wait for a socket's join frame.
const joinReadTimeout = 10 * time.Second

var errUserKicked = errors.New("the user was removed from the room at or after the time this ticket was issued")

// openTicket authenticates the upgrade: it must carry a join ticket the
// namespace's gateway signed, for this namespace, issued after any kick of the
// same user from the same room. The peer's identity is the ticket's, never the
// join frame's, so a client cannot say who it is.
func (s *Server) openTicket(r *http.Request) (ctrlauth.Ticket, int, error) {
	t, err := ctrlauth.OpenTicket(s.controlKey, r.Header.Get(ctrlauth.TicketHeader), time.Now())
	switch {
	case err != nil:
		return t, http.StatusUnauthorized, err
	case t.Namespace != s.config.Namespace:
		return t, http.StatusForbidden, errors.New("join ticket is for another namespace")
	case s.kicks.refuses(t.Room, t.UserID, t.DeviceID, t.IssuedAtMs, t.AdmitGen):
		return t, http.StatusForbidden, errUserKicked
	}
	return t, 0, nil
}

// handleSignal upgrades to WebSocket and runs the signaling loop for one peer.
func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	s.drainingMu.RLock()
	draining := s.draining
	s.drainingMu.RUnlock()
	if draining {
		http.Error(w, "server draining", http.StatusServiceUnavailable)
		return
	}

	ticket, status, err := s.openTicket(r)
	if err != nil {
		s.logger.Warn("Signalling upgrade refused", zap.String("remote", r.RemoteAddr), zap.Error(err))
		http.Error(w, err.Error(), status)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}
	s.logger.Debug("WebSocket connected", zap.String("remote", r.RemoteAddr))

	roomID, ok := s.readJoin(conn, r, ticket)
	if !ok {
		conn.Close()
		return
	}
	s.joinRoom(conn, roomID, ticket)
}

// readJoin reads and checks the first frame. On any problem it tells the client
// why and reports false.
func (s *Server) readJoin(conn *websocket.Conn, r *http.Request, ticket ctrlauth.Ticket) (string, bool) {
	refuse := func(code, message string) (string, bool) {
		conn.WriteMessage(websocket.TextMessage, mustMarshal(NewErrorMessage(code, message)))
		return "", false
	}

	conn.SetReadDeadline(time.Now().Add(joinReadTimeout))
	_, msgBytes, err := conn.ReadMessage()
	if err != nil {
		s.logger.Warn("Failed to read join message", zap.Error(err))
		return "", false
	}
	conn.SetReadDeadline(time.Time{}) // Clear deadline

	var msg ClientMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		return refuse("invalid_message", "malformed JSON")
	}
	if msg.Type != MessageTypeJoin {
		return refuse("invalid_message", "first message must be join")
	}
	var joinData JoinData
	if err := json.Unmarshal(msg.Data, &joinData); err != nil || joinData.RoomID == "" {
		return refuse("invalid_join", "roomId required")
	}
	if err := roomid.Validate(joinData.RoomID); err != nil {
		return refuse("invalid_join", err.Error())
	}
	// The gateway routed this socket to the room's owner by the ?room= query;
	// a join for another room would land the peer on an SFU that does not own
	// that room and split the call.
	if q := r.URL.Query().Get("room"); q != "" && q != joinData.RoomID {
		return refuse("room_mismatch", "join roomId must equal the room query parameter")
	}
	// The ticket admits one room. A join for another is the client asking for
	// something the namespace did not grant.
	if joinData.RoomID != ticket.Room {
		return refuse("room_mismatch", "join roomId must equal the room the gateway admitted this socket to")
	}
	return joinData.RoomID, true
}

// joinRoom puts the authenticated peer in its room and runs its signalling loop.
func (s *Server) joinRoom(conn *websocket.Conn, roomID string, ticket ctrlauth.Ticket) {
	room := s.roomManager.GetOrCreateRoom(roomID)
	peer := NewPeer(ticket.UserID, conn, room, s.logger)
	peer.DeviceID = ticket.DeviceID
	peer.eventSink = ticket.EventSink
	peer.muted.Store(ticket.Muted)
	peer.videoMuted.Store(ticket.VideoMuted)
	s.roomManager.reporter.observe(ticket.EventSink)

	if err := room.AddPeer(peer); err != nil {
		conn.WriteMessage(websocket.TextMessage, mustMarshal(NewErrorMessage("join_failed", err.Error())))
		conn.Close()
		return
	}
	// The ticket was checked against the kick log at the upgrade, but the client
	// chooses when to send its join frame, so a kick can land in between and
	// find no peer to remove. Checked again now that the peer is in the room, a
	// kick is either in the log here or finds the peer there: it records before
	// it looks.
	if s.kicks.refuses(ticket.Room, ticket.UserID, ticket.DeviceID, ticket.IssuedAtMs, ticket.AdmitGen) {
		s.logger.Info("Joined peer removed: the user was kicked while its join was in flight",
			zap.String("room", ticket.Room), zap.String("user_id", ticket.UserID))
		room.KickPeer(peer)
		return
	}
	if ticket.AdmitExp > 0 {
		go s.endAtAdmissionExpiry(peer, room, time.Unix(ticket.AdmitExp, 0))
	}

	// Send welcome with current participants
	peer.SendMessage(NewServerMessage(MessageTypeWelcome, &WelcomeData{
		PeerID:       peer.ID,
		RoomID:       room.ID,
		Participants: room.GetParticipants(),
	}))
	// After the welcome, so the peer is told of a correction as it is told of
	// any later mute, by a forced participant-state, once it knows who it is.
	s.settleMute(room, peer, ticket, mediaAudio)
	s.settleMute(room, peer, ticket, mediaVideo)

	// Send TURN credentials
	if s.config.TURNSecret != "" && len(s.config.TURNServers) > 0 {
		s.sendTURNCredentials(peer, MessageTypeTURNCredentials)
	}

	// Send existing tracks from other peers
	room.SendExistingTracksTo(peer)

	// Start credential refresh goroutine
	if s.config.TURNCredentialTTL > 0 {
		go s.credentialRefreshLoop(peer)
	}

	// Signaling read loop
	s.signalingLoop(peer, room)
}

// settleMute gives peer the logged mute state of m when its ticket's snapshot
// of it is older than the log. It reads the log after the peer is in the room,
// so a mute is either in the log here or finds the peer there. It then reads
// again: a mute landing between the first read and the store would apply to the
// peer first and be overwritten by the older state, and is seen by the second
// read.
func (s *Server) settleMute(room *Room, peer *Peer, ticket ctrlauth.Ticket, m media) {
	mutes, flag := s.muteLogOf(m), peer.mutedFlag(m)
	for {
		rec, ok := mutes.correction(ticket.Room, ticket.UserID, ticket.IssuedAtMs)
		if !ok {
			return
		}
		if flag.Load() != rec.muted {
			room.setPeerMuted(peer, m, rec.muted)
		}
		if mutes.current(ticket.Room, ticket.UserID) == rec.seq {
			return
		}
	}
}

// endAtAdmissionExpiry removes peer when the admission its ticket was issued on
// ends, unless it has left by then.
func (s *Server) endAtAdmissionExpiry(peer *Peer, room *Room, at time.Time) {
	select {
	case <-peer.done:
		return
	case <-s.expireAfter(time.Until(at)):
	}
	s.logger.Info("Peer removed: its admission to the room ended", zap.String("room", room.ID), zap.String("user_id", peer.UserID))
	room.ExpirePeer(peer)
}

// handleState relays a client's audio-state or video-state to the room.
func (s *Server) handleState(peer *Peer, room *Room, msg ClientMessage) {
	var data StateData
	if err := json.Unmarshal(msg.Data, &data); err != nil || data.Enabled == nil {
		peer.SendMessage(NewErrorMessage("invalid_state", `"`+string(msg.Type)+`" needs {"enabled": true|false}`))
		return
	}
	kind := "audio"
	if msg.Type == MessageTypeVideoState {
		kind = "video"
	}
	room.relayState(peer, kind, *data.Enabled)
}
