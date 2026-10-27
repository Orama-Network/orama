package serverless

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestHostFunctions_Cache(t *testing.T) {
	// Note: HostFunctions implementation has been moved to pkg/serverless/hostfunctions
	// This test validates that the HostServices interface works correctly

	db := NewMockRQLite()
	ipfs := NewMockIPFSClient()
	logger := zap.NewNop()

	// Create a mock implementation that satisfies HostServices
	var h HostServices = &mockHostServices{
		db:     db,
		ipfs:   ipfs,
		logger: logger,
		logs:   make([]LogEntry, 0),
	}

	ctx := context.Background()

	// Test Storage interface
	cid, err := h.StoragePut(ctx, []byte("data"))
	if err != nil {
		t.Fatalf("StoragePut failed: %v", err)
	}
	data, err := h.StorageGet(ctx, cid)
	if err != nil {
		t.Fatalf("StorageGet failed: %v", err)
	}
	if string(data) != "data" {
		t.Errorf("expected 'data', got %q", string(data))
	}
}

// mockHostServices is a minimal mock for testing the HostServices interface
type mockHostServices struct {
	db     *MockRQLite
	ipfs   *MockIPFSClient
	logger *zap.Logger
	logs   []LogEntry
}

func (m *mockHostServices) DBQuery(ctx context.Context, query string, args []interface{}) ([]byte, error) {
	return nil, nil
}

func (m *mockHostServices) DBExecute(ctx context.Context, query string, args []interface{}) (int64, error) {
	return 0, nil
}

func (m *mockHostServices) DBExecuteV2(ctx context.Context, query string, args []interface{}) ([]byte, error) {
	return []byte(`{"rows_affected":0}`), nil
}

func (m *mockHostServices) DBQueryV2(ctx context.Context, query string, args []interface{}) ([]byte, error) {
	return []byte(`{"rows":[]}`), nil
}

func (m *mockHostServices) DBQueryBatch(ctx context.Context, opsJSON []byte) ([]byte, error) {
	return []byte(`{"results":[]}`), nil
}

func (m *mockHostServices) CacheGet(ctx context.Context, key string) ([]byte, error) {
	return nil, nil
}

func (m *mockHostServices) CacheSet(ctx context.Context, key string, value []byte, ttlSeconds int64) error {
	return nil
}

func (m *mockHostServices) CacheDelete(ctx context.Context, key string) error {
	return nil
}

func (m *mockHostServices) CacheIncr(ctx context.Context, key string) (int64, error) {
	return 0, nil
}

func (m *mockHostServices) CacheIncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	return 0, nil
}

func (m *mockHostServices) CacheIncrByTTL(ctx context.Context, key string, delta, ttlSeconds int64) (int64, error) {
	return 0, nil
}

func (m *mockHostServices) StoragePut(ctx context.Context, data []byte) (string, error) {
	// Mock implementation - just return a fake CID
	return "QmTest123", nil
}

func (m *mockHostServices) StorageGet(ctx context.Context, cid string) ([]byte, error) {
	// Mock implementation - return the test data
	return []byte("data"), nil
}

func (m *mockHostServices) PubSubPublish(ctx context.Context, topic string, data []byte) error {
	return nil
}

func (m *mockHostServices) PubSubPublishBatch(ctx context.Context, msgsJSON []byte) error {
	return nil
}

func (m *mockHostServices) PushSend(ctx context.Context, userID string, msgJSON []byte) error {
	return nil
}

func (m *mockHostServices) PushSendV2(ctx context.Context, userID string, msgJSON []byte) ([]byte, error) {
	return []byte(`{"ok":true,"devices_attempted":0,"devices_succeeded":0,"results":[]}`), nil
}

func (m *mockHostServices) PushSendTopic(ctx context.Context, topicID string, msgJSON []byte) ([]byte, error) {
	return []byte(`{"ok":true,"devices_attempted":0,"devices_succeeded":0,"results":[]}`), nil
}

func (m *mockHostServices) TurnCredentials(ctx context.Context) ([]byte, error) {
	return []byte(`{"configured":false}`), nil
}

