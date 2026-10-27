package webrtc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"github.com/gorilla/websocket"
)

const testSink = "http://10.0.0.3:10004"

// signalAs sends a ?room= join as sub (on device) through h, with the spoofed
// ticket header a client could set, and returns the request as the proxy saw it.
func signalAs(h *WebRTCHandlers, room, sub, device string) (*http.Request, *httptest.ResponseRecorder) {
	req := requestWithNamespace("GET", "/v1/webrtc/signal?"+url.Values{"room": {room}}.Encode(), "ns")
	req = asCaller(req, sub, device)
	req.Header.Set(ctrlauth.TicketHeader, "forged-by-the-client")
	w := httptest.NewRecorder()
	h.SignalHandler(w, req)
	return req, w
}

func openTicketOf(t *testing.T, req *http.Request) ctrlauth.Ticket {
	t.Helper()
	key, err := ctrlauth.Key(testTURNSecret)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := ctrlauth.OpenTicket(key, req.Header.Get(ctrlauth.TicketHeader), time.Now())
	if err != nil {
		t.Fatalf("the SFU would refuse this ticket: %v", err)
	}
	return tk
}

func rpcCode(t *testing.T, w *httptest.ResponseRecorder) httputil.RPCErrorCode {
	t.Helper()
	var env httputil.RPCErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Error == nil {
		t.Fatalf("not a typed error: %s", w.Body)
	}
	return env.Error.Code
}

func TestSignalHandler_ticketCarriesTheAuthenticatedIdentity(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	h.SetEventSink(testSink, nil)

	req, w := signalAs(h, "r1", "0xalice", "device-thumbprint")

	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	tk := openTicketOf(t, req)
	if tk.UserID != "0xalice" || tk.DeviceID != "device-thumbprint" || tk.Room != "r1" || tk.Namespace != "ns" || tk.EventSink != testSink || tk.Muted {
		t.Fatalf("ticket = %+v", tk)
	}
	if req.Header.Get(ctrlauth.TicketHeader) == "forged-by-the-client" {
		t.Fatal("the client's own ticket header was forwarded to the SFU")
	}
	if ttl := time.Until(time.Unix(tk.Expires, 0)); ttl <= 0 || ttl > ctrlauth.TicketTTL {
		t.Fatalf("ticket lives %s, want at most %s", ttl, ctrlauth.TicketTTL)
	}
}

