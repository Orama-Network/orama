package sfu

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

// wsWriteTimeout bounds one signaling write. A client that stopped reading
// (a stalled TCP connection) would otherwise block the writer - and with it a
// broadcast to the whole room - until the kernel gives up minutes later.
const wsWriteTimeout = 5 * time.Second

var (
	ErrPeerNotInitialized = errors.New("peer connection not initialized")
	ErrPeerClosed         = errors.New("peer is closed")
	ErrWebSocketClosed    = errors.New("websocket connection closed")
)

// Peer represents a participant in a room with a WebRTC PeerConnection.
type Peer struct {
	ID string
	// UserID and DeviceID are what the namespace gateway authenticated for
	// this socket (join.go); the client has no say in either.
	UserID   string
	DeviceID string

	// muted is set when the namespace muted this user: no audio of theirs is
	// forwarded. It is read per RTP packet (forwardRTP), so a client that
	// keeps publishing audio, or modifies itself to, is still silent.
	muted atomic.Bool
	// videoMuted is muted for video: none of the user's video tracks, camera
	// or screen, is forwarded (media.go).
	videoMuted atomic.Bool
	// muteMu makes setting muted or videoMuted and telling the room one step,
	// so the state the room is told last is the state the peer is in.
	muteMu sync.Mutex
	// eventSink is where this peer's membership is reported (ticket.EventSink).
	eventSink string
	// kicked is set when the namespace removed this peer, for the leave event.
	kicked atomic.Bool
	// expired is set when the admission this peer joined on ended, for the leave event.
	expired atomic.Bool

	pc   *webrtc.PeerConnection
	conn *websocket.Conn
	room *Room

	// Negotiation state; see negotiation.go.
	sigMu              sync.Mutex // serializes every offer/answer exchange
	negotiationMu      sync.Mutex // guards the fields below
	negotiationPending bool
	batchingTracks     bool
	iceRestartWanted   bool
	iceRestartOffered  bool
	nextMid            atomic.Int64 // counter behind the SFU's own mids

	// Lifecycle. done is closed by Close, exactly once, which also releases
	// the PeerConnection and the socket; goroutines tied to the peer end on it.
	done           chan struct{}
	closed         atomic.Bool
	closeOnce      sync.Once
	closeErr       error
	disconnectOnce sync.Once
	connMu         sync.Mutex // serializes writes to conn

	// after waits out an interval; a field so a test can drive the TURN
	// credential refresh without waiting hours.
	after func(time.Duration) <-chan time.Time

	// writeTimeout bounds a signaling write; rtcpOut, when set, takes the
	// RTCP written to this peer in place of its PeerConnection. Fields so a
	// test can stall a socket in milliseconds and observe feedback without a
	// live media path.
	writeTimeout time.Duration
	rtcpOut      func([]rtcp.Packet) error

	// limiter bounds the signaling work this peer can cause (ratelimit.go).
	limiter *signalLimiter

	// gatheringState, when set, takes the place of the PeerConnection's ICE
	// gathering state, so a test can interleave a gathering with a TURN
	// credential refresh deterministically.
	gatheringState func() webrtc.ICEGatheringState

	logger  *zap.Logger
	onClose func(*Peer)
}

// NewPeer creates a new peer
func NewPeer(userID string, conn *websocket.Conn, room *Room, logger *zap.Logger) *Peer {
	id := uuid.New().String()
	return &Peer{
		ID:           id,
		UserID:       userID,
		conn:         conn,
		room:         room,
		done:         make(chan struct{}),
		after:        time.After,
		writeTimeout: wsWriteTimeout,
		limiter:      newSignalLimiter(),
		logger:       logger.With(zap.String("peer_id", id)),
	}
}

