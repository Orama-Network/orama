package sfu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"github.com/Orama-Network/orama/core/pkg/turn"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// The HTTP server's timeouts. They bound a slow or silent client of the control
// and health routes. They do not bound a signalling socket: net/http clears the
// connection's deadlines when the handler hijacks it for the WebSocket upgrade,
// so neither ReadTimeout nor a write timeout ever fires on an upgraded socket;
// its liveness is the signalling loop's own. WriteTimeout is left unset for the
// same reason a reader of this file would otherwise wonder about it.
const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpIdleTimeout       = 2 * time.Minute
)

// Server is the SFU HTTP server providing WebSocket signaling and a health endpoint.
// It binds only to a WireGuard IP — never exposed publicly.
type Server struct {
	config      *Config
	roomManager *RoomManager
	logger      *zap.Logger
	httpServer  *http.Server
	upgrader    websocket.Upgrader
	draining    bool
	drainingMu  sync.RWMutex

	// controlKey authenticates the gateways of this namespace (ctrlauth): the
	// join tickets they present and the control requests they make.
	controlKey []byte
	// kicks remembers recent kicks so a join already in flight is refused.
	kicks *kickLog
	// mutes and videoMutes remember recent mutes of audio and of video, so a
	// join with a stale ticket takes the current state.
	mutes      *muteLog
	videoMutes *muteLog
	// replays refuses a control request whose MAC was already served.
	replays *ctrlauth.ReplayGuard

	// refreshAfter waits out the interval before a TURN credential refresh.
	// A field so a test can drive the refresh without racing the package timer.
	refreshAfter func(time.Duration) <-chan time.Time
	// expireAfter waits out the time left of a peer's admission (join.go); a
	// field so a test can end an admission without waiting.
	expireAfter func(time.Duration) <-chan time.Time
}

// NewServer creates a new SFU server.
func NewServer(cfg *Config, logger *zap.Logger) (*Server, error) {
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("invalid SFU config: %v", errs[0])
	}

	key, err := ctrlauth.Key(cfg.TURNSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid SFU config: %w", err)
	}

	s := &Server{
		config:       cfg,
		roomManager:  NewRoomManager(cfg, logger),
		logger:       logger.With(zap.String("component", "sfu"), zap.String("namespace", cfg.Namespace)),
		controlKey:   key,
		kicks:        newKickLog(),
		mutes:        newMuteLog(),
		videoMutes:   newMuteLog(),
		replays:      ctrlauth.NewReplayGuard(ctrlauth.DefaultReplayCapacity),
		refreshAfter: time.After,
		expireAfter:  time.After,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true }, // Gateway handles auth
		},
	}

	s.roomManager.reporter = newReporter(key, s.logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/signal", s.handleSignal)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc(ctrlauth.KickPath, s.handleKick)
	mux.HandleFunc(ctrlauth.KickDevicePath, s.handleKickDevice)
	mux.HandleFunc(ctrlauth.MutePath, s.handleMute(mediaAudio))
	mux.HandleFunc(ctrlauth.VideoMutePath, s.handleMute(mediaVideo))

	s.httpServer = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	return s, nil
}

// ListenAndServe starts the HTTP server. Blocks until the server is stopped.
func (s *Server) ListenAndServe() error {
	s.logger.Info("SFU server starting",
		zap.String("addr", s.config.ListenAddr),
		zap.String("namespace", s.config.Namespace))
	return s.httpServer.ListenAndServe()
}

// Drain initiates graceful drain: notifies all peers, waits, then closes.
func (s *Server) Drain(timeout time.Duration) {
	s.drainingMu.Lock()
	s.draining = true
	s.drainingMu.Unlock()

	s.logger.Info("SFU draining started", zap.Duration("timeout", timeout))

	// Notify all peers
	s.roomManager.mu.RLock()
	for _, room := range s.roomManager.rooms {
		room.broadcastMessage("", NewServerMessage(MessageTypeServerDraining, &ServerDrainingData{
			Reason:    "server shutting down",
			TimeoutMs: int(timeout.Milliseconds()),
		}))
	}
	s.roomManager.mu.RUnlock()

	// Wait for timeout, then force close
	<-timeAfter(timeout)
}

// Close shuts down the SFU server.
func (s *Server) Close() error {
	s.logger.Info("SFU server shutting down")
	s.roomManager.CloseAll()
	s.roomManager.reporter.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// handleHealth reports readiness (503 while draining) and the room count.
// With ?room=<id> it also reports whether that room has participants here: the
// namespace gateway asks this to find the SFU that already hosts a room
// (website/src/docs/operator/webrtc-operations.mdx#room-placement). A room that is empty, or absent, is false.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.drainingMu.RLock()
	draining := s.draining
	s.drainingMu.RUnlock()

	hasRoom := s.roomManager.HasParticipants(r.URL.Query().Get("room"))

	if draining {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"draining","rooms":%d,"hasRoom":%t}`, s.roomManager.RoomCount(), hasRoom)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","rooms":%d,"hasRoom":%t}`, s.roomManager.RoomCount(), hasRoom)
}

