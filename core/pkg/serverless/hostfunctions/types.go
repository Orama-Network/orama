package hostfunctions

import (
	"net/http"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/ipfs"
	"github.com/Orama-Network/orama/core/pkg/pubsub"
	"github.com/Orama-Network/orama/core/pkg/push"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/serverless/triggers"
	"github.com/Orama-Network/orama/core/pkg/serverless/wsbridge"
	olriclib "github.com/olric-data/olric"
	"go.uber.org/zap"
)

// HostFunctionsConfig holds configuration for HostFunctions.
type HostFunctionsConfig struct {
	IPFSAPIURL  string
	HTTPTimeout time.Duration

	// TURN configuration — feat-9. Plumbed in from the gateway so the
	// `turn_credentials` host fn can mint per-namespace TURN credentials
	// without a round-trip back through HTTP. Mirrors the HTTP endpoint
	// at /v1/webrtc/turn/credentials. TURNSecret empty → host fn returns
	// a structured "TURN not configured" envelope (no error).
	TURNDomain       string
	TURNSecret       string
	StealthCDNDomain string // optional; non-empty adds turns:<domain>:443 URI

	// DatabaseNamespace is the namespace whose functions may use the database
	// handle: a namespace gateway's own client_namespace. A database host call
	// from a function in any other namespace is refused (bugboard #427). Empty
	// means no function may use it — the cluster gateway's database is the
	// cluster registry.
	DatabaseNamespace string
}

// HostFunctions provides the bridge between WASM functions and Orama services.
// It implements the HostServices interface and is injected into the execution
// context.
//
// It holds **no per-invocation state**. One of these exists for the life of the
// gateway and is shared by every invocation running on it, so anything about
// "the current invocation" kept here is a field two concurrent invocations
// overwrite for each other. It had two — the invocation context and the
// captured logs — and both produced exactly that: bugboard #348 was a
// cross-tenant identity leak through the first, and #108 was one invocation's
// log lines appearing in another's record through the second.
//
// Identity and logs ride the context instead, attached per invocation by the
// engine. A host call with no invocation on its context is a host call outside
// any invocation, and says so rather than picking up whoever ran last.
type HostFunctions struct {
	db rqlite.Client
	// dbNamespace is the namespace db belongs to; see checkDatabaseAccess.
	dbNamespace string

	// cacheClient yields the Olric client connected right now, nil when there
	// is none; read on every operation because the gateway replaces the client
	// when it reconnects.
	cacheClient func() olriclib.Client
	storage     ipfs.IPFSClient
	ipfsAPIURL  string
	pubsub      pubsub.Bus
	wsManager   serverless.WebSocketManager
	secrets     serverless.SecretsManager
	httpClient  *http.Client
	// anonHTTPClient routes every outbound request of AnonFetch through
	// the node's Tor SOCKS5 port (feat-11). It never falls back to the
	// direct httpClient (no silent privacy regression).
	anonHTTPClient *http.Client
	logger         *zap.Logger

	// pushDispatcher (legacy) and pushManager (per-namespace, bug #220
	// follow-up) provide push send-paths. When pushManager is set, PushSend
	// uses it so per-namespace config takes effect; pushDispatcher is the
	// fallback. Both nil = push not configured anywhere on this gateway,
	// PushSend returns nil silently for portability.
	pushDispatcher *push.PushDispatcher
	pushManager    *push.Manager

	// wsBridge may be nil when the gateway doesn't run a bridge. In that
	// case WSPubSubBridge returns an error rather than silently no-oping
	// — bridging is a deliberate request whose absence should be visible.
	wsBridge *wsbridge.Bridge

	// invoker is set after construction (via SetInvoker) to break the
	// engine ↔ host-functions circular dep. nil means FunctionInvoke
	// returns ErrFunctionInvokeNotAvailable.
	invoker     serverless.FunctionInvoker
	invokerLock sync.RWMutex

	// asyncInvokeSem bounds the number of concurrently-running
	// FunctionInvokeAsync goroutines across the gateway. A buffered channel
	// used as a counting semaphore: a slot is taken before spawning and
	// released when the goroutine finishes. When full, FunctionInvokeAsync
	// rejects (backpressure to the guest) instead of spawning unbounded
	// goroutines under a frame flood. Built in NewHostFunctions; nil only in
	// bare test construction (treated as unbounded there).
	asyncInvokeSem chan struct{}

	// TURN config — feat-9. Cached at NewHostFunctions; immutable for
	// the gateway's lifetime so no lock needed. Empty TURNSecret means
	// `turn_credentials` host fn returns a configured=false envelope
	// instead of an error (same shape as PushSend's silent-noop when
	// push isn't configured — keeps functions portable).
	turnDomain       string
	turnSecret       string
	stealthCDNDomain string

	// triggerDispatcher is set after construction (via SetTriggerDispatcher).
	// When non-nil, PubSubPublish / PubSubPublishBatch synchronously fire
	// wildcard triggers on the local gateway so functions like
	// presence-aggregator with trigger "presence:*" actually receive
	// WASM-published events (bugboard #93, plan-3 wildcard delivery gap).
	// nil leaves the existing behavior (libp2p-only delivery; wildcards
	// silently dropped on WASM publishes).
	triggerDispatcher     *triggers.PubSubDispatcher
	triggerDispatcherLock sync.RWMutex

	// ephemeralStore backs ephemeral_state_set / ephemeral_state_clear
	// (bugboard #710). Constructed in NewHostFunctions when a WS manager is
	// present; nil otherwise (host fns then return an error). The store
	// registers a disconnect hook on the WS manager so a client's owned state
	// auto-clears the instant its WebSocket disconnects.
	ephemeralStore *serverless.EphemeralStore

	// capabilityIssuer mints and revokes capabilities (feat-264). Set once at
	// gateway start, before any function runs, via SetCapabilityIssuer; nil
	// means capability_mint and capability_revoke fail and say why.
	capabilityIssuer serverless.CapabilityIssuer

	// fetchCapIssuer mints storage fetch capabilities (bugboard #266). Set
	// once at gateway start via SetFetchCapIssuer; nil means
	// storage_fetch_cap_mint fails and says why.
	fetchCapIssuer serverless.FetchCapIssuer

	// attestation says whether a namespace enforces device attestation, which
	// gates both mints (attestation.go). Set once at gateway start; nil checks
	// nothing.
	attestation AttestationEnforcement

	// attestationJudge answers get_caller_attestation_verdict (attestation.go).
	// Set once at gateway start; nil answers nothing.
	attestationJudge AttestationJudge

	// webrtc admits, kicks and mutes users in the namespace's rooms. Set once
	// at gateway start via SetWebRTCController; nil means webrtc_admit,
	// webrtc_kick and webrtc_mute fail and say why.
	webrtc serverless.WebRTCController
}

// Ensure HostFunctions implements HostServices interface.
var _ serverless.HostServices = (*HostFunctions)(nil)

// cacheDMapName prefixes each namespace's function cache map
// (":serverless_cache:<namespace>"). The HTTP cache API names its maps
// "<namespace>:<dmap>" on the same Olric and refuses an empty namespace, so a
// name that starts with ':' is one it can never produce.
const cacheDMapName = ":serverless_cache"
