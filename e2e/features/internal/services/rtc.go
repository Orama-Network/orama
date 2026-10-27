//go:build e2e_fleet

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/Orama-Network/orama/e2e/harness/gw"
)

// The SFU's signalling protocol (core/pkg/sfu/signaling.go), reached through
// the namespace gateway's /v1/webrtc/signal (website/src/docs/developer/webrtc.mdx#signaling-messages).
const (
	SignalPath        = "/v1/webrtc/signal"
	MsgJoin           = "join"
	MsgOffer          = "offer"
	MsgAnswer         = "answer"
	MsgCandidate      = "ice-candidate"
	MsgWelcome        = "welcome"
	MsgTURNCreds      = "turn-credentials"
	MsgRefreshCreds   = "refresh-credentials"
	MsgServerDraining = "server-draining"
	MsgError          = "error"
	// MsgParticipantJoined tells the room a peer joined, named as the welcome
	// names it; MsgParticipantLeft that one left.
	MsgParticipantJoined = "participant-joined"
	MsgParticipantLeft   = "participant-left"
	// Participant state and moderation (website/src/docs/developer/webrtc.mdx#audio-and-video-state,
	// #kick-and-mute).
	MsgAudioState       = "audio-state"
	MsgVideoState       = "video-state"
	MsgParticipantState = "participant-state"
	MsgKicked           = "kicked"
	// rtcPacketBurst is how many RTP packets one Publish call writes.
	rtcPacketBurst = 50
	opusPayload    = 111
)

// SignalMsg is one signalling frame.
type SignalMsg struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// TURNCreds is what the SFU (or the REST route) hands a client.
type TURNCreds struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	TTL      int      `json:"ttl"`
	URIs     []string `json:"uris"`
}

// RTCPeer is a headless WebRTC client in one SFU room: relay-only ICE
// through the namespace's TURN, a publishing audio track, and a count of the
// RTP packets it receives from the room.
type RTCPeer struct {
	ws       *websocket.Conn
	wsMu     sync.Mutex
	pc       *webrtc.PeerConnection
	track    *webrtc.TrackLocalStaticRTP
	received atomic.Int64
	// receivedVideo counts the RTP packets of video tracks among received.
	receivedVideo atomic.Int64
	seq           uint16
	// video publishing and keyframe feedback (rtc_video.go)
	videoTrack *webrtc.TrackLocalStaticRTP
	videoSeq   uint16
	videoSSRC  atomic.Uint32  // SSRC of the video track received from the room
	plis       atomic.Int64   // PLI/FIR read on the published video track's sender
	Messages   chan SignalMsg // every frame, for tests that watch the protocol
	Creds      TURNCreds
	PeerID     string
	// Participants are who the welcome listed in the room, this peer included,
	// as the SFU names them: by the identity the gateway authenticated.
	Participants []Participant
}

// Participant is one entry of the welcome's participant list.
type Participant struct {
	PeerID   string `json:"peerId"`
	UserID   string `json:"userId"`
	DeviceID string `json:"deviceId"`
}

// JoinRoom opens the signalling socket through c as token, joins room as
// userID, and waits for the welcome and the TURN credentials.
func JoinRoom(ctx context.Context, c *gw.Client, token, room, userID string) (*RTCPeer, error) {
	path := SignalPath + "?" + url.Values{"room": {room}}.Encode()
	ws, _, err := c.DialWS(ctx, path, token, nil)
	if err != nil {
		return nil, fmt.Errorf("open the signalling socket: %w", err)
	}
	p := &RTCPeer{ws: ws, Messages: make(chan SignalMsg, 256)}
	if err := p.send(MsgJoin, map[string]string{"roomId": room, "userId": userID}); err != nil {
		ws.Close()
		return nil, err
	}
	for p.Creds.Username == "" || p.PeerID == "" {
		m, err := p.read()
		if err != nil {
			ws.Close()
			return nil, fmt.Errorf("waiting for welcome and credentials: %w", err)
		}
		switch m.Type {
		case MsgWelcome:
			var w struct {
				PeerID       string        `json:"peerId"`
				Participants []Participant `json:"participants"`
			}
			if err := json.Unmarshal(m.Data, &w); err != nil {
				return nil, fmt.Errorf("welcome: %w", err)
			}
			p.PeerID, p.Participants = w.PeerID, w.Participants
		case MsgTURNCreds:
			if err := json.Unmarshal(m.Data, &p.Creds); err != nil {
				return nil, fmt.Errorf("turn-credentials: %w", err)
			}
		case MsgError:
			return nil, fmt.Errorf("the SFU refused the join: %s", m.Data)
		}
	}
	return p, nil
}

// SendState tells the room whether this peer is sending audio or video: kind
// is MsgAudioState or MsgVideoState, data the frame's payload as the client
// would send it.
func (p *RTCPeer) SendState(kind string, data any) error { return p.send(kind, data) }

// Close leaves the room.
func (p *RTCPeer) Close() {
	if p.pc != nil {
		_ = p.pc.Close()
	}
	_ = p.ws.Close()
}

func (p *RTCPeer) send(typ string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	return p.ws.WriteJSON(SignalMsg{Type: typ, Data: raw})
}

