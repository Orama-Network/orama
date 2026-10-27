package sfu

import (
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"go.uber.org/zap"
)

// reportMembership queues a join or leave of peer for the namespace.
func (r *Room) reportMembership(peer *Peer, eventType, reason string) {
	if r.reporter == nil {
		return
	}
	r.reporter.report(ctrlauth.MembershipEvent{
		Type:     eventType,
		Room:     r.ID,
		UserID:   peer.UserID,
		DeviceID: peer.DeviceID,
		PeerID:   peer.ID,
		Reason:   reason,
		At:       time.Now().UTC(),
	})
}

// peersOfUser returns every peer in the room authenticated as userID: one per
// device the user is connected from.
func (r *Room) peersOfUser(userID string) []*Peer {
	var out []*Peer
	for _, p := range r.snapshotPeers("") {
		if p.UserID == userID {
			out = append(out, p)
		}
	}
	return out
}

// KickUser removes every peer of userID from the room: each is told why, then
// its socket and PeerConnection are closed. It returns how many were removed.
func (r *Room) KickUser(userID string) int {
	peers := r.peersOfUser(userID)
	for _, p := range peers {
		r.KickPeer(p)
	}
	return len(peers)
}

// KickPeer removes one peer on the namespace's behalf.
func (r *Room) KickPeer(p *Peer) {
	p.kicked.Store(true)
	if err := p.SendMessage(NewServerMessage(MessageTypeKicked, &KickedData{Code: KickedCodeRemoved, Reason: "removed from the room by the namespace"})); err != nil {
		r.logger.Debug("Could not tell a kicked peer why", zap.String("peer_id", p.ID), zap.Error(err))
	}
	r.RemovePeer(p.ID)
}

// ExpirePeer removes peer because the admission it joined on has ended: it is
// told why, then its socket and PeerConnection are closed.
func (r *Room) ExpirePeer(peer *Peer) {
	peer.expired.Store(true)
	if err := peer.SendMessage(NewServerMessage(MessageTypeKicked, &KickedData{Code: KickedCodeExpired, Reason: "your admission to the room has ended"})); err != nil {
		r.logger.Debug("Could not tell a peer its admission ended", zap.String("peer_id", peer.ID), zap.Error(err))
	}
	r.RemovePeer(peer.ID)
}

// KickDevice removes the peers of userID connected from deviceID, and no other
// device's. It returns how many were removed.
func (r *Room) KickDevice(userID, deviceID string) int {
	n := 0
	for _, p := range r.peersOfUser(userID) {
		if p.DeviceID == deviceID {
			r.KickPeer(p)
			n++
		}
	}
	return n
}

// MuteUser stops (or resumes) the forwarding of every track of kind m of
// userID and tells the room. It returns how many peers of the user the room has.
func (r *Room) MuteUser(userID string, m media, muted bool) int {
	peers := r.peersOfUser(userID)
	for _, p := range peers {
		r.setPeerMuted(p, m, muted)
	}
	return len(peers)
}

// setPeerMuted sets one peer's mute of m and tells the room, as one step per
// peer: two of them interleaving (a join settling its mute while the user is
// unmuted) would otherwise leave the room told the older state. Video resumed
// mid-stream cannot be decoded until the next keyframe, so an unmute of video
// asks the publisher for one.
func (r *Room) setPeerMuted(p *Peer, m media, muted bool) {
	p.muteMu.Lock()
	defer p.muteMu.Unlock()
	p.mutedFlag(m).Store(muted)
	r.broadcastMessage("", NewServerMessage(MessageTypeParticipantState, &ParticipantStateData{
		PeerID: p.ID, UserID: p.UserID, Kind: m.String(), Enabled: !muted, Forced: true,
	}))
	if m == mediaVideo && !muted {
		r.RequestKeyframe(mediaVideo.String() + "-" + p.ID)
	}
}

// relayState tells the others in the room that peer turned its audio or video
// on or off. The server does not act on it: the media itself shows whether a
// track is live. It is for the others' interfaces.
func (r *Room) relayState(peer *Peer, kind string, enabled bool) {
	r.broadcastMessage(peer.ID, NewServerMessage(MessageTypeParticipantState, &ParticipantStateData{
		PeerID: peer.ID, UserID: peer.UserID, Kind: kind, Enabled: enabled,
	}))
}
