package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/client"
	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

// The device flows end to end, through the handlers, against a migrated
// registry on real SQLite: what decides them is the SQL and the order of the
// checks, and a fake would model both.

type sqlDB struct {
	client.DatabaseClient
	db *sql.DB
}

func (s *sqlDB) Query(ctx context.Context, query string, args ...interface{}) (*client.QueryResult, error) {
	trimmed := bytes.TrimSpace([]byte(query))
	if len(trimmed) == 0 || (trimmed[0] != 'S' && trimmed[0] != 's') {
		res, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		n, _ := res.RowsAffected()
		return &client.QueryResult{Count: 1, LastInsertID: id, RowsAffected: n}, nil
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := &client.QueryResult{}
	for rows.Next() {
		cells := make([]interface{}, len(cols))
		into := make([]interface{}, len(cols))
		for i := range cells {
			into[i] = &cells[i]
		}
		if err := rows.Scan(into...); err != nil {
			return nil, err
		}
		for i, c := range cells {
			if b, ok := c.([]byte); ok {
				cells[i] = string(b)
			}
		}
		out.Rows = append(out.Rows, cells)
	}
	out.Count = int64(len(out.Rows))
	return out, rows.Err()
}

type sqlNet struct {
	client.NetworkClient
	db *sqlDB
}

func (n *sqlNet) Database() client.DatabaseClient { return n.db }

type sqlRqlite struct {
	rqlite.Client
	db *sql.DB
}

func (r *sqlRqlite) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return r.db.ExecContext(ctx, query, args...)
}

// testDeviceKey is a device as a client holds it.
type testDeviceKey struct {
	jwk  string
	id   string
	sign func(string) string
}

func newTestDeviceKey(t *testing.T) *testDeviceKey {
	raw, id, sign := signingDevice(t)
	return &testDeviceKey{jwk: string(raw), id: id, sign: sign}
}

type flow struct {
	t   *testing.T
	h   *Handlers
	svc *authsvc.Service
	db  *sql.DB
}

const flowNamespace = "anchat"

