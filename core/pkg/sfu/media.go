package sfu

import (
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

// media is a kind of track a namespace can stop forwarding of, apart from the
// other: a user's audio (webrtc_mute) or their video, camera and screen alike
// (webrtc_mute_video). Each has its own flag on the peer, its own log on the
// server and its own control route, and they never touch each other.
type media int

const (
	mediaAudio media = iota
	mediaVideo
)

// String is the kind as the protocol names it: participant-state's kind, and
// pion's name for the track's codec type.
func (m media) String() string {
	if m == mediaVideo {
		return "video"
	}
	return "audio"
}

// mediaOf is the media of a published track, and false for a kind no mute
// applies to.
func mediaOf(kind webrtc.RTPCodecType) (media, bool) {
	switch kind {
	case webrtc.RTPCodecTypeAudio:
		return mediaAudio, true
	case webrtc.RTPCodecTypeVideo:
		return mediaVideo, true
	}
	return 0, false
}

// mutedFlag is the peer's flag for m, read per RTP packet by forwardRTP.
func (p *Peer) mutedFlag(m media) *atomic.Bool {
	if m == mediaVideo {
		return &p.videoMuted
	}
	return &p.muted
}

// muteLogOf is the server's log of the mutes of m.
func (s *Server) muteLogOf(m media) *muteLog {
	if m == mediaVideo {
		return s.videoMutes
	}
	return s.mutes
}
