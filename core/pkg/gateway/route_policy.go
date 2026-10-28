package gateway

import (
	"context"
	"net/http"
	"strings"

	nodeauth "github.com/Orama-Network/orama/core/pkg/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	serverlesshandlers "github.com/Orama-Network/orama/core/pkg/gateway/handlers/serverless"
	"github.com/Orama-Network/orama/core/pkg/gateway/routepolicy"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
)

// Who may call what.
//
// This replaces isPublicPath, requiredScope and requiresNamespaceOwnership —
// three hand-maintained lists of path prefixes that nothing tied to the routes
// they described. See package routepolicy for what that cost.
//
// Every route this gateway serves is declared here, and a route that is not
// declared cannot be registered: routepolicy.Mux panics on it, and a test fails
// on it first. The middleware asks the table which policy the request's route
// carries; it never looks at the path itself.

var (
	// open is a route deliberately reachable by anyone.
	policyOpen = routepolicy.Policy{Access: routepolicy.Open}

	// handlerAuth is a route whose handler authenticates the caller itself. It
	// must be exempt from the API-key middleware, or that middleware refuses
	// the caller's credential — an invite token, the cluster secret — as a bad
	// API key before the handler that understands it runs.
	policyHandlerAuth = routepolicy.Policy{Access: routepolicy.HandlerAuth}

	// credential is a route any valid credential reaches.
	policyCredential = routepolicy.Policy{}
)

// control declares a control-plane route: which part of the platform, and what
// this route does to it.
//
// Fifty-eight routes used to declare the one word `admin`. So a credential that
// could deploy could also mint keys, transfer the namespace and read the audit
// trail — and a grant could not be narrowed to any of them without holding all
// of them. That is why there was no `developer` role and why a `db:table=`
// selector could not be enforced.
func control(domain auth.Domain, action auth.Action) routepolicy.Policy {
	return routepolicy.Policy{Domain: string(domain), Action: string(action)}
}

// owned is a control-plane route on a namespace's own resources: the caller
// must also hold a live grant in it.
func owned(domain auth.Domain, action auth.Action) routepolicy.Policy {
	p := control(domain, action)
	p.Ownership = true
	return p
}

// dataPlane is a data-plane grant, with the kind of token it requires.
//
// ownership says whether the caller must additionally hold a live grant in the
// namespace. Storage and cache do not ask for one — a wallet with no grant holds
// the data plane — but a wallet that does hold a narrowed grant has it resolved
// on those routes so its selector applies (narrowed_grant.go).
func dataPlane(domain auth.Domain, action auth.Action, ownership bool, token routepolicy.TokenRequirement) routepolicy.Policy {
	return routepolicy.Policy{
		Domain:    string(domain),
		Action:    string(action),
		Ownership: ownership,
		Token:     token,
	}
}

// gatewayRoutes is the policy of every route. It is built once and read from
// every request.
var gatewayRoutes = buildRoutePolicies()

// RoutePolicies is the policy table of every route the gateway serves. The
// whitepaper generator renders Appendix C from it.
func RoutePolicies() *routepolicy.Table { return gatewayRoutes }

