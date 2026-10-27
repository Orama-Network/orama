# Compute and realtime

> **At a glance.**
>
> - **What:** three per-namespace services layered on the namespace gateway. Serverless functions run tenant WebAssembly in a wazero sandbox inside the gateway. Push notifications route a message to a device through APNs, Expo or ntfy, using the namespace's own credentials. WebRTC runs a pion selective forwarding unit (SFU) on each member node and one shared TURN relay per host.
> - **Key numbers:** functions have 1 to 256 MB (default 64) and 1 to 60 s (default 30), reach the platform through 55 host functions, and run five at a time per namespace gateway. Push dispatchers are cached 30 s and rotating topics live 7 to 8 days. A WebRTC room holds at most 100 peers, a join ticket lives 30 s, TURN listens on 3478 and 5349 with relay ports 49152-65535.
> - **Code:** `core/pkg/serverless/`, `core/pkg/push/`, `core/pkg/sfu/`, `core/pkg/turn/`.

![Serverless functions: callers, the namespace gateway and what a function can reach](../technical-reference/diagrams/ch21-overview.svg)

## One pattern, three services

A tenant application needs server logic near its data, a way to wake a phone and a way to carry voice and video, all on shared hosts. They share one answer: policy lives in the tenant's own namespace (gateway, database, secrets), and the platform exposes narrow doors whose namespace comes from server-side context, never from a caller's argument.

## Serverless functions

A function is a TinyGo program compiled to WASM, sealed and stored in IPFS, and run by the gateway of the namespace that owns it, against that namespace's own database. The invoker refuses any other namespace, the database host functions check that the gateway's database belongs to the invocation, and the cluster gateway proxies tenant traffic away. The cluster gateway once ran every tenant's functions against the shared registry, where one function could rename another tenant's namespace.

### Shapes and sandbox

A command function reads JSON on stdin, writes JSON on stdout and gets a fresh instance per call, with memory capped by an allocator that makes `memory.grow` return -1. A reactor exports `handle`; because a cold TinyGo `_initialize` costs about 550 ms, the engine keeps up to two pre-initialised one-shot instances per module. A persistent function holds one instance for a WebSocket's lifetime (5,000 per gateway). Instances are never reused: the pool moves the cold start, not the isolation.

A guest sees stdin, stdout, the host clocks and the host CSPRNG: no filesystem, environment or sockets. wazero has no fuel metering, so CPU is bounded indirectly by the deadline, the memory cap, a slot semaphore (10 per process, 5 per namespace), per-invocation budgets such as 1,000 publishes, and token-bucket rate limits (10 per second per wallet with burst 60, 2 per second per address, 250,000 a minute per namespace).

### Deploy and invoke

Deploy records the uploaded WASM's SHA-256 (`wasm_sha256`; the sealed blob's CID differs per deploy), seals the WASM under the cluster wrap key, pins it on every IPFS Cluster peer, and fails unless the cluster reports it pinned within 30 s; a garbage collection once deleted functions pinned on only three peers. Each deploy inserts a new version row and supersedes the one before it; ten are kept. Only the current version runs: `name@N` of a superseded version is refused with 404 `FUNCTION_VERSION_SUPERSEDED`, and a rollback makes an earlier version current again. A purge, for the owner or an admin, removes a function's rows, triggers and history for good. The version insert and trigger hand-over are separate statements, so a deploy is not atomic.

Invocation checks that the namespace is served here, then authorizes: an internal function is admin-only, a public one is open, any other needs the invoke grant, which a signed-in wallet or a key with the `invoke` scope holds. Retries double the delay, up to five, and a final failure can publish to a dead-letter topic. A function past its deadline answers 504 `FUNCTION_TIMEOUT`, which is not retryable because the function ran; a rate limit, which refused the call before it ran, answers 429.

### Triggers

Two trigger kinds start an invocation with no caller. Every gateway polls cron rows every 30 s, and a conditional `UPDATE` on `next_run_at` is the lease: the winner invokes, the losers skip. The claim precedes the invocation, so a crash loses a slot rather than repeating it. Pub/sub triggers are deduplicated by an `NX` write in the namespace's Olric for 30 s; when Olric is down the claim fails open, because a duplicate wake-up harms less than a dropped one. A trigger chain stops at depth 5, recorded beside the message because the wire format carries only the payload. GossipSub has no wildcard subscription, so wildcard triggers fire only for publishes seen on the same gateway.

### Host functions and egress

The 55 host functions cover the namespace database, cache, pub/sub, push, WebRTC control, HTTP and nested invocation. A function is the namespace's own code, so host calls do not re-check the caller.

Outbound `http_fetch` runs from the cluster's own network, so the guard sits where the address is final: a dial-time check on every resolved address and redirect hop refuses loopback, private, link-local and carrier-grade ranges and the chain's block. A name that resolved to an overlay address once passed a URL check and reached the mesh. `anon_fetch` goes through the node's Tor client with no direct fallback, so it fails loudly rather than leak the gateway's address.

## Push notifications

