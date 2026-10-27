package sfu

import (
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

// keyframeSettle is how long a new subscriber's negotiation gets before the
// SFU asks the publishers for keyframes on its behalf.
const keyframeSettle = 300 * time.Millisecond

// removeTrackSendersFromPeers removes RTPSenders for the given track IDs from all peers.
// This fixes the ghost track bug from the original implementation.
func (r *Room) removeTrackSendersFromPeers(trackIDs []string) {
	trackIDSet := make(map[string]bool, len(trackIDs))
	for _, id := range trackIDs {
		trackIDSet[id] = true
	}

	for _, peer := range r.snapshotPeers("") {
		if peer.pc == nil {
			continue
		}
		for _, sender := range peer.pc.GetSenders() {
			if sender.Track() == nil {
				continue
			}
			if trackIDSet[sender.Track().ID()] {
				if err := peer.pc.RemoveTrack(sender); err != nil {
					r.logger.Warn("Failed to remove track sender",
						zap.String("peer_id", peer.ID),
						zap.String("track_id", sender.Track().ID()),
						zap.Error(err))
				}
			}
		}
	}
}

// BroadcastTrack creates a local track from a remote track and forwards it to all other peers.
func (r *Room) BroadcastTrack(sourcePeerID string, track *webrtc.TrackRemote) {
	codec := track.Codec()

	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		codec.RTPCodecCapability,
		track.Kind().String()+"-"+sourcePeerID,
		sourcePeerID,
	)
	if err != nil {
		r.logger.Error("Failed to create local track", zap.Error(err))
		return
	}

	// Look up source peer's UserID
	r.peersMu.RLock()
	var sourceUserID string
	var drop func() bool
	if sourcePeer, ok := r.peers[sourcePeerID]; ok {
		sourceUserID = sourcePeer.UserID
		if m, ok := mediaOf(track.Kind()); ok {
			drop = sourcePeer.mutedFlag(m).Load
		}
	}
	r.peersMu.RUnlock()

	// Store for future joiners
	r.publishedTracksMu.Lock()
	r.publishedTracks[localTrack.ID()] = &publishedTrack{
		sourcePeerID:    sourcePeerID,
		sourceUserID:    sourceUserID,
		localTrack:      localTrack,
		remoteTrackSSRC: uint32(track.SSRC()),
		kind:            track.Kind().String(),
		keyframes:       newKeyframeLimiter(keyframeMinInterval),
	}
	r.publishedTracksMu.Unlock()

	go forwardRTP(track, localTrack, r.logger.With(zap.String("track_id", localTrack.ID())), drop)

	// Add to all current peers except the source
	for _, peer := range r.snapshotPeers(sourcePeerID) {
		if _, err := peer.AddTrack(localTrack); err != nil {
			r.logger.Warn("Failed to add track to peer",
				zap.String("peer_id", peer.ID), zap.Error(err))
			continue
		}
		peer.SendMessage(NewServerMessage(MessageTypeTrackAdded, &TrackAddedData{
			PeerID:   sourcePeerID,
			UserID:   sourceUserID,
			TrackID:  localTrack.ID(),
			StreamID: localTrack.StreamID(),
			Kind:     track.Kind().String(),
		}))
	}
}

// SendExistingTracksTo sends all published tracks to a newly joined peer.
// Uses batch mode for a single renegotiation.
func (r *Room) SendExistingTracksTo(peer *Peer) {
	r.publishedTracksMu.RLock()
	var tracks []*publishedTrack
	for _, pt := range r.publishedTracks {
		if pt.sourcePeerID != peer.ID {
			tracks = append(tracks, pt)
		}
	}
	r.publishedTracksMu.RUnlock()

	if len(tracks) == 0 {
		return
	}

	peer.StartTrackBatch()
	for _, pt := range tracks {
		if _, err := peer.AddTrack(pt.localTrack); err != nil {
			r.logger.Warn("Failed to add existing track", zap.Error(err))
			continue
		}
		peer.SendMessage(NewServerMessage(MessageTypeTrackAdded, &TrackAddedData{
			PeerID:   pt.sourcePeerID,
			UserID:   pt.sourceUserID,
			TrackID:  pt.localTrack.ID(),
			StreamID: pt.localTrack.StreamID(),
			Kind:     pt.kind,
		}))
	}
	peer.EndTrackBatch()

	// Request keyframes for video tracks after negotiation settles
	go func() {
		<-timeAfter(keyframeSettle)
		r.RequestKeyframeForAllVideoTracks()
	}()
}

// RequestKeyframe asks the source peer of a video track for a keyframe. PLIs
// for one track are spaced keyframeMinInterval apart; a request inside the
// interval is deferred to its end, or merged with the deferred one.
func (r *Room) RequestKeyframe(trackID string) {
	r.publishedTracksMu.RLock()
	pt, ok := r.publishedTracks[trackID]
	r.publishedTracksMu.RUnlock()
	if !ok || pt.kind != "video" {
		return
	}

	action, wait := pt.keyframes.reserve(time.Now())
	switch action {
	case keyframeSendNow:
		r.sendPLI(trackID, pt)
	case keyframeDefer:
		time.AfterFunc(wait, func() {
			pt.keyframes.fired(time.Now())
			r.sendPLI(trackID, pt)
		})
	case keyframeCoalesced:
		// The deferred PLI already scheduled for this track serves this request.
	}
}

// sendPLI writes a PLI for pt to its source peer's connection.
func (r *Room) sendPLI(trackID string, pt *publishedTrack) {
	r.peersMu.RLock()
	source, ok := r.peers[pt.sourcePeerID]
	r.peersMu.RUnlock()
	if !ok {
		return
	}

	pli := &rtcp.PictureLossIndication{MediaSSRC: pt.remoteTrackSSRC}
	if err := source.sendRTCP([]rtcp.Packet{pli}); err != nil {
		r.logger.Debug("Failed to send PLI", zap.String("track_id", trackID), zap.Error(err))
	}
}

// RequestKeyframeForAllVideoTracks sends PLIs for all video tracks.
func (r *Room) RequestKeyframeForAllVideoTracks() {
	r.publishedTracksMu.RLock()
	var ids []string
	for id, pt := range r.publishedTracks {
		if pt.kind == "video" {
			ids = append(ids, id)
		}
	}
	r.publishedTracksMu.RUnlock()

	for _, id := range ids {
		r.RequestKeyframe(id)
	}
}