func buildRoutePolicies() *routepolicy.Table {
	t := routepolicy.NewTable()

	// --- Open to anyone ------------------------------------------------
	t.Add(policyOpen,
		"/health", "/status",
		"/v1/health", "/v1/status", "/v1/version",
		// The status page's script and stylesheet: static files, the same
		// for everyone. Its data is /v1/status.
		"/status/assets/",
		// Getting a key with a wallet signature.
		"/v1/auth/api-key",
		// The device authorization grant. The machine asking has no credential
		// — that is what it is asking for — and approving one costs a wallet
		// signature, which the handler verifies exactly as /v1/auth/verify does.
		"/v1/auth/device", "/v1/auth/device/approve", "/v1/auth/device/token",
		// Polled while a namespace's cluster is still provisioning, by a client
		// that has not been given anything to poll it with yet.
		"/v1/namespace/status",
		// Called by Caddy on this host. Present and cleanup authenticate
		// Caddy in the handler, by a MAC under the ACME challenge key install
		// gives it (acme_auth.go); they are Open because that key is not an
		// API key. tls/check is still on the list of things Phase 2 has to
		// give a credential to.
		"/v1/internal/acme/present", "/v1/internal/acme/cleanup", "/v1/internal/tls/check",
		// Peer health probing. Returns the node id and nothing else.
		"/v1/internal/ping",
		// Chain proxy for the explorer and for wallets. The handler allowlists
		// Comet and bank/staking reads, the chain indexer's routes under
		// /v1/chain/index/, the Orama modules' Query services and the bounded
		// cosmos-sdk and wasmd ones a wallet reads under /v1/chain/query/, and
		// two POST routes, /v1/chain/simulate and /v1/chain/broadcast, which
		// take a signed transaction. A wallet has no credential to present: the
		// chain charges the sender a fee for a transaction, and the gateway
		// bounds the load (chain_tx_limit.go). It does not forward an arbitrary
		// path.
		"/v1/chain/",
	)
	// The login handshake and the session's own life: open, since nobody has
	// a credential yet when it starts, and the routes a pending device's
	// restricted session reaches (feat-973). Refreshing is how it becomes a
	// full session once the namespace approves the device; logging out ends
	// this session only (all=true is refused to it in the handler).
	t.Add(policyOwnSession(policyOpen),
		// The key material a client needs to verify a token it was given.
		"/v1/auth/jwks", "/.well-known/jwks.json",
		"/v1/auth/challenge", "/v1/auth/verify", "/v1/auth/refresh",
		"/v1/auth/logout",
	)
	// The invoker decides whether the caller may run the function, and a
	// public function is open by design — but a grant narrowed to
	// `fn:name=` still limits it, on this route as on /v1/functions/<fn>/invoke.
	t.Add(policyInvoke, "/v1/invoke/")

	// --- The handler authenticates the caller --------------------------
	t.Add(policyHandlerAuth,
		// Invite token, validated and consumed single-use in the handler.
		"/v1/internal/join", "/v1/node/enroll",
		// This node's Caddy, by a coordination v2 stamp under the TLS store's
		// MAC key, which install gives it (tls_store_handler.go).
		"/v1/internal/tls-store",
		// A node's health report, for a peer's cluster gateway: a
		// coordination MAC over the request plus a WireGuard-peer source
		// check, in the handler (internalTelemetryHandler).
		"/v1/internal/telemetry",
		// An SFU of this namespace reporting a join or leave: a MAC under the
		// key derived from the namespace's TURN secret, in the handler.
		"/v1/internal/webrtc/events",
		// A MAC over the request, keyed by a value derived from the cluster
		// secret, naming the node the claim is about. The handler acts on that
		// name and never on the body's. Declared MainGateway below: `dns_nodes`
		// lives in the cluster registry, and a namespace gateway's isolated
		// RQLite does not have it.
		// Signed internal header plus a WireGuard-peer source check.
		"/v1/internal/namespace/spawn", "/v1/internal/namespace/repair",
		"/v1/internal/secrets/reencrypt",
		"/v1/internal/storage/evict",
		"/v1/internal/deployments/replica/setup", "/v1/internal/deployments/replica/update",
		"/v1/internal/deployments/replica/rollback", "/v1/internal/deployments/replica/teardown",
		"/v1/internal/deployments/replica/env",
		// Rate-limited per identity hash inside the handler.
		"/v1/vault/push", "/v1/vault/pull", "/v1/vault/status", "/v1/vault/health",
	)

	// --- Any valid credential ------------------------------------------
	t.Add(policyCredential,
		// Exchanging a key for a token.
		"/v1/auth/token",
		// A wallet's own sessions. The handler refuses anything but a token
		// from a signed-in wallet, and reads whose sessions from that token.
		"/v1/auth/sessions", "/v1/auth/sessions/",
		// A wallet's own devices. Same rule: the handler refuses anything but
		// a signed-in token, and reads whose devices from it.
		"/v1/auth/devices", "/v1/auth/devices/",
		// A workload renewing its own token. It needs the token it is renewing
		// and nothing else, which is what the handler checks.
		"/v1/auth/renew",
		// A read of the gateway's own schema version.
		"/v1/schema-status",
		// Whether WebRTC is on. The switches beside it are admin.
		"/v1/namespace/webrtc/status",
	)

	// Asking what the credential is. A pending device is told it is pending.
	t.Add(policyOwnSession(policyCredential), "/v1/auth/whoami")

	// Device attestation (feat-941): a signed-in device asks for a challenge
	// and posts its proof. The handler refuses anything but a device-bound
	// session's token. MainGateway: the index gateway is the one that keeps
	// Google's and GrapheneOS's lists fresh, so a namespace gateway never
	// verifies an Android chain. A pending device attests itself: a namespace
	// that enforces attestation judges its admission functions' callers too.
	attestRoute := policyOwnSession(policyCredential)
	attestRoute.MainGateway = true
	t.Add(attestRoute, "/v1/attest/challenge", "/v1/attest/ios", "/v1/attest/android")

	// --- Control plane -------------------------------------------------
	//
	// Each of these says what it does rather than that it is "admin". A key
	// or a grant narrowed to one of these domains reaches it and nothing
	// else, which is the whole point of the split.

	// The record of who was given what and when. It names the namespace's
	// wallets and the times they sign in, so reading it is the owner's, not
	// any credential that happens to belong to the namespace.
	t.Add(control(auth.DomainAudit, auth.ActionRead), "/v1/audit")

	// The tenant's databases. `query` runs whatever SQL the caller sends, so
	// it is a write however it is spelled.
	t.Add(control(auth.DomainDB, auth.ActionRead), "/v1/db/sqlite/list", "/v1/db/sqlite/backups")
	t.Add(control(auth.DomainDB, auth.ActionWrite),
		"/v1/db/sqlite/create", "/v1/db/sqlite/query", "/v1/db/sqlite/delete", "/v1/db/sqlite/backup")

	// Deployments: reading what is there, and changing it. Every deployment
	// route is MainGateway: a deployment's units, logs and stats are this
	// node's systemd, which only the index gateway may drive through the
	// privileged helper. Served by a namespace gateway, a delete removed the
	// rows and freed the port while the refused stop left the unit running on
	// it, and the next deployment given the port crash-looped behind the old
	// one (stagenet e2e, 2026-10-01).
	t.Add(deploymentRoute(control(auth.DomainDeploy, auth.ActionRead)),
		"/v1/deployments/list", "/v1/deployments/get", "/v1/deployments/versions",
		"/v1/deployments/logs", "/v1/deployments/stats", "/v1/deployments/events",
		"/v1/deployments/domains/list")
	t.Add(deploymentRoute(control(auth.DomainDeploy, auth.ActionWrite)),
		"/v1/deployments/delete", "/v1/deployments/rollback",
		"/v1/deployments/static/upload", "/v1/deployments/static/update",
		"/v1/deployments/nextjs/upload", "/v1/deployments/nextjs/update",
		"/v1/deployments/go/upload", "/v1/deployments/go/update",
		"/v1/deployments/nodejs/upload", "/v1/deployments/nodejs/update",
		"/v1/deployments/domains/add", "/v1/deployments/domains/verify",
		"/v1/deployments/domains/remove")

	// A deployment's environment is its secrets, and handing a deployment its
	// grants is handing out authority — so that one is `members`, not `deploy`.
	t.Add(deploymentRoute(control(auth.DomainSecrets, auth.ActionRead)), "/v1/deployments/env")
	t.Add(deploymentRoute(control(auth.DomainSecrets, auth.ActionWrite)), "/v1/deployments/env/set")
	t.Add(deploymentRoute(control(auth.DomainMembers, auth.ActionWrite)), "/v1/deployments/grants")

	// A namespace's own settings, and its deletion.
	//
	// Deletion is MainGateway: it tears down the cluster a namespace gateway
	// runs on, and only the index gateway has the handler, so proxied from
	// ns-<name> it was a 404 (stagenet e2e, 2026-09-30). The namespace comes
	// from the subdomain, and the credential must still belong to it.
	namespaceDeletion := control(auth.DomainNamespace, auth.ActionWrite)
	namespaceDeletion.MainGateway = true
	t.Add(namespaceDeletion, "/v1/namespace/delete")
	// The browser origins a namespace admits. An owner's or admin's: which
	// sites may read the namespace's answers with its users' credentials is
	// the namespace's to decide, and a developer's grant does not reach
	// namespace settings. MainGateway: the index gateway answers ns-<name>
	// hosts, so its cache is the one a change must reach at once.
	corsOrigins := control(auth.DomainNamespace, auth.ActionWrite)
	corsOrigins.MainGateway = true
	t.Add(corsOrigins, "/v1/namespace/cors-origins")
	t.Add(control(auth.DomainNamespace, auth.ActionWrite),
		"/v1/namespace/rate-limit", "/v1/namespace/session-policy",
		// Which app builds the namespace admits (feat-941).
		"/v1/namespace/attestation-policy",
		"/v1/namespace/webrtc/enable", "/v1/namespace/webrtc/disable",
		// The namespace's own WebRTC policy: whether its rooms admit only
		// users its functions admitted.
		"/v1/webrtc/config",
		"/v1/namespace/webrtc/stealth/enable", "/v1/namespace/webrtc/stealth/disable")

	// Topology mutation and node operation: the cluster operator's. Every one
	// is an operatorRoute, so the scope gate admits only a wallet session on
	// the cluster's operator list; no namespace role, grant or key reaches
	// them, `*:*:*` included. Most handlers check the list again.
	//
	// The node routes reach a node's agent: its status, its logs, restarting
	// or stopping its services, dropping it from the mesh. They declared
	// operator:read and operator:write, which a namespace owner's or admin's
	// `*:*:*` held, and their handlers checked nothing (bugboard bug-55).
	t.Add(operatorRoute(), "/v1/node/status", "/v1/node/logs", "/v1/node/command", "/v1/node/leave")
	// This node's health with every check's detail, and the whole cluster's,
	// one-shot and streamed: every node's report, addresses and versions
	// included.
	t.Add(operatorRoute(), "/v1/operator/health", "/v1/operator/telemetry", "/v1/operator/telemetry/stream")

	// This node's peers, its peer ids and its storage peers' addresses: a map
	// of the cluster. They were open to anyone. Another node's discovery asks
	// with a coordination MAC over the mesh, which the handler checks; anyone
	// else needs to be an operator.
	t.AddDynamic("/v1/network/status", networkDetailPolicy)
	t.AddDynamic("/v1/network/peers", networkDetailPolicy)
	// Listing the operator's nodes, registering one, connecting and
	// disconnecting peers (which changes the cluster's map), the operator list
	// itself, and rotating the signing key and the cluster's secrets.
	t.Add(operatorRoute(), "/v1/operator/nodes", "/v1/network/connect", "/v1/network/disconnect",
		"/v1/operator/node/register",
		"/v1/operator/operators", "/v1/operator/operators/",
		"/v1/operator/rotate-signing-key", "/v1/operator/rotate-secrets")
	// Who may create a namespace, and the allowlist. cluster_settings and
	// namespace_creators live only in the cluster registry.
	t.Add(operatorRoute(),
		"/v1/operator/settings", "/v1/operator/settings/",
		"/v1/operator/creators", "/v1/operator/creators/")
	// Minting a cluster invite hands out the cluster secret, the swarm key and
	// every other secret the cluster holds. It asked for `*:*:*`, which every
	// namespace owner holds in their own namespace.
	t.Add(operatorRoute(), "/v1/operator/invite")
	// The cluster's own audit events (namespace IS NULL): invites minted,
	// nodes joining and claimed, namespaces deleted. They name operators,
	// nodes and every tenant's deletion, so they are an operator's to read.
	t.Add(operatorRoute(), "/v1/operator/audit")
	// Removing a namespace its owner can no longer delete. The namespace comes
	// from the body, not the host.
	t.Add(operatorRoute(), "/v1/operator/namespaces/remove")

	// --- Control plane on a namespace's own resources ------------------

	// The raw database: an export is every row in it, an import replaces them.
	// On a namespace gateway the handlers also require the owner's grant
	// (refuseWholeDatabaseToNonOwner): the rows include every key and grant.
	t.Add(owned(auth.DomainDB, auth.ActionRead), "/v1/rqlite/export")
	t.Add(owned(auth.DomainDB, auth.ActionWrite), "/v1/rqlite/import")

	// A backup is every secret in the namespace, sealed to a key the caller
	// names; a restore replaces the database. Both handlers also require the
	// owner's grant. The restore key is public and only names what to seal to.
	t.Add(owned(auth.DomainSecrets, auth.ActionRead), "/v1/namespace/backup")
	t.Add(owned(auth.DomainDB, auth.ActionRead), "/v1/namespace/restore-key")
	t.Add(owned(auth.DomainDB, auth.ActionWrite), "/v1/namespace/restore")

	// Handing out authority in a namespace is the control plane's own control
	// plane. Transferring goes further and needs the owner, which the handler
	// checks: a permission set cannot express "owner", only what an owner may
	// do.
	t.Add(owned(auth.DomainMembers, auth.ActionWrite),
		"/v1/namespace/members", "/v1/namespace/members/",
		// Revoking a user's device on their behalf is deciding who may hold a
		// session in the namespace.
		"/v1/namespace/devices", "/v1/namespace/devices/")

	t.Add(owned(auth.DomainSecrets, auth.ActionWrite),
		"/v1/namespace/push-credentials", "/v1/namespace/push-credentials/",
		"/v1/push/config")
	// Sending reaches every user's devices and every topic, so it needs
	// push:send, which no client credential holds: a developer or above, or a
	// key minted with the push-send grant (bugboard #434). Registration is
	// push:write, below.
	t.Add(owned(auth.DomainPush, auth.ActionSend), "/v1/push/send", "/v1/push/topics/send")

	t.Add(owned(auth.DomainFn, auth.ActionRead), "/v1/serverless/ws/connections", "/v1/serverless/ws/connections/")
	// Function management. /v1/functions/ is one handler serving several
	// operations and is declared below.
	t.Add(owned(auth.DomainFn, auth.ActionManage), "/v1/functions")

	// Creating a namespace is the one control-plane act that cannot require an
	// existing grant: a wallet with no namespace has no grant anywhere, and
	// requiring one would mean nobody could ever start. What it does require is
	// a signed-in wallet — the handler reads the owner from the token and
	// writes the owner grant — so a key cannot create a namespace and a leaked
	// one cannot fill the registry with them. Who may call it, and the
	// per-wallet cap, are cluster settings the handler reads. A grant cannot
	// express "this wallet is an operator", and open mode includes wallets
	// that hold no grant at all.
	t.Add(routepolicy.Policy{Token: routepolicy.WalletToken}, "/v1/namespaces")

	// Listing the namespaces a wallet owns is the same: it is about the
	// wallet, not about the namespace its session happens to be in, and a
	// wallet in the lobby holds no grant to require.
	// MainGateway: the registry of who owns what is the index gateway's.
	t.Add(routepolicy.Policy{Token: routepolicy.WalletToken, MainGateway: true}, "/v1/namespace/list")

	// Scoped API-key management operates on the MAIN cluster registry, where
	// keys are validated. A namespace gateway's own RQLite has no authoritative
	// api_keys table, so a key written there would never authenticate.
	keyManagement := owned(auth.DomainMembers, auth.ActionWrite)
	keyManagement.MainGateway = true
	t.Add(keyManagement, "/v1/namespace/keys", "/v1/namespace/keys/")

	// A node recording itself. The handler checks a MAC over the request, keyed
	// by a value derived from the cluster secret, naming the node the claim is
	// about; it acts on that name and never on the body's.
	//
	// MainGateway for the same reason as the keys above: `dns_nodes` lives in
	// the cluster registry, and a namespace gateway's isolated RQLite does not
	// have it. Without this, addressing the request at `ns-<name>.<domain>`
	// proxies it into a tenant's gateway and lands the row in the wrong
	// database.
	nodeSelfRegistration := policyHandlerAuth
	nodeSelfRegistration.MainGateway = true
	t.Add(nodeSelfRegistration, "/v1/internal/node/register", "/v1/internal/node/heartbeat",
		"/v1/internal/node/enrol-key")

	// A peer's push fan-out, relayed to this node's own ntfy. A v2 coordination
	// MAC (covers the body, names this node) plus a WireGuard-peer source check,
	// in the handler. MainGateway: the node's ntfy and the stamp's audience are
	// the index gateway's, not a tenant namespace gateway's.
	pushNtfyRelay := policyHandlerAuth
	pushNtfyRelay.MainGateway = true
	t.Add(pushNtfyRelay, "/v1/internal/push/ntfy/")

	// --- Data plane ----------------------------------------------------
	//
	// storage and webrtc additionally require a principal's token: a logged-in
	// user, or a deployed app holding its own workload token. proxy requires a
	// logged-in user: the tunnel is an end user's anonymity, keyed to who they
	// are. That is what makes an extracted runtime key worthless: the key alone
	// reaches none of them.
	t.Add(dataPlane(auth.DomainStorage, auth.ActionRead, false, routepolicy.PrincipalToken),
		"/v1/storage/get/", "/v1/storage/status/")
	t.Add(dataPlane(auth.DomainStorage, auth.ActionWrite, false, routepolicy.PrincipalToken),
		"/v1/storage/upload", "/v1/storage/pin")
	// Fetch capabilities (bugboard #266): minting and revoking are the owner's
	// storage-read acts. Using one is not: the capability is the whole
	// authorization of its download, checked by the handler before anything is
	// read, and the route keeps no address in the request log, since the
	// request reaches it through a relay.
	t.Add(dataPlane(auth.DomainStorage, auth.ActionRead, false, routepolicy.PrincipalToken),
		"/v1/storage/fetch-caps", "/v1/storage/fetch-caps/")
	relayedDownload := policyHandlerAuth
	relayedDownload.RequestLog = routepolicy.LogNoAddress
	t.Add(relayedDownload, "/v1/storage/relayed/")
	t.Add(dataPlane(auth.DomainWebRTC, auth.ActionRead, true, routepolicy.PrincipalToken),
		"/v1/webrtc/turn/credentials", "/v1/webrtc/signal", "/v1/webrtc/rooms")
	// The anonymity routes keep no record of who used them: no request_logs row
	// (which would pair the caller's address and API key id with the time, size
	// and duration of a request) and no access-log line. The handlers log what
	// the operator needs without the destination or the caller, and the request
	// metrics count the request by status.
	anonymised := dataPlane(auth.DomainProxy, auth.ActionWrite, true, routepolicy.WalletToken)
	anonymised.RequestLog = routepolicy.LogNone
	t.Add(anonymised, "/v1/proxy/anon", "/v1/proxy/tunnel")
	// The relay (bugboard #266) is the one proxy route with no credential: a
	// destination-pinned tunnel any client may open, rate-limited by address.
	// MainGateway: the Tor client is the node's, whatever host was asked for.
	relayTunnel := policyOpen
	relayTunnel.MainGateway = true
	relayTunnel.RequestLog = routepolicy.LogNone
	t.Add(relayTunnel, "/v1/proxy/relay")
	t.Add(dataPlane(auth.DomainPubsub, auth.ActionRead, true, routepolicy.AnyCredential),
		"/v1/pubsub/ws", "/v1/pubsub/topics", "/v1/pubsub/presence")
	t.Add(dataPlane(auth.DomainPubsub, auth.ActionWrite, true, routepolicy.AnyCredential),
		"/v1/pubsub/publish", "/v1/pubsub/publish-batch")
	// Topic registration (FEAT-265) carries the same grant as device
	// registration; the handler binds nothing about the caller to the topic.
	t.Add(dataPlane(auth.DomainPush, auth.ActionWrite, true, routepolicy.AnyCredential),
		"/v1/push/devices", "/v1/push/devices/", "/v1/push/topics")
	t.Add(dataPlane(auth.DomainCache, auth.ActionRead, false, routepolicy.AnyCredential),
		"/v1/cache/health", "/v1/cache/get", "/v1/cache/mget", "/v1/cache/scan")
	t.Add(dataPlane(auth.DomainCache, auth.ActionWrite, false, routepolicy.AnyCredential),
		"/v1/cache/put", "/v1/cache/delete")

	// Unpinning is the one storage operation that does not need a logged-in
	// user (bugboard #151). It is namespace-ownership-checked in its handler
	// and can only DROP the namespace's own pins — it never reads or uploads —
	// so a userless server-side reclaim (cron, avatar GC) may prove possession
	// of its storage-scoped key by exchanging it for a token. A bare key still
	// fails. Upload, get and pin keep the strict requirement.
	//
	// The handler reaches every CID the namespace owns, so the storage grant is
	// what keeps a key out of here, and no key profile meant for an app bundle
	// carries it (auth.ProfileGrants, bugboard #946): the key that unpins is a
	// storage key a function keeps in the namespace's secrets.
	t.AddDynamic("/v1/storage/unpin/", func(r *http.Request) routepolicy.Policy {
		if r.Method == http.MethodDelete {
			return dataPlane(auth.DomainStorage, auth.ActionWrite, false, routepolicy.AnyToken)
		}
		return dataPlane(auth.DomainStorage, auth.ActionWrite, false, routepolicy.PrincipalToken)
	})

	// The rqlite ORM composes its own patterns from a base path and reports
	// them, so they are declared from the same list it registers.
	t.Add(owned(auth.DomainDB, auth.ActionWrite), ormGatewayRoutes()...)

	// One registered route serves every operation on a named function, and the
	// handler dispatches on the rest of the path. Its policy has to dispatch
	// the same way, so it is declared by the package that does the dispatching.
	t.AddDynamic("/v1/functions/", functionRoutePolicy)

	return t
}