The platform holds no Apple developer account. Each namespace supplies its own APNs key, Expo token or ntfy server, sealed at rest with AES-256-GCM, and `push.Manager` builds one dispatcher per namespace. The 30 s dispatcher cache is also the only cross-gateway invalidation, so a rotated key takes effect cluster-wide within about a minute.

### Two ways to register a device

The account path stores a sealed provider token under `(namespace, user, device)`. An HMAC fingerprint enforces one owner per physical token, most recent registration winning, and rows bound to a revoked session device are dropped on every send.

The topic path stores a device under an address it chose. The device generates a secret of 16 to 64 bytes and the topic id is its SHA-256; whoever holds the id can send, and only the secret can re-point or delete. The table has no user column and rounds expiry to a UTC midnight, so a registration lives 7 to 8 days without recording its second. Rotation hides a device from senders, not from the platform, whose provider token is stable.

A send is one synchronous attempt per device, with no queue, retry or receipt; the caller owns retry. An empty APNs alert is refused locally, because Apple answers 200 and drops it. VoIP pushes expire after 30 s so a call invite never arrives as a phantom missed call.

### ntfy across nodes

Every node runs a loopback ntfy on `127.0.0.1:10109` with no shared store, so a message exists only on the node that received it. The sender publishes to all nodes over WireGuard, through the index gateway on port 10104, each request carrying a coordination MAC over the body and the target's peer id; one acceptance is success. Subscribers converge on one instance because the nameserver nodes pin `push.<zone>` to the healthy nameserver with the lowest IP (TTL 60 s). A tenant-supplied ntfy URL is checked at write, at build and at dial.

## WebRTC

WebRTC is opt-in per namespace. Enabling it generates a 32-byte TURN secret, allocates ports on the three member nodes, starts an SFU on each, and publishes TURN DNS for the hosts that confirm they serve the namespace.

![WebRTC: signalling, placement, media and the shared TURN](../technical-reference/diagrams/ch23-overview.svg)

### Placement and join

A room lives in exactly one SFU, which every gateway must find without coordination. The gateway ranks the namespace's SFU nodes by SHA-256 of namespace, room and node id (rendezvous hashing), probes each `GET /health?room=` with a 1.5 s timeout, and picks the first healthy node already hosting the room, else the top-ranked healthy one. Nothing is written, so no lease can go stale. With no healthy SFU the join fails, because a local fallback would split the call.

The gateway authenticates the user, checks admission in the namespace database when the namespace requires it, and signs a 30 s ticket (HMAC under a key derived from the TURN secret) naming user, device, room, audio and video mute flags and admission end. The user is the token's `account_id` claim, else its subject, so peers never see a wallet the namespace named an account for. It proxies the socket to the SFU's WireGuard address. The SFU takes identity only from the ticket, and kicks the peer when its admission ends.

### Media and TURN

The SFU forwards RTP without decoding, with no simulcast or bandwidth estimation of its own, and its own ICE policy is relay-only, so media leaves the node through the public TURN address. Rooms hold at most 100 peers. When the SFU and a client offer at once, the SFU yields with a stand-in answer, because pion has no rollback.

TURN is one `orama-turn` process per host serving every namespace on it. The first design ran one process per namespace, and the second on a node crash-looped on the well-known port. Isolation moved to the credential: the username is `EXPIRY:NAMESPACE` and the password is an HMAC-SHA1 under that namespace's secret, so a credential for one tenant cannot authenticate as another. The tenant list is re-read every 2 s by file hash, and a failed reload keeps the previous set, because changing tenants must never restart the process and drop everyone's relays.

### Kick, mute and role placement

A kick revokes the user's admissions first, then posts to every SFU of the namespace, not only the owner, since probes can change who hosts a room. Each SFU keeps a kick log that compares admission generations, so a user re-admitted after a kick can rejoin at once. A kick can target one device alone, by its session device id. Audio and video mutes are separate, enforced per packet, and survive rejoin. Control calls carry a MAC with a nonce, valid 60 s and refused on reuse.

A reconciler on every node runs each 60 s to match SFU and TURN placement to the allocation table. Only the lowest-sorted live member reallocates, and only under a strict majority of viable members. Stopping a unit needs a clean read showing no allocation, so a registry error never causes an outage.

## How it fails

A WASM block missing from a node's Kubo yields a retryable 503 after bounded reads and one re-pin. A gateway crash loses its in-flight invocations and any claimed cron slot. With Olric down, cache calls return 0 and triggers may fire twice. A dead SFU fails its probe, and peers reconnect through any gateway to the next-ranked node. Membership events reach subscribers at most once.

## Trust

A function can use its namespace's database (minus platform tables), read every secret of the namespace, push to any user in it and call any function in it. It cannot reach another namespace, a reserved address, a file or a socket. A runtime API key carries `push:write`, so a leaked one can push to any user of its namespace; WebRTC data-plane routes refuse an API key alone. The TURN server sets no peer permission handler, so a client with a valid credential can ask it to send UDP to the host's own loopback and overlay addresses.

The limit that matters most is shared capacity: each namespace gateway runs five command-mode functions at once, and every tenant on a host shares one TURN relay range of 16,384 ports.