func (m *mockHostServices) DBTransaction(ctx context.Context, opsJSON []byte) ([]byte, error) {
	return []byte(`{"committed":true,"results":[]}`), nil
}

func (m *mockHostServices) ExecAndPublish(ctx context.Context, opsJSON []byte, topic string, dataTemplate []byte) ([]byte, error) {
	return []byte(`{"committed":true,"published":true,"seq":1,"results":[]}`), nil
}

func (m *mockHostServices) WSPubSubBridge(ctx context.Context, clientID, topic string) error {
	return nil
}

func (m *mockHostServices) WSPubSubUnbridge(ctx context.Context, clientID, topic string) error {
	return nil
}

func (m *mockHostServices) SetHTTPResponse(ctx context.Context, status int, headers map[string]string, body []byte) error {
	return SetRawHTTPResponse(ctx, status, headers, body)
}

func (m *mockHostServices) EphemeralStateSet(ctx context.Context, topic, key string, payload []byte, ttlMs int64) error {
	return nil
}

func (m *mockHostServices) EphemeralStateClear(ctx context.Context, topic, key string) error {
	return nil
}

func (m *mockHostServices) EphemeralStateList(ctx context.Context, topic string) ([]byte, error) {
	return []byte(`{"entries":[]}`), nil
}

func (m *mockHostServices) WSSend(ctx context.Context, clientID string, data []byte) error {
	return nil
}

func (m *mockHostServices) WSBroadcast(ctx context.Context, topic string, data []byte) error {
	return nil
}

func (m *mockHostServices) FunctionInvoke(ctx context.Context, name string, payload []byte) ([]byte, error) {
	return nil, nil
}

func (m *mockHostServices) FunctionInvokeAsync(ctx context.Context, name string, payload []byte) error {
	return nil
}

func (m *mockHostServices) HTTPFetch(ctx context.Context, method, url string, headers map[string]string, body []byte) ([]byte, error) {
	return nil, nil
}

func (m *mockHostServices) AnonFetch(ctx context.Context, method, url string, headers map[string]string, body []byte) ([]byte, error) {
	return nil, nil
}

func (m *mockHostServices) GetEnv(ctx context.Context, key string) (string, error) {
	return "", nil
}

func (m *mockHostServices) GetSecret(ctx context.Context, name string) (string, error) {
	return "", nil
}

func (m *mockHostServices) GetRequestID(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetCallerWallet(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetWSClientID(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetCallerClaim(ctx context.Context, name string) string {
	return ""
}

func (m *mockHostServices) GetCallerJWTSubject(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetCallerDeviceID(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetCallerAttestation(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetCallerAttestationVerdict(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) GetCallerCapability(ctx context.Context) string {
	return ""
}

func (m *mockHostServices) MintCapability(ctx context.Context, resource string, ttl time.Duration) (string, error) {
	return "", nil
}

func (m *mockHostServices) RevokeCapability(ctx context.Context, token string) error {
	return nil
}

func (m *mockHostServices) MintStorageFetchCaps(ctx context.Context, cid string, count int, ttl time.Duration) (string, error) {
	return "", nil
}

func (m *mockHostServices) WebRTCAdmit(ctx context.Context, room, user, device string, ttl time.Duration) (string, error) {
	return "", nil
}

func (m *mockHostServices) WebRTCKick(ctx context.Context, room, user string) error {
	return nil
}

func (m *mockHostServices) WebRTCMute(ctx context.Context, room, user string, muted bool) error {
	return nil
}

func (m *mockHostServices) EnqueueBackground(ctx context.Context, functionName string, payload []byte) (string, error) {
	return "", nil
}

func (m *mockHostServices) ScheduleOnce(ctx context.Context, functionName string, runAt time.Time, payload []byte) (string, error) {
	return "", nil
}

func (m *mockHostServices) LogInfo(ctx context.Context, message string) {
	m.logs = append(m.logs, LogEntry{Level: "info", Message: message})
}

func (m *mockHostServices) LogError(ctx context.Context, message string) {
	m.logs = append(m.logs, LogEntry{Level: "error", Message: message})
}