// policyFor is the policy of the route this request matches.
func (g *Gateway) policyFor(r *http.Request) routepolicy.Policy {
	if p, ok := r.Context().Value(ctxKeyRoutePolicy).(routepolicy.Policy); ok {
		return p
	}
	return gatewayRoutes.For(r)
}

// routePolicyMiddleware resolves the policy once and puts it on the request, so
// the four places that read it agree by construction and the mux is walked once
// rather than once per middleware.
func (g *Gateway) routePolicyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, withRoutePolicy(r, gatewayRoutes.For(r)))
	})
}

// ormGatewayRoutes is the pattern list the rqlite ORM gateway registers under
// the gateway's base path.
func ormGatewayRoutes() []string {
	orm := &rqlite.HTTPGateway{BasePath: ormBasePath}
	return orm.Routes()
}

// policyInvoke is the policy of the HTTP invoke routes: open (a public function
// needs no credential; whether a caller may run a private one is the
// invoker's decision), but a grant narrowed to `fn:name=` is resolved and
// applied (narrowOpenRoute), so a narrowed wallet runs only its functions.
//
// A pending device's session reaches it, and the invoker runs for it only a
// function deployed open to pending devices.
var policyInvoke = func() routepolicy.Policy {
	p := policyOpen
	p.NarrowedByGrant = true
	p.PendingDevice = routepolicy.PendingFunctionInvoke
	return p
}()