// InitPeerConnection creates and configures the WebRTC PeerConnection.
func (p *Peer) InitPeerConnection(api *webrtc.API, iceServers []webrtc.ICEServer) error {
	policy := webrtc.ICETransportPolicyRelay // Force TURN relay
	if p.room.allowDirectICE {
		policy = webrtc.ICETransportPolicyAll
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers:         iceServers,
		ICETransportPolicy: policy,
	})
	if err != nil {
		return err
	}
	p.pc = pc

	// pion invokes these handlers from inside its own transports, which
	// pc.Close() waits for: closing from the handler itself would deadlock, so
	// every path that ends the peer runs on its own goroutine.
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		p.logger.Info("ICE state changed", zap.String("state", state.String()))

		switch state {
		case webrtc.ICEConnectionStateDisconnected:
			// Give 15 seconds to reconnect before removing
			go p.handleReconnectTimeout()
		case webrtc.ICEConnectionStateFailed, webrtc.ICEConnectionStateClosed:
			go p.handleDisconnect()
		}
	})

	// ICE candidate generation
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		c := candidate.ToJSON()
		data := &ICECandidateData{Candidate: c.Candidate}
		if c.SDPMid != nil {
			data.SDPMid = *c.SDPMid
		}
		if c.SDPMLineIndex != nil {
			data.SDPMLineIndex = *c.SDPMLineIndex
		}
		if c.UsernameFragment != nil {
			data.UsernameFragment = *c.UsernameFragment
		}
		p.SendMessage(NewServerMessage(MessageTypeICECandidate, data))
	})

	// Incoming tracks from the client
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		p.logger.Info("Track received",
			zap.String("track_id", track.ID()),
			zap.String("kind", track.Kind().String()),
			zap.String("codec", track.Codec().MimeType))

		// Read RTCP feedback (PLI/NACK) in background
		go p.readRTCP(receiver, track)

		// Forward track to all other peers
		p.room.BroadcastTrack(p.ID, track)
	})

	p.initNegotiation()
	return nil
}

// SendMessage sends a signaling message via WebSocket. A write that fails or
// times out leaves the socket unusable, so it disconnects the peer.
func (p *Peer) SendMessage(msg *ServerMessage) error {
	if p.closed.Load() {
		return ErrPeerClosed
	}
	if p.conn == nil {
		return ErrWebSocketClosed
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to encode %s message: %w", msg.Type, err)
	}

	p.connMu.Lock()
	defer p.connMu.Unlock()
	err = p.conn.SetWriteDeadline(time.Now().Add(p.writeTimeout))
	if err == nil {
		err = p.conn.WriteMessage(websocket.TextMessage, data)
	}
	if err != nil {
		go p.handleDisconnect()
		return fmt.Errorf("failed to write %s message to peer %s, disconnecting it: %w", msg.Type, p.ID, err)
	}
	return nil
}

// GetInfo returns public info about this peer
func (p *Peer) GetInfo() ParticipantInfo {
	return ParticipantInfo{PeerID: p.ID, UserID: p.UserID, DeviceID: p.DeviceID}
}

// handleReconnectTimeout waits for ICE to recover from a disconnect and
// removes the peer if it has not.
func (p *Peer) handleReconnectTimeout() {
	select {
	case <-p.done:
		return
	case <-timeAfter(reconnectTimeout):
	}

	if p.pc == nil {
		return
	}
	state := p.pc.ICEConnectionState()
	if state == webrtc.ICEConnectionStateDisconnected || state == webrtc.ICEConnectionStateFailed {
		p.logger.Info("Peer did not reconnect within timeout, removing")
		p.handleDisconnect()
	}
}

// handleDisconnect ends the peer: the room forgets it and Close releases the
// connection, the socket and the goroutines. The room's callback calls Close
// as well; Close being once-only is what makes that safe, and what keeps the
// resources from leaking when the room had already dropped the peer.
func (p *Peer) handleDisconnect() {
	p.disconnectOnce.Do(func() {
		if p.onClose != nil {
			p.onClose(p)
		}
		if err := p.Close(); err != nil {
			p.logger.Warn("Failed to close disconnected peer", zap.Error(err))
		}
	})
}

// Close releases the PeerConnection, the WebSocket and everything tied to
// done. It runs once, whoever calls it first; later calls return its result.
func (p *Peer) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		close(p.done)

		// Not under connMu: a write stuck on a dead socket holds it, and
		// closing the socket is what releases that write.
		if p.conn != nil {
			p.conn.Close()
		}
		if p.pc != nil {
			if err := p.pc.Close(); err != nil {
				p.closeErr = fmt.Errorf("failed to close peer connection of %s: %w", p.ID, err)
			}
		}
	})
	return p.closeErr
}

// OnClose sets the disconnect callback
func (p *Peer) OnClose(fn func(*Peer)) {
	p.onClose = fn
}
