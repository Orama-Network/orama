//go:build e2e_fleet

package webrtc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/features/internal/services"
	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// Admission, identity, membership events, kick and mute (website/src/docs/developer/webrtc.mdx#admission).
const (
	rtcFixtureDir = "testdata/rtcfn"
	rtcFunction   = "e2e-rtcadmin"
	pathConfig    = "/v1/webrtc/config"
	pathPubsubWS  = "/v1/pubsub/ws"
	fixturePerm   = 0o644
	// errBodyBytes bounds the refusal body read off a failed handshake.
	errBodyBytes = 4096
	// admitTTLSeconds is long enough for a test; shortTTLSeconds is the expiry test's.
	admitTTLSeconds = 300
	shortTTLSeconds = 2
	// eventBudget bounds a membership event reaching a subscriber.
	eventBudget = 30 * time.Second
	// silenceWindow is how long a muted publisher is watched to receive nothing.
	silenceWindow = 6 * time.Second
	// expiryBudget bounds an admission with a short ttl running out.
	expiryBudget = time.Minute
	// settleEvery spaces the checks that in-flight packets have arrived; publishEvery
	// spaces the publishing of a muted peer's bursts.
	settleEvery  = 500 * time.Millisecond
	publishEvery = 200 * time.Millisecond
	// roomPrefix keeps the rooms of this test apart from the others'.
	roomPrefix = "e2e-adm-"
)

// rtcUser is a runtime member of the namespace signed in at the index gateway.
type rtcUser struct {
	subject string
	token   string
}

func newRTCUser(t *testing.T, n *ns.Namespace, role string) rtcUser {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	n.CLI.MustOK(t, "members", "add", w.Address(), "--role", role)
	s, err := harness.GW(t).For(t).SignIn(t.Context(), w, n.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rtcUser{subject: s.Subject, token: s.AccessToken}
}

// deployRTCFunction builds the fixture with `orama function deploy` and removes
// it at cleanup.
func deployRTCFunction(t *testing.T, n *ns.Namespace) {
	t.Helper()
	deployFixture(t, n, rtcFixtureDir, rtcFunction)
}

// deployFixture deploys the TinyGo function in fixtureDir as name and removes it
// at cleanup.
func deployFixture(t *testing.T, n *ns.Namespace, fixtureDir, name string) {
	t.Helper()
	if _, err := exec.LookPath("tinygo"); err != nil {
		harness.SkipNotApplicable(t, "tinygo is not on the runner's PATH; `orama function deploy` builds functions with it")
	}
	dir := t.TempDir()
	for _, file := range []string{"function.go", "go.mod"} {
		src, err := os.ReadFile(filepath.Join(fixtureDir, file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), src, fixturePerm); err != nil {
			t.Fatal(err)
		}
	}
	yaml := "name: " + name + "\npublic: false\nmemory: 64\ntimeout: 30\n"
	if err := os.WriteFile(filepath.Join(dir, "function.yaml"), []byte(yaml), fixturePerm); err != nil {
		t.Fatal(err)
	}
	n.CLI.MustOK(t, "function", "deploy", dir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		defer cancel()
		if res, err := n.CLI.Run(ctx, "function", "delete", name, "--force"); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: failed to delete function %s: %v %s", name, err, res.Stderr)
		}
	})
}

// callRTC runs the fixture with op as bearer and returns the decoded reply.
func callRTC(t *testing.T, fx *fixture, bearer string, op map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	r := fx.c.MustSend(t, gw.Req{Method: http.MethodPost, Path: "/v1/functions/" + rtcFunction + "/invoke",
		Query: url.Values{"namespace": {fx.n.Name}}, Bearer: bearer,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: raw})
	r.Expect(t, http.StatusOK)
	var out map[string]any
	if err := r.Decode(&out); err != nil {
		t.Fatal(err)
	}
	if e, bad := out["error"]; bad {
		t.Fatalf("%v op failed: %v", op["op"], e)
	}
	return out
}

func admit(t *testing.T, fx *fixture, by string, room, user string, ttl int) {
	t.Helper()
	out := callRTC(t, fx, by, map[string]any{"op": "admit", "room": room, "user": user, "ttl": ttl})
	if out["room"] != room || out["user_id"] != user {
		t.Fatalf("admit answered %v", out)
	}
}