// policyOwnSession is p, reachable by a pending device's restricted session
// because the route acts on that session or its device alone.
func policyOwnSession(p routepolicy.Policy) routepolicy.Policy {
	p.PendingDevice = routepolicy.PendingOwnSession
	return p
}

// functionRoutePolicy is the policy of one operation on /v1/functions/.
//
// Invoking is public: the invoker decides whether the caller may run the
// function, and a public function is open by design. The WebSocket is an invoke
// transport and takes the invoke grant — unless it is opened with a capability,
// which the handler checks itself before the upgrade (feat-264). Everything
// else — deploying, deleting, logs, triggers, secrets — is the control plane.
//
// The operation is read with the handler's own parser. It used to be read off
// the path's suffix, so `/v1/functions/{fn}/triggers/invoke` and
// `/v1/functions/secrets/invoke` were "invoke", public, and dispatched to the
// trigger and secret handlers.
func functionRoutePolicy(r *http.Request) routepolicy.Policy {
	switch {
	case serverlesshandlers.IsFunctionAction(r.URL.Path, "invoke"):
		return policyInvoke
	case isCapabilityUpgrade(r):
		return policyHandlerAuth
	case serverlesshandlers.IsFunctionAction(r.URL.Path, "ws"):
		// The socket handler refuses a pending device's session before the
		// upgrade unless the function is open to pending devices.
		p := owned(auth.DomainFn, auth.ActionInvoke)
		p.PendingDevice = routepolicy.PendingFunctionInvoke
		return p
	default:
		return owned(auth.DomainFn, auth.ActionManage)
	}
}