func newFlow(t *testing.T) *flow {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO namespaces(id, name) VALUES (10, ?)`, flowNamespace); err != nil {
		t.Fatalf("namespace: %v", err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	svc, err := authsvc.NewService(testLogger(), &sqlNet{db: &sqlDB{db: db}}, string(keyPEM), "default", "test-hmac-secret")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	svc.SetRqliteClient(&sqlRqlite{db: db})
	return &flow{t: t, h: NewHandlers(testLogger(), svc, nil, "default", noopInternalAuth), svc: svc, db: db}
}

func (f *flow) member(wallet string, role authsvc.Role) {
	f.t.Helper()
	if err := f.svc.Grant(context.Background(), authsvc.GrantRequest{Namespace: flowNamespace,
		PrincipalType: authsvc.PrincipalWallet, Identifier: wallet, Role: role, CreatedBy: "0xowner"}); err != nil {
		f.t.Fatalf("grant: %v", err)
	}
}

type wallet struct {
	address string
	sign    func(string) string
}

func newWallet(t *testing.T) wallet {
	key, _ := ethcrypto.GenerateKey()
	return wallet{address: ethcrypto.PubkeyToAddress(key.PublicKey).Hex(), sign: func(message string) string {
		prefix := []byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len(message)))
		sig, err := ethcrypto.Sign(ethcrypto.Keccak256(prefix, []byte(message)), key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return "0x" + hex.EncodeToString(sig)
	}}
}

func (f *flow) do(handler http.HandlerFunc, method, path string, body any, claims *authsvc.JWTClaims) (int, map[string]any) {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	ctx := context.WithValue(r.Context(), CtxKeyNamespaceOverride, flowNamespace)
	if claims != nil {
		ctx = context.WithValue(ctx, CtxKeyJWT, claims)
	}
	rec := httptest.NewRecorder()
	handler(rec, r.WithContext(ctx))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// signIn runs challenge + verify, binding device d when it is not nil.
func (f *flow) signIn(w wallet, d *testDeviceKey) (int, map[string]any) {
	f.t.Helper()
	challenge := map[string]any{"wallet": w.address, "namespace": flowNamespace}
	if d != nil {
		challenge["device_id"] = d.id
	}
	code, c := f.do(f.h.ChallengeHandler, http.MethodPost, "/v1/auth/challenge", challenge, nil)
	if code != http.StatusOK {
		f.t.Fatalf("challenge: %d %v", code, c)
	}
	message := c["message"].(string)
	req := map[string]any{"message": message, "signature": w.sign(message)}
	if d != nil {
		req["device_key"] = json.RawMessage(d.jwk)
		req["device_signature"] = d.sign(message)
	}
	return f.do(f.h.VerifyHandler, http.MethodPost, "/v1/auth/verify", req, nil)
}

func (f *flow) claimsOf(body map[string]any) *authsvc.JWTClaims {
	f.t.Helper()
	claims, err := f.svc.ParseAndVerifyJWT(body["access_token"].(string))
	if err != nil {
		f.t.Fatalf("the issued token does not verify: %v", err)
	}
	return claims
}

func proof(d *testDeviceKey, action, binding string) map[string]any {
	id := "p" + strconv.FormatInt(time.Now().UnixNano(), 36) + "xxxxxxxxxx"
	iat := time.Now().Unix()
	return map[string]any{"iat": iat, "id": id,
		"sig": d.sign(string(authsvc.DeviceProofMessage(action, flowNamespace, binding, iat, id)))}
}

func TestVerifyHandler_bindsTheDeviceAndHandsOutNoKey(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	d := newTestDeviceKey(t)

	code, body := f.signIn(w, d)
	if code != http.StatusOK {
		t.Fatalf("sign-in: %d %v", code, body)
	}
	if body["device_id"] != d.id || body["api_key"] != nil {
		t.Errorf("device_id %v, api_key %v: want the device and no key", body["device_id"], body["api_key"])
	}
	if f.claimsOf(body).Did != d.id {
		t.Error("the access token does not carry the device")
	}
}

func TestVerifyHandler_requiredPolicyRefusesAnEndUserWithoutADevice(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, authsvc.DevicePolicyRequired, "0xowner"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if code, body := f.signIn(w, nil); code != http.StatusForbidden || body["code"] != ErrCodeDeviceRequired {
		t.Errorf("a sign-in without a device: %d %v", code, body)
	}
	if code, body := f.signIn(w, newTestDeviceKey(t)); code != http.StatusOK {
		t.Errorf("a sign-in with a device: %d %v", code, body)
	}
}

// Property: a wallet signature alone yields a pending device and no session;
// an active device approves it; the new device collects its own session.
func TestVerifyHandler_approvalPolicyNeedsAnExistingDevice(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, authsvc.DevicePolicyApproval, "0xowner"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)
	code, first := f.signIn(w, phone)
	if code != http.StatusOK {
		t.Fatalf("the account's first device: %d %v", code, first)
	}
	code, pending := f.signIn(w, laptop)
	if code != http.StatusAccepted || pending["access_token"] != nil || pending["user_code"] == nil {
		t.Fatalf("a second device with a wallet signature alone: %d %v", code, pending)
	}

	userCode, deviceCode := pending["user_code"].(string), pending["device_code"].(string)
	phoneClaims := f.claimsOf(first)
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodPost, "/v1/auth/devices/approve",
		map[string]any{"user_code": userCode}, phoneClaims); code != http.StatusUnauthorized {
		t.Errorf("an approval without the approving device's proof: %d %v", code, body)
	}
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodPost, "/v1/auth/devices/approve",
		map[string]any{"user_code": userCode, "device_proof": proof(phone, authsvc.DeviceProofApprove, userCode)},
		phoneClaims); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, body)
	}
	if code, body := f.do(f.h.DeviceTokenHandler, http.MethodPost, "/v1/auth/device/token",
		map[string]any{"device_code": deviceCode}, nil); code == http.StatusOK {
		t.Fatalf("the device code alone collected the session: %v", body)
	}
	code, session := f.do(f.h.DeviceTokenHandler, http.MethodPost, "/v1/auth/device/token",
		map[string]any{"device_code": deviceCode, "device_proof": proof(laptop, authsvc.DeviceProofClaim, deviceCode)}, nil)
	if code != http.StatusOK || f.claimsOf(session).Did != laptop.id {
		t.Errorf("collect: %d %v", code, session)
	}
}

// A refusal used to run the approval first: "deny" left the login approved,
// past the device policy.
func TestDeviceApprovalHandler_denyDoesNotApprove(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	pending, err := f.svc.StartDeviceAuthorization(context.Background(), flowNamespace)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	code, c := f.do(f.h.ChallengeHandler, http.MethodPost, "/v1/auth/challenge",
		map[string]any{"wallet": w.address, "namespace": flowNamespace}, nil)
	if code != http.StatusOK {
		t.Fatalf("challenge: %d", code)
	}
	message := c["message"].(string)
	if code, body := f.do(f.h.DeviceApprovalHandler, http.MethodPost, "/v1/auth/device/approve",
		map[string]any{"user_code": pending.UserCode, "message": message, "signature": w.sign(message), "deny": true}, nil); code != http.StatusOK {
		t.Fatalf("deny: %d %v", code, body)
	}
	if _, err := f.svc.ClaimDeviceAuthorization(context.Background(), pending.DeviceCode, nil); err != authsvc.ErrDeviceAccessDenied {
		t.Errorf("after a refusal the login answers %v, want access_denied", err)
	}
}

func TestDeviceApprovalHandler_requiredPolicyRefusesApprovingAPlainLogin(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, authsvc.DevicePolicyRequired, "0xowner"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	pending, _ := f.svc.StartDeviceAuthorization(context.Background(), flowNamespace)
	_, c := f.do(f.h.ChallengeHandler, http.MethodPost, "/v1/auth/challenge",
		map[string]any{"wallet": w.address, "namespace": flowNamespace}, nil)
	message := c["message"].(string)
	code, body := f.do(f.h.DeviceApprovalHandler, http.MethodPost, "/v1/auth/device/approve",
		map[string]any{"user_code": pending.UserCode, "message": message, "signature": w.sign(message)}, nil)
	if code != http.StatusForbidden || body["code"] != ErrCodeDeviceRequired {
		t.Errorf("a wallet approved a session bound to no device: %d %v", code, body)
	}
}

// A lifted access token must not be able to sign the account out of every
// other device: a device-bound caller proves the device to revoke.
func TestDeviceByIDHandler_aDeviceBoundCallerProvesTheDeviceToRevoke(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)
	_, first := f.signIn(w, phone)
	if code, body := f.signIn(w, laptop); code != http.StatusOK {
		t.Fatalf("second device: %d %v", code, body)
	}
	claims := f.claimsOf(first)
	path := "/v1/auth/devices/" + laptop.id
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodDelete, path, nil, claims); code != http.StatusUnauthorized ||
		body["code"] != ErrCodeDeviceProofRequired {
		t.Errorf("a revocation without the device's proof: %d %v", code, body)
	}
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodDelete, path,
		map[string]any{"device_proof": proof(phone, authsvc.DeviceProofRevoke, laptop.id)}, claims); code != http.StatusOK {
		t.Errorf("revoke with proof: %d %v", code, body)
	}
}

// The way back under the approval policy: an operator revokes what a user can
// no longer reach.
func TestNamespaceDeviceByIDHandler_anOperatorRevokesAUsersDevice(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	lost := newTestDeviceKey(t)
	if code, body := f.signIn(w, lost); code != http.StatusOK {
		t.Fatalf("sign-in: %d %v", code, body)
	}
	operator := &authsvc.JWTClaims{Sub: "0xowner", Namespace: flowNamespace}
	code, listed := f.do(f.h.NamespaceDevicesHandler, http.MethodGet,
		"/v1/namespace/devices?subject="+w.address, nil, operator)
	if code != http.StatusOK || len(listed["devices"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, listed)
	}
	if code, body := f.do(f.h.NamespaceDeviceByIDHandler, http.MethodDelete,
		"/v1/namespace/devices/"+lost.id, nil, operator); code != http.StatusOK || body["subject"] != authsvc.NormalizeWallet(w.address) {
		t.Errorf("operator revoke: %d %v", code, body)
	}
}

func TestIssueAPIKeyHandler_requiredPolicyRefusesAnEndUsersKey(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, authsvc.DevicePolicyRequired, "0xowner"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	_, c := f.do(f.h.ChallengeHandler, http.MethodPost, "/v1/auth/challenge",
		map[string]any{"wallet": w.address, "namespace": flowNamespace}, nil)
	message := c["message"].(string)
	code, body := f.do(f.h.IssueAPIKeyHandler, http.MethodPost, "/v1/auth/api-key",
		map[string]any{"message": message, "signature": w.sign(message)}, nil)
	if code != http.StatusForbidden || body["code"] != ErrCodeDeviceRequired {
		t.Errorf("an end user was handed a key bound to no device: %d %v", code, body)
	}
}

func TestSessionPolicyHandler_setsAndReads(t *testing.T) {
	f := newFlow(t)
	operator := &authsvc.JWTClaims{Sub: "0xowner", Namespace: flowNamespace}
	if code, body := f.do(f.h.SessionPolicyHandler, http.MethodPut, "/v1/namespace/session-policy",
		map[string]any{"device_policy": "sometimes"}, operator); code != http.StatusBadRequest {
		t.Errorf("an unknown policy: %d %v", code, body)
	}
	if code, body := f.do(f.h.SessionPolicyHandler, http.MethodPut, "/v1/namespace/session-policy",
		map[string]any{"device_policy": "approval"}, operator); code != http.StatusOK {
		t.Fatalf("set: %d %v", code, body)
	}
	if code, body := f.do(f.h.SessionPolicyHandler, http.MethodGet, "/v1/namespace/session-policy", nil, operator); code != http.StatusOK ||
		body["device_policy"] != "approval" {
		t.Errorf("read back: %d %v", code, body)
	}
}

// From a session bound to a device, logging out everywhere is the device's act
// (bug-960): the access token alone is refused by name and ends nothing; a
// proof for another action does not do; the device's logout-all proof ends
// every session of the account, once. A session bound to no device needs none.
func TestLogoutHandler_allFromADeviceBoundSessionNeedsTheDevicesProof(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)
	_, first := f.signIn(w, phone)
	_, other := f.signIn(w, laptop)
	claims := f.claimsOf(first)
	logoutAll := func(proofBody map[string]any) (int, map[string]any) {
		body := map[string]any{"all": true, "namespace": flowNamespace}
		if proofBody != nil {
			body["device_proof"] = proofBody
		}
		return f.do(f.h.LogoutHandler, http.MethodPost, "/v1/auth/logout", body, claims)
	}
	laptopRefresh := func() (int, map[string]any) {
		rt := other["refresh_token"].(string)
		code, body := f.do(f.h.RefreshHandler, http.MethodPost, "/v1/auth/refresh", map[string]any{
			"refresh_token": rt, "namespace": flowNamespace, "device_proof": proof(laptop, authsvc.DeviceProofRefresh, rt),
		}, nil)
		if code == http.StatusOK {
			other["refresh_token"] = body["refresh_token"]
		}
		return code, body
	}

	if code, body := logoutAll(nil); code != http.StatusUnauthorized || body["code"] != ErrCodeDeviceProofRequired {
		t.Errorf("all=true with the access token alone: %d %v", code, body)
	}
	if code, body := laptopRefresh(); code != http.StatusOK {
		t.Fatalf("the refused logout ended the laptop's session: %d %v", code, body)
	}
	if code, body := logoutAll(proof(phone, authsvc.DeviceProofEndSession, phone.id)); code != http.StatusUnauthorized || body["code"] != ErrCodeDeviceProofInvalid {
		t.Errorf("all=true with an end-session proof: %d %v", code, body)
	}
	good := proof(phone, authsvc.DeviceProofLogoutAll, phone.id)
	if code, body := logoutAll(good); code != http.StatusOK {
		t.Fatalf("all=true with the device's logout-all proof: %d %v", code, body)
	}
	if code, body := logoutAll(good); code != http.StatusUnauthorized || body["code"] != ErrCodeDeviceProofInvalid {
		t.Errorf("the logout-all proof replayed: %d %v", code, body)
	}
	if code, body := laptopRefresh(); code == http.StatusOK {
		t.Errorf("the laptop's session outlived logging out everywhere: %d %v", code, body)
	}

	plain := newWallet(t)
	f.member(plain.address, authsvc.RoleRuntime)
	_, unbound := f.signIn(plain, nil)
	if code, body := f.do(f.h.LogoutHandler, http.MethodPost, "/v1/auth/logout",
		map[string]any{"all": true, "namespace": flowNamespace}, f.claimsOf(unbound)); code != http.StatusOK {
		t.Errorf("all=true from a session bound to no device: %d %v", code, body)
	}
}