func setRequireAdmission(t *testing.T, fx *fixture, admin string, on bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]bool{"require_admission": on})
	fx.c.MustSend(t, gw.Req{Method: http.MethodPut, Path: pathConfig, Bearer: admin, Body: body,
		Header: http.Header{"Content-Type": {"application/json"}}}).Expect(t, http.StatusOK)
	r := fx.c.MustSend(t, gw.Req{Path: pathConfig, Bearer: admin}).Expect(t, http.StatusOK)
	var got struct {
		Require bool `json:"require_admission"`
	}
	if err := r.Decode(&got); err != nil || got.Require != on {
		t.Fatalf("GET %s = %s (%v), want require_admission=%v", pathConfig, r.Body, err, on)
	}
}

// refusal dials the signalling socket with ?room= and returns the HTTP status
// and typed error code the gateway refused it with.
func refusal(t *testing.T, fx *fixture, token, room string) (int, string) {
	t.Helper()
	conn, resp, err := fx.c.DialWS(t.Context(), services.SignalPath+"?"+url.Values{"room": {room}}.Encode(), token, nil)
	if err == nil {
		conn.Close()
		t.Fatalf("the join of room %q was not refused", room)
	}
	if resp == nil {
		t.Fatalf("dial: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyBytes))
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	return resp.StatusCode, env.Error.Code
}

// expectRefused fails unless the join is refused with 403 and code.
func expectRefused(t *testing.T, fx *fixture, token, room, code string) {
	t.Helper()
	if status, got := refusal(t, fx, token, room); status != http.StatusForbidden || got != code {
		t.Fatalf("join of %q: HTTP %d code %q, want 403 %s", room, status, got, code)
	}
}

// joinFrameRefusal joins without ?room= and returns the code of the error frame.
func joinFrameRefusal(t *testing.T, fx *fixture, token, room string) string {
	t.Helper()
	conn, _, err := fx.c.DialWS(t.Context(), services.SignalPath, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, _ := json.Marshal(map[string]string{"roomId": room})
	if err := conn.WriteJSON(services.SignalMsg{Type: services.MsgJoin, Data: data}); err != nil {
		t.Fatal(err)
	}
	var m services.SignalMsg
	_ = conn.SetReadDeadline(time.Now().Add(dialBudget))
	if err := conn.ReadJSON(&m); err != nil {
		t.Fatal(err)
	}
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(m.Data, &e)
	if m.Type != services.MsgError {
		t.Fatalf("frame = %s %s, want an error frame", m.Type, m.Data)
	}
	return e.Code
}

// membership is one of the platform's events on _orama/webrtc/<room>.
type membership struct {
	Orama    string `json:"_orama"`
	Room     string `json:"room"`
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id"`
	PeerID   string `json:"peer_id"`
	Reason   string `json:"reason"`
}

// watchMembership subscribes to the room's membership topic and returns a
// function that waits for the next event of type from user.
func watchMembership(t *testing.T, fx *fixture, token, room string) func(typ, user string) membership {
	t.Helper()
	topic := "_orama/webrtc/" + room
	conn, resp, err := fx.c.DialWS(t.Context(), pathPubsubWS+"?"+url.Values{"topic": {topic}}.Encode(), token, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("subscribing to %s (HTTP %d): %v", topic, status, err)
	}
	t.Cleanup(func() { conn.Close() })
	events := make(chan membership, frameBuffer)
	go func() {
		for {
			var env struct {
				Data string `json:"data"`
			}
			if err := conn.ReadJSON(&env); err != nil {
				close(events)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(env.Data)
			var m membership
			if err == nil && json.Unmarshal(raw, &m) == nil {
				events <- m
			}
		}
	}()
	return func(typ, user string) membership {
		t.Helper()
		var got membership
		eventually.Require(t, time.Second, eventBudget, "a "+typ+" event for "+user, func() (bool, error) {
			for {
				select {
				case m, ok := <-events:
					if !ok {
						return false, eventually.Stop(io.ErrClosedPipe)
					}
					if m.Orama == typ && strings.EqualFold(m.UserID, user) {
						got = m
						return true, nil
					}
				default:
					return false, nil
				}
			}
		})
		return got
	}
}

const frameBuffer = 256

// waitFrame reads p's protocol frames until match, or the budget ends.
func waitFrame(t *testing.T, p *services.RTCPeer, what string, match func(services.SignalMsg) bool) {
	t.Helper()
	eventually.Require(t, 500*time.Millisecond, eventBudget, what, func() (bool, error) {
		for {
			select {
			case m, ok := <-p.Messages:
				if !ok {
					return false, eventually.Stop(io.ErrClosedPipe)
				}
				if match(m) {
					return true, nil
				}
			default:
				return false, nil
			}
		}
	})
}

// rtcJoin joins room as u through the ?room= path and starts media.
func rtcJoin(t *testing.T, fx *fixture, u rtcUser, room string, publish bool) *services.RTCPeer {
	t.Helper()
	p, err := services.JoinRoom(t.Context(), fx.c, u.token, room, "a-name-the-client-chose")
	if err != nil {
		t.Fatalf("%s joining %s: %v", u.subject, room, err)
	}
	t.Cleanup(p.Close)
	if err := p.Start(publish); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAdmission_identityIsTheAuthenticatedUser: whatever userId the join frame
// carries, the room knows the peer by the user the gateway authenticated
// (website/src/docs/developer/webrtc.mdx#identity), and a namespace that never turned admission on
// admits everyone as before.
func TestAdmission_identityIsTheAuthenticatedUser(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	waitPlaced(t, fx)
	u := newRTCUser(t, fx.n, "runtime")

	p := rtcJoin(t, fx, u, roomPrefix+"identity-"+fx.n.Name, false)

	if len(p.Participants) != 1 || !strings.EqualFold(p.Participants[0].UserID, u.subject) || p.Participants[0].UserID == "a-name-the-client-chose" {
		t.Fatalf("participants = %+v, want the authenticated subject %s and not the name in the frame", p.Participants, u.subject)
	}
}

// TestAdmission_lifecycle follows one namespace through requiring admission,
// admitting, the membership events, a kick, an expiry, the audio state and a
// mute (website/src/docs/developer/webrtc.mdx#admission, #membership-events, #kick-and-mute).
func TestAdmission_lifecycle(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	waitPlaced(t, fx)
	deployRTCFunction(t, fx.n)
	admin := newRTCUser(t, fx.n, "admin")
	alice, bob := newRTCUser(t, fx.n, "runtime"), newRTCUser(t, fx.n, "runtime")
	room := roomPrefix + "calls-" + fx.n.Name
	caller := alice.token // any member may invoke the fixture; the host call acts for the namespace

	t.Run("requiring admission refuses a user nobody admitted", func(t *testing.T) {
		setRequireAdmission(t, fx, admin.token, true)
		expectRefused(t, fx, alice.token, room, "WEBRTC_ADMISSION_REQUIRED")
		if code := joinFrameRefusal(t, fx, alice.token, room); code != "admission_required" {
			t.Fatalf("join-frame path: code %q, want admission_required", code)
		}
	})

	watch := watchMembership(t, fx, admin.token, room)

	t.Run("an admitted user joins, only to the room and as the user it was issued for", func(t *testing.T) {
		admit(t, fx, caller, room, alice.subject, admitTTLSeconds)
		expectRefused(t, fx, alice.token, room+"-other", "WEBRTC_ADMISSION_REQUIRED")
		expectRefused(t, fx, bob.token, room, "WEBRTC_ADMISSION_REQUIRED")

		p := rtcJoin(t, fx, alice, room, true)
		if len(p.Participants) != 1 || !strings.EqualFold(p.Participants[0].UserID, alice.subject) {
			t.Fatalf("participants = %+v", p.Participants)
		}
		if ev := watch("webrtc.join", alice.subject); ev.Room != room || ev.PeerID != p.PeerID {
			t.Fatalf("join event = %+v, want room %s peer %s", ev, room, p.PeerID)
		}
		p.Close()
		if ev := watch("webrtc.leave", alice.subject); ev.PeerID != p.PeerID || ev.Reason != "left" {
			t.Fatalf("leave event = %+v", ev)
		}
	})

	t.Run("a kick closes the connection and blocks the rejoin", func(t *testing.T) {
		p := rtcJoin(t, fx, alice, room, false)
		watch("webrtc.join", alice.subject)

		callRTC(t, fx, caller, map[string]any{"op": "kick", "room": room, "user": alice.subject})

		waitFrame(t, p, "the kick to close the socket", func(m services.SignalMsg) bool { return m.Type == services.MsgKicked })
		if ev := watch("webrtc.leave", alice.subject); ev.Reason != "kicked" {
			t.Fatalf("leave event = %+v, want reason kicked", ev)
		}
		expectRefused(t, fx, alice.token, room, "WEBRTC_ADMISSION_REVOKED")
	})

	t.Run("an expired admission is refused as expired", func(t *testing.T) {
		admit(t, fx, caller, room, alice.subject, shortTTLSeconds)
		eventually.Require(t, time.Second, expiryBudget, "the admission to expire", func() (bool, error) {
			conn, resp, err := fx.c.DialWS(t.Context(), services.SignalPath+"?"+url.Values{"room": {room}}.Encode(), alice.token, nil)
			if err == nil {
				conn.Close() // still inside its ttl
				return false, nil
			}
			if resp == nil {
				return false, err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyBytes))
			if !strings.Contains(string(body), "WEBRTC_ADMISSION_EXPIRED") {
				return false, eventually.Stop(fmt.Errorf("HTTP %d %s, want WEBRTC_ADMISSION_EXPIRED", resp.StatusCode, body))
			}
			return true, nil
		})
	})

	t.Run("audio-state and video-state reach the room; a malformed one is an error, never unknown_message", func(t *testing.T) {
		admit(t, fx, caller, room, alice.subject, admitTTLSeconds)
		admit(t, fx, caller, room, bob.subject, admitTTLSeconds)
		a, b := rtcJoin(t, fx, alice, room, false), rtcJoin(t, fx, bob, room, false)

		if err := a.SendState(services.MsgAudioState, map[string]bool{"enabled": false}); err != nil {
			t.Fatal(err)
		}
		waitFrame(t, b, "alice's audio state", func(m services.SignalMsg) bool {
			var s struct {
				UserID  string `json:"userId"`
				Kind    string `json:"kind"`
				Enabled bool   `json:"enabled"`
			}
			return m.Type == services.MsgParticipantState && json.Unmarshal(m.Data, &s) == nil &&
				strings.EqualFold(s.UserID, alice.subject) && s.Kind == "audio" && !s.Enabled
		})

		if err := a.SendState(services.MsgVideoState, map[string]string{}); err != nil {
			t.Fatal(err)
		}
		waitFrame(t, a, "invalid_state", func(m services.SignalMsg) bool {
			return m.Type == services.MsgError && strings.Contains(string(m.Data), "invalid_state")
		})
	})

	t.Run("a mute stops the user's audio on the server, through a rejoin, until unmuted", func(t *testing.T) {
		mutedRoom := roomPrefix + "mute-" + fx.n.Name
		admit(t, fx, caller, mutedRoom, alice.subject, admitTTLSeconds)
		admit(t, fx, caller, mutedRoom, bob.subject, admitTTLSeconds)
		pub, sub := rtcJoin(t, fx, alice, mutedRoom, true), rtcJoin(t, fx, bob, mutedRoom, false)
		waitMedia(t, []*services.RTCPeer{pub}, []*services.RTCPeer{sub})

		callRTC(t, fx, caller, map[string]any{"op": "mute", "room": mutedRoom, "user": alice.subject, "muted": true})
		expectSilence(t, pub, sub)

		pub.Close()
		pub = rtcJoin(t, fx, alice, mutedRoom, true) // the mute holds against a client that starts over
		expectSilence(t, pub, sub)

		callRTC(t, fx, caller, map[string]any{"op": "mute", "room": mutedRoom, "user": alice.subject, "muted": false})
		before := sub.Received()
		eventually.Require(t, time.Second, mediaBudget, "audio to flow again after the unmute", func() (bool, error) {
			if err := pub.Publish(); err != nil {
				return false, err
			}
			return sub.Received() > before, nil
		})
	})

	t.Run("a video mute stops the user's video on the server, not their audio, through a rejoin, until unmuted", func(t *testing.T) {
		videoMuteHolds(t, fx, caller, alice, bob)
	})

	t.Run("a device admitted by its session's device id joins, another device of the account does not, and a device kick removes that device alone", func(t *testing.T) {
		deviceAdmission(t, fx, caller, admin.token)
	})

	t.Run("a device admitted with its mutes has nothing forwarded from its first packet, with no mute call after the admission", func(t *testing.T) {
		admittedMuted(t, fx, caller, bob)
	})
}

// expectSilence publishes from pub for silenceWindow and fails if sub receives
// audio, once the packets already in flight when the mute landed have arrived.
func expectSilence(t *testing.T, pub, sub *services.RTCPeer) {
	t.Helper()
	last := int64(-1)
	eventually.Require(t, settleEvery, mediaBudget, "the packets in flight to arrive", func() (bool, error) {
		got := sub.Received()
		settled := got == last
		last = got
		return settled, nil
	})
	before := sub.Received()
	leaked := false
	err := eventually.Poll(t.Context(), publishEvery, silenceWindow, "a muted publisher to stay silent", func() (bool, error) {
		if err := pub.Publish(); err != nil {
			return false, eventually.Stop(err)
		}
		leaked = sub.Received() > before
		return leaked, nil
	})
	var timeout *eventually.TimeoutError
	if err != nil && !errors.As(err, &timeout) {
		t.Fatal(err)
	}
	if leaked {
		t.Fatalf("a muted publisher's audio still reached the room: %d packets", sub.Received()-before)
	}
}