// ormBasePath is where the rqlite ORM gateway mounts. It is the same constant
// the routes use to mount it, so the declaration and the registration cannot
// name different paths.
const ormBasePath = "/v1/rqlite"

// ctxKeyRoutePolicy carries the matched route's policy on the request.
type routePolicyKey struct{}

var ctxKeyRoutePolicy = routePolicyKey{}

func withRoutePolicy(r *http.Request, p routepolicy.Policy) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyRoutePolicy, p))
}

// networkDetailPolicy is the policy of the network status routes: a request
// stamped as coming from another node is authenticated by the handler
// (verifyCoordination); any other request is an operator's.
func networkDetailPolicy(r *http.Request) routepolicy.Policy {
	if nodeauth.HasCoordinationStamp(r) {
		return policyHandlerAuth
	}
	return operatorRoute()
}

// deploymentRoute keeps a deployment route on the index gateway even when it
// is addressed to ns-<name>: the namespace still comes from the subdomain and
// the credential must belong to it.
func deploymentRoute(p routepolicy.Policy) routepolicy.Policy {
	p.MainGateway = true
	return p
}

// operatorRoute declares a route of the cluster operator: the scope gate admits
// only a wallet session on the operator list (requireOperatorCaller).
//
// It is always served by the index gateway. The list is in the cluster
// registry and is stripped from a namespace's RQLite, so the same request
// addressed to ns-<name> and proxied into the tenant's gateway could not be
// answered: it came back as a retryable 503, "the registry did not answer", to
// a caller who was simply not an operator. Served by the index gateway it is
// the 403 it should be, and a credential of another namespace is refused for
// the host it names.
func operatorRoute() routepolicy.Policy {
	return routepolicy.Policy{Operator: true, MainGateway: true}
}

// credentialServesHostNamespace refuses a request to a route the index gateway
// serves for an ns-<name> host when the credential belongs to another
// namespace. A namespace gateway refuses a foreign credential on its own; the
// index gateway serves every namespace, so the host is the only thing that
// says which one the caller meant.
func credentialServesHostNamespace(w http.ResponseWriter, r *http.Request) bool {
	host, _ := r.Context().Value(hostNamespaceKey{}).(string)
	if host == "" {
		return true
	}
	credential, _ := r.Context().Value(CtxKeyNamespaceOverride).(string)
	if strings.EqualFold(strings.TrimSpace(credential), host) {
		return true
	}
	forbidden(w, CodeNamespaceMismatch, "this credential belongs to another namespace",
		map[string]any{"namespace": host, "credential_namespace": credential})
	return false
}