// signalingLoop reads signaling messages from the WebSocket until disconnect.
func (s *Server) signalingLoop(peer *Peer, room *Room) {
	defer room.RemovePeer(peer.ID)

	for {
		_, msgBytes, err := peer.conn.ReadMessage()
		if err != nil {
			s.logger.Debug("WebSocket read error", zap.String("peer_id", peer.ID), zap.Error(err))
			return
		}

		var msg ClientMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			peer.SendMessage(NewErrorMessage("invalid_message", "malformed JSON"))
			continue
		}

		if (msg.Type == MessageTypeOffer || msg.Type == MessageTypeICECandidate) && !peer.limiter.allowSignal() {
			s.closeRateLimited(peer, fmt.Errorf("more than %d %s messages at %d per second: %w",
				signalBurst, msg.Type, signalRefillPerSecond, ErrSignalRateLimited))
			return
		}

		switch msg.Type {
		case MessageTypeOffer:
			var data OfferData
			if err := json.Unmarshal(msg.Data, &data); err != nil {
				peer.SendMessage(NewErrorMessage("invalid_offer", err.Error()))
				continue
			}
			if err := peer.HandleOffer(data.SDP); errors.Is(err, ErrSignalRateLimited) {
				s.closeRateLimited(peer, err)
				return
			} else if err != nil {
				s.logger.Error("Failed to handle offer", zap.String("peer_id", peer.ID), zap.Error(err))
				peer.SendMessage(NewErrorMessage("offer_failed", err.Error()))
			}

		case MessageTypeAnswer:
			var data AnswerData
			if err := json.Unmarshal(msg.Data, &data); err != nil {
				peer.SendMessage(NewErrorMessage("invalid_answer", err.Error()))
				continue
			}
			if err := peer.HandleAnswer(data.SDP); err != nil {
				s.logger.Error("Failed to handle answer", zap.String("peer_id", peer.ID), zap.Error(err))
			}

		case MessageTypeICECandidate:
			var data ICECandidateData
			if err := json.Unmarshal(msg.Data, &data); err != nil {
				peer.SendMessage(NewErrorMessage("invalid_candidate", err.Error()))
				continue
			}
			if err := peer.HandleICECandidate(&data); err != nil {
				s.logger.Error("Failed to handle ICE candidate", zap.String("peer_id", peer.ID), zap.Error(err))
			}

		case MessageTypeAudioState, MessageTypeVideoState:
			s.handleState(peer, room, msg)

		case MessageTypeLeave:
			s.logger.Info("Peer leaving", zap.String("peer_id", peer.ID))
			return

		default:
			peer.SendMessage(NewErrorMessage("unknown_message", fmt.Sprintf("unknown message type: %s", msg.Type)))
		}
	}
}

// closeRateLimited tells a peer that exceeded its signaling allowance why it is
// being disconnected; the caller then leaves the signaling loop, which removes
// the peer.
func (s *Server) closeRateLimited(peer *Peer, cause error) {
	s.logger.Warn("Closing peer for exceeding its signaling rate limit", zap.String("peer_id", peer.ID), zap.Error(cause))
	peer.SendMessage(NewErrorMessage(rateLimitedCode, cause.Error()))
}

// sendTURNCredentials sends TURN server credentials to a peer as msgType:
// turn-credentials on join, refresh-credentials on the 80%-of-TTL refresh.
func (s *Server) sendTURNCredentials(peer *Peer, msgType MessageType) {
	ttl := time.Duration(s.config.TURNCredentialTTL) * time.Second
	username, password, err := turn.GenerateCredentials(s.config.TURNSecret, s.config.Namespace, peer.UserID, ttl)
	if err != nil {
		s.logger.Error("No TURN credential for a peer the gateway named nobody for", zap.String("peer_id", peer.ID), zap.Error(err))
		return
	}

	peer.SendMessage(NewServerMessage(msgType, &TURNCredentialsData{
		Username: username,
		Password: password,
		TTL:      s.config.TURNCredentialTTL,
		URIs:     turnURIs(s.config.TURNServers),
	}))
}

// credentialRefreshLoop sends fresh TURN credentials at 80% of TTL until the
// peer is gone.
func (s *Server) credentialRefreshLoop(peer *Peer) {
	refreshInterval := time.Duration(float64(s.config.TURNCredentialTTL)*0.8) * time.Second

	for {
		select {
		case <-peer.done:
			return
		case <-s.refreshAfter(refreshInterval):
		}

		if peer.closed.Load() {
			return
		}

		s.sendTURNCredentials(peer, MessageTypeRefreshCredentials)
		s.logger.Debug("Refreshed TURN credentials", zap.String("peer_id", peer.ID))
	}
}

func mustMarshal(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}