func TestSignalJoinFrame_ticketIdentityIgnoresTheFramesUserID(t *testing.T) {
	sfus, dir := threeSFUs(t)
	h := joinGateway(t, dir)
	h.SetEventSink(testSink, nil)
	c := dialGateway(t, gatewayServer(t, h), "")

	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"join","data":{"roomId":"r1","userId":"someone-else"}}`))
	if got := readText(t, c); got != `{"type":"welcome"}` {
		t.Fatalf("reply = %s", got)
	}

	for _, s := range sfus {
		select {
		case token := <-s.tickets:
			req := &http.Request{Header: http.Header{}}
			req.Header.Set(ctrlauth.TicketHeader, token)
			tk := openTicketOf(t, req)
			if tk.UserID != testUser || tk.Room != "r1" {
				t.Fatalf("ticket = %+v, want %s in r1 whatever the frame said", tk, testUser)
			}
			return
		default:
		}
	}
	t.Fatal("no SFU received a ticket")
}

func TestSignalHandler_callerWithoutAValidatedTokenIsRefused(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	req := httptest.NewRequest("GET", "/v1/webrtc/signal?room=r1", nil)
	req = requestWithNamespaceOf(req, "ns") // a namespace, but no claims
	w := httptest.NewRecorder()
	h.SignalHandler(w, req)

	if w.Code != http.StatusUnauthorized || rpcCode(t, w) != httputil.ErrCodeUnauthorized {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if target != "" {
		t.Error("an unauthenticated join was proxied")
	}
}

func TestSignalHandler_gatewayWithoutSecretOrStoreRefuses(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	noSecret := gatewayFor(t, dir, &target)
	noSecret.controlKey = nil
	noStore := gatewayFor(t, dir, &target)
	noStore.admissions = nil

	for name, h := range map[string]*WebRTCHandlers{"no TURN secret": noSecret, "no admission store": noStore} {
		_, w := signalAs(h, "r1", testUser, "")
		if w.Code != http.StatusServiceUnavailable || rpcCode(t, w) != httputil.ErrCodeServiceUnavailable {
			t.Errorf("%s: status %d %s", name, w.Code, w.Body)
		}
	}
	if target != "" {
		t.Error("a join was proxied without a ticket to present")
	}
}

func TestSignalHandler_requiredAdmission(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	store := h.admissions
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	if err := store.SetRequireAdmission(ctx, "ns", true); err != nil {
		t.Fatal(err)
	}
	store.Admit(ctx, "ns", "admitted-room", testUser, "", time.Hour)
	store.Admit(ctx, "ns", "device-room", testUser, "phone", time.Hour)
	store.Admit(ctx, "ns", "short-room", testUser, "", time.Hour)
	store.Admit(ctx, "ns", "kicked-room", testUser, "", time.Hour)
	store.Revoke(ctx, "ns", "kicked-room", testUser)
	store.db.Exec(ctx, `UPDATE webrtc_admissions SET expires_at = 1 WHERE room = 'short-room'`)

	cases := []struct {
		name       string
		room, user string
		device     string
		status     int
		code       httputil.RPCErrorCode
	}{
		{"admitted", "admitted-room", testUser, "", http.StatusOK, ""},
		{"admitted, any device", "admitted-room", testUser, "laptop", http.StatusOK, ""},
		{"never admitted to the room", "other-room", testUser, "", http.StatusForbidden, codeAdmissionRequired},
		{"admitted to another user", "admitted-room", "0xmallory", "", http.StatusForbidden, codeAdmissionRequired},
		{"admission for another device", "device-room", testUser, "laptop", http.StatusForbidden, codeAdmissionRequired},
		{"admission for this device", "device-room", testUser, "phone", http.StatusOK, ""},
		{"expired", "short-room", testUser, "", http.StatusForbidden, codeAdmissionExpired},
		{"revoked", "kicked-room", testUser, "", http.StatusForbidden, codeAdmissionRevoked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target = ""
			_, w := signalAs(h, c.room, c.user, c.device)
			if w.Code != c.status {
				t.Fatalf("status %d %s, want %d", w.Code, w.Body, c.status)
			}
			if c.code != "" {
				if got := rpcCode(t, w); got != c.code {
					t.Fatalf("code = %s, want %s", got, c.code)
				}
				if target != "" {
					t.Fatal("a refused join was proxied to an SFU")
				}
			}
		})
	}
}

func TestSignalJoinFrame_requiredAdmissionRefusesWithATypedFrame(t *testing.T) {
	sfus, dir := threeSFUs(t)
	h := joinGateway(t, dir)
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.SetRequireAdmission(ctx, "ns", true)
	h.admissions.Admit(ctx, "ns", "admitted-room", testUser, "", time.Hour)
	srv := gatewayServer(t, h)

	c := dialGateway(t, srv, "")
	_ = c.WriteMessage(websocket.TextMessage, []byte(joinJSON("secret-room")))
	if got := readText(t, c); !strings.Contains(got, `"code":"admission_required"`) {
		t.Fatalf("reply = %s, want an admission_required error frame", got)
	}
	for _, s := range sfus {
		if j := nextJoin(s); j != "" {
			t.Fatalf("a refused join reached an SFU: %s", j)
		}
	}

	c = dialGateway(t, srv, "")
	_ = c.WriteMessage(websocket.TextMessage, []byte(joinJSON("admitted-room")))
	if got := readText(t, c); got != `{"type":"welcome"}` {
		t.Fatalf("an admitted user: reply = %s", got)
	}
}

func TestSignalHandler_notRequiredKeepsOldBehaviourButMuteHolds(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)

	req, w := signalAs(h, "open-room", "0xanyone", "")
	if w.Code != http.StatusOK || openTicketOf(t, req).Muted {
		t.Fatalf("a namespace that does not require admission: status %d", w.Code)
	}

	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.Admit(ctx, "ns", "open-room", "0xanyone", "", time.Hour)
	h.admissions.SetMuted(ctx, "ns", "open-room", "0xanyone", MediaAudio, true)
	req, w = signalAs(h, "open-room", "0xanyone", "")
	if w.Code != http.StatusOK || !openTicketOf(t, req).Muted {
		t.Fatalf("a muted user's rejoin: status %d, want a ticket that says muted", w.Code)
	}
}

func TestSignalHandler_ticketCarriesTheAdmissionGenerationAndAReadmissionOutranksTheKick(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.SetRequireAdmission(ctx, "ns", true)
	h.admissions.Admit(ctx, "ns", "r1", testUser, "", time.Hour)

	req, w := signalAs(h, "r1", testUser, "")
	if w.Code != http.StatusOK || openTicketOf(t, req).AdmitGen != 1 {
		t.Fatalf("first admission: status %d ticket %+v, want generation 1", w.Code, openTicketOf(t, req))
	}
	kicked, err := h.admissions.Revoke(ctx, "ns", "r1", testUser)
	if err != nil || kicked != 1 {
		t.Fatalf("Revoke = %d, %v, want 1", kicked, err)
	}
	h.admissions.Admit(ctx, "ns", "r1", testUser, "", time.Hour)
	req, w = signalAs(h, "r1", testUser, "")
	if w.Code != http.StatusOK || openTicketOf(t, req).AdmitGen <= kicked {
		t.Fatalf("after re-admission: status %d ticket %+v, want a generation above %d", w.Code, openTicketOf(t, req), kicked)
	}
}

func TestSignalHandler_aNamespaceWithoutAdmissionIssuesGenerationZero(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.Admit(ctx, "ns", "r1", testUser, "", time.Hour)
	req, w := signalAs(h, "r1", testUser, "")
	if tk := openTicketOf(t, req); w.Code != http.StatusOK || tk.AdmitGen != 0 || tk.AdmitExp != 0 {
		t.Fatalf("status %d ticket %+v, want no admission bound", w.Code, tk)
	}
}

func TestSignalHandler_unreadableAdmissionRecordsAreRetryable(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	if _, err := h.admissions.db.Exec(httptest.NewRequest("GET", "/", nil).Context(), `DROP TABLE webrtc_admissions`); err != nil {
		t.Fatal(err)
	}

	_, w := signalAs(h, "r1", testUser, "")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d %s, want 503: a store that cannot be read must not admit", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"retryable":true`) || target != "" {
		t.Fatalf("body %s, target %q", w.Body, target)
	}
}