func (p *RTCPeer) read() (SignalMsg, error) {
	var m SignalMsg
	_, raw, err := p.ws.ReadMessage()
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("frame is not JSON: %w", err)
	}
	return m, nil
}

// Start builds a relay-only PeerConnection from the SFU's credentials,
// optionally with a publishing track, and runs the signalling loop.
func (p *RTCPeer) Start(publish bool) error {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers:         []webrtc.ICEServer{{URLs: p.Creds.URIs, Username: p.Creds.Username, Credential: p.Creds.Password}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	if err != nil {
		return fmt.Errorf("create the peer connection: %w", err)
	}
	p.pc = pc
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = p.send(MsgCandidate, c.ToJSON())
		}
	})
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		video := tr.Kind() == webrtc.RTPCodecTypeVideo
		if video {
			p.videoSSRC.Store(uint32(tr.SSRC()))
		}
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			p.received.Add(1)
			if video {
				p.receivedVideo.Add(1)
			}
		}
	})
	go p.loop()
	if !publish {
		return nil
	}
	if err := p.addAudio(); err != nil {
		return err
	}
	return p.sendOffer()
}

// addAudio adds an Opus track to the peer's connection, for its next offer.
func (p *RTCPeer) addAudio() error {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "e2e-"+p.PeerID)
	if err != nil {
		return fmt.Errorf("create the track: %w", err)
	}
	if _, err := p.pc.AddTrack(track); err != nil {
		return fmt.Errorf("add the track: %w", err)
	}
	p.track = track
	return nil
}

// sendOffer makes the peer's own offer and sends it.
func (p *RTCPeer) sendOffer() error {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create the offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set the local offer: %w", err)
	}
	return p.send(MsgOffer, map[string]string{"sdp": offer.SDP})
}

// loop answers the SFU's offers and applies its answers and candidates.
func (p *RTCPeer) loop() {
	for {
		m, err := p.read()
		if err != nil {
			close(p.Messages)
			return
		}
		select {
		case p.Messages <- m:
		default:
		}
		p.handle(m)
	}
}

func (p *RTCPeer) handle(m SignalMsg) {
	var sdp struct {
		SDP string `json:"sdp"`
	}
	switch m.Type {
	case MsgOffer:
		if json.Unmarshal(m.Data, &sdp) != nil || p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp.SDP}) != nil {
			return
		}
		answer, err := p.pc.CreateAnswer(nil)
		if err != nil || p.pc.SetLocalDescription(answer) != nil {
			return
		}
		_ = p.send(MsgAnswer, map[string]string{"sdp": answer.SDP})
	case MsgAnswer:
		if json.Unmarshal(m.Data, &sdp) == nil {
			_ = p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp.SDP})
		}
	case MsgCandidate:
		var c webrtc.ICECandidateInit
		if json.Unmarshal(m.Data, &c) == nil {
			_ = p.pc.AddICECandidate(c)
		}
	}
}

// Publish writes a burst of RTP packets on the track; callers drive it from
// an eventually loop, which paces it.
func (p *RTCPeer) Publish() error {
	if p.track == nil {
		return fmt.Errorf("this peer publishes no track")
	}
	for i := 0; i < rtcPacketBurst; i++ {
		p.seq++
		pkt := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: opusPayload, SequenceNumber: p.seq,
			Timestamp: uint32(p.seq) * 960, SSRC: 0xE2E}, Payload: []byte{0xFC, 0xFF, 0xFE}}
		if err := p.track.WriteRTP(pkt); err != nil {
			return fmt.Errorf("write RTP: %w", err)
		}
	}
	return nil
}

// Received is how many RTP packets arrived from the room.
func (p *RTCPeer) Received() int64 { return p.received.Load() }

// ReceivedVideo and ReceivedAudio split Received by the kind of track the
// packets arrived on.
func (p *RTCPeer) ReceivedVideo() int64 { return p.receivedVideo.Load() }
func (p *RTCPeer) ReceivedAudio() int64 { return p.received.Load() - p.receivedVideo.Load() }

// Connected reports whether ICE reached connected through a relay.
func (p *RTCPeer) Connected() bool {
	return p.pc != nil && p.pc.ICEConnectionState() == webrtc.ICEConnectionStateConnected
}

// RelayCandidates gathers ICE with creds, relay-only, and returns the relay
// candidates' "ip port" pairs: none when the TURN server refuses creds.
func RelayCandidates(ctx context.Context, creds TURNCreds) ([]string, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers:         []webrtc.ICEServer{{URLs: creds.URIs, Username: creds.Username, Credential: creds.Password}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	if err != nil {
		return nil, fmt.Errorf("create the peer connection: %w", err)
	}
	defer pc.Close()
	if _, err := pc.CreateDataChannel("e2e", nil); err != nil {
		return nil, err
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, err
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var out []string
	for _, line := range strings.Split(pc.LocalDescription().SDP, "\n") {
		f := strings.Fields(line)
		if strings.HasPrefix(line, "a=candidate:") && len(f) >= 8 && f[7] == "relay" {
			out = append(out, f[4]+" "+f[5])
		}
	}
	return out, nil
}

// GatherBudget bounds one relay gathering.
const GatherBudget = 45 * time.Second
