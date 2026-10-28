# API surface

> **At a glance.**
>
> - **Hand-written.** Who owns each gateway route: the SDK, the CLI, a direct caller or another node. [Gateway routes](c-gateway-routes.md) is the generated list of the same routes with their policy; this table is the owner of each and the reason a route is not in the SDK.
> - **Owners:** SDK 43, CLI 84, direct 30, internal 23.

Every route the gateway registers, and which client owns it. Humans use the CLI, programs use the SDK and this HTTP API, and there is no Orama dashboard ([the CLI](../vol1/35-the-cli.md), [SDKs](../vol1/36-sdks.md)). The TypeScript SDK's coverage is a decision rather than an accident: it reaches 43 of 180 routes, and the other 137 are here with a reason.

The route set in this appendix and the set the gateway registers must be identical. The test `core/pkg/gateway/api_surface_test.go` reads the first two cells of every table row whose first cell is a backticked path, so a registered route missing here fails, and so does a documented route no longer registered. Adding a route means deciding who calls it.

| Owner | Meaning | Count |
|-------|---------|-------|
| `SDK` | `@orama-network/orama` calls it | 43 |
| `CLI` | The `orama` CLI calls it. An application has no reason to: deploying, minting keys and managing nodes are operator actions. | 84 |
| `direct` | Reachable by a client, but not through the SDK by design. The reason is in the row. | 30 |
| `internal` | Node-to-node over the WireGuard overlay. Never reachable by a client. | 23 |

The request and response shapes of the `SDK` routes are pinned by the fixtures in `contracts/`, which both a Go handler test and a TypeScript unit test read, so a shape change on either side fails without a cluster.

### Health and version

| Route | Owner | Notes |
|-------|-------|-------|
| `/.well-known/jwks.json` | direct | JWKS for verifying gateway-issued JWTs. Read by other services, not by an application. |
| `/health` | SDK | `network.health()`. Open: the overall status and each check's status, nothing else. The detail — latencies, errors, the namespaces hosted here and their ports — is `/v1/operator/health`. |
| `/status` | direct | Open. A browser (`Accept: text/html`) gets the public status page; anything else gets the same JSON as `/v1/status`. |
| `/status/assets/` | direct | Open. The status page's script and stylesheet, served under a CSP that allows nothing else. |
| `/v1/health` | direct | Same as `/health`, kept for older callers. |
| `/v1/schema-status` | CLI | Migration state, polled during provisioning. Reads the tracker of the database the gateway serves: `schema_migrations` on the index gateway, `orama_schema_migrations` on a namespace gateway (whose `schema_migrations` belongs to the tenant). A database with no leader or past its deadline is a retryable 503. |
| `/v1/status` | direct | Open: `status` and `server` (up since when), and on a cluster gateway the public view of the network — overall state and headline, node counts, each service's state and 90-day daily uptime, the chain's height, block time and validator shares, and network request rate, error rate and p95. No node address, peer id, hostname or error text (`cluster.PublicStatus`). Cached 5s. Per-node detail is `/v1/operator/telemetry`; peer ids and addresses are `/v1/network/status`, for operators. |
| `/v1/version` | CLI | Build version. `orama version` and the upgrade checks read it. |

### Authentication

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/auth/api-key` | SDK | `auth.getApiKey()` |
| `/v1/auth/challenge` | SDK | `auth.challenge()` |
| `/v1/auth/device` | CLI | Starts a login from a machine with no wallet on it (RFC 8628). Returns a device code the machine polls with and a short code the human approves. `orama auth login` on a server or in CI. |
| `/v1/auth/device/approve` | CLI | Approves — or, with `deny`, refuses — a pending device login. Costs a wallet signature over the gateway's own challenge, exactly as `/v1/auth/verify` does. `orama auth approve <code>`. |
| `/v1/auth/device/token` | CLI | The waiting machine's poll. Answers `authorization_pending`, `slow_down`, `expired_token` or `access_denied` until it is approved, then returns the session once; a code that names no pending login, or whose session was already collected, is `invalid_grant`. |
| `/v1/auth/jwks` | direct | JWKS. See `/.well-known/jwks.json`. |
| `/v1/auth/logout` | SDK | `auth.logout()` |
| `/v1/auth/refresh` | SDK | Session renewal, called by the client on a 401. |
| `/v1/auth/renew` | SDK | A deployment renewing its own workload token with the token it is holding. Refuses anything that is not a workload token. |
| `/v1/auth/sessions` | CLI | The live sessions signed in as the calling wallet. Never returns a refresh token. `orama auth sessions`. |
| `/v1/auth/sessions/` | CLI | `DELETE /v1/auth/sessions/{id}` ends one session, its access tokens and the sockets they hold open with it (a session issued before sessions carried an id keeps its access tokens until they expire, and the response says so). `orama auth sessions revoke <id>`. |
| `/v1/auth/devices` | SDK | The calling account's devices, revoked ones included. `auth.listDevices()`. |
| `/v1/auth/devices/` | SDK | `DELETE /v1/auth/devices/{id}` revokes one device — its sessions, access tokens and sockets — and nothing else, keeping the revoker's proof and optional `evidence` for the revoked device's `DEVICE_REVOKED`; `POST /v1/auth/devices/approve` approves a device link from the calling device. `auth.revokeDevice()`, `auth.approveDeviceLink()`. |
| `/v1/attest/android` | direct | An Android key-attestation chain over `SHA-256(challenge ‖ did)`; answers a token carrying the attestation. Called by the app's own attestation code, which the SDK does not wrap: the proof comes from the platform. See [Authorization](../vol1/14-authorization.md#device-attestation-at-the-gateway). |
| `/v1/attest/challenge` | direct | A single-use, five-minute challenge for the calling session's device. As `/v1/attest/android`. |
| `/v1/attest/ios` | direct | An App Attest attestation over `SHA-256(challenge ‖ did)`; answers a token carrying the attestation. As `/v1/attest/android`. |
| `/v1/auth/token` | direct | Exchange an API key for a JWT. A server-side concern; the SDK sends the key itself. |
| `/v1/auth/verify` | SDK | `auth.verify()` |
| `/v1/auth/whoami` | SDK | `auth.whoami()` |
| `/v1/audit` | CLI | The namespace's record of who was given what and when. Admin grant; the namespace comes from the credential, never the query string. `?action=`, `?principal=`, `?since=` and `?limit=` narrow it (50 by default, 200 at most). `orama audit [--follow]`. |
| `/v1/operator/audit` | CLI | The cluster's own audit events: the rows recorded under no namespace (invites minted, nodes joining and claimed, namespaces deleted, by their owner or by an operator). Operator route. The same filters and page as `/v1/audit`; the answer carries `"scope":"cluster"`. `orama audit --cluster [--follow]`. |

### Database (RQLite)

Every ORM route below (`query`, `exec`, `find`, `find-one`, `select`, `transaction`, `create-table`, `drop-table`) reads at most 4 MiB of request body (`rqlite.MaxRequestBodyBytes`): a larger one is `413` before any statement is parsed, an unparsable or blank one `400`.

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/rqlite/create-table` | SDK | `db.createTable()` |
| `/v1/rqlite/drop-table` | SDK | `db.dropTable()` |
| `/v1/rqlite/exec` | SDK | `db.exec()` |
| `/v1/rqlite/export` | CLI | Native RQLite backup. `orama db backup`. |
| `/v1/rqlite/find` | SDK | `db.find()`, `Repository.find()` |
| `/v1/rqlite/find-one` | SDK | `db.findOne()`, `Repository.findOne()` |
| `/v1/rqlite/import` | CLI | Native RQLite restore. The body must be a SQLite database file. On a namespace gateway (owner only) it is spooled, capped at 256 MiB (`413`: at once when the length is announced, after 256 MiB when it is not), and checked offline in SQLite before RQLite loads it: a damaged file, a trigger, a view over a platform table or an ownership row for content only other namespaces hold is refused `400`, nothing written. After the load the gateway removes the passive rows an older backup may carry (plaintext API keys, ownership rows of other namespaces) and checks again, as a backstop, and the destination's storage quota is kept. Shares the gateway's one transfer slot with backup and restore (`429`) and has the five-minute budget. On the cluster gateway it is the operator's whole-registry replacement: streamed, uncapped, SQLite header only. |
| `/v1/rqlite/query` | SDK | `db.query()` |
| `/v1/rqlite/schema` | SDK | `db.getSchema()` |
| `/v1/rqlite/select` | SDK | `QueryBuilder.getMany()` / `getOne()` |
| `/v1/rqlite/transaction` | SDK | `db.transaction()` |

On a namespace gateway, SQL sent to `/v1/rqlite/*` (exec, query, each transaction op, the statement `select` and `find` build, create-table, drop-table) may not name a platform table such as `grants`, `api_keys`, `ipfs_content_ownership` or `deployments`, however it is quoted. A request that does is refused with `403` and `code: SQL_NOT_ALLOWED`, whoever the caller is. The list is in [the SQL guard](../vol1/17-database.md#the-sql-guard) and [serverless](../vol1/21-serverless.md). The cluster gateway's `/v1/rqlite/*` is an operator's and is not filtered.

### Cache

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/cache/delete` | SDK | `cache.delete()` |
| `/v1/cache/get` | SDK | `cache.get()` |
| `/v1/cache/health` | SDK | `cache.health()` |
| `/v1/cache/mget` | SDK | `cache.multiGet()` |
| `/v1/cache/put` | SDK | `cache.put()` |
| `/v1/cache/scan` | SDK | `cache.scan()`. `match` is a regular expression on the key; invalid is a 400. |

A namespace's cache is one Olric DMap with the `dmap` name folded into each key, so every `dmap` shares one memory bound; `dmap` and `key` together are at most 255 bytes (the 413 names the limit for that `dmap`). See [the cache](../vol1/18-cache.md).

### Pub/sub

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/pubsub/presence` | SDK | `pubsub.getPresence()` |
| `/v1/pubsub/publish` | SDK | `pubsub.publish()` |
| `/v1/pubsub/publish-batch` | direct | Publishing many messages in one request. Worth adding to the SDK when an application needs it; nothing does today. |
| `/v1/pubsub/topics` | SDK | `pubsub.topics()` |
| `/v1/pubsub/ws` | SDK | `pubsub.subscribe()` |

### Storage

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/storage/fetch-caps` | SDK | `POST` mints `count` (1 to 64) fetch capabilities for one CID the namespace owns, `ttl_seconds` 3600 to 604800: body `{"cid","count","ttl_seconds"}`, answer `{"namespace","cid","caps":[{"id","token","revoke_key","expires_at"}]}` (`expires_at` in Unix seconds; `revoke_key` is what revokes that id). Needs the storage-read grant and a device-bound session (`403 FETCH_CAP_DEVICE_REQUIRED` for an API key or workload token); the CID must be in canonical form. See [storage](../vol1/19-storage.md#fetch-capabilities). |
| `/v1/storage/fetch-caps/` | SDK | `DELETE /v1/storage/fetch-caps/{id}` with `X-Orama-Revoke-Key: <revoke_key>` (the key the mint returned for that id) revokes one capability: `200 {"revoked":"<id>"}`; no key or another id's is `403 FETCH_CAP_REVOKE_KEY_INVALID`; a namespace already holding 1,000 capability revocations in force is `429 FETCH_CAP_REVOCATION_LIMIT`; an id that is not 32 hex characters is `400`. |
| `/v1/storage/relayed/` | SDK | `GET /v1/storage/relayed/{cid}` with `X-Orama-Fetch-Cap: <token>` and no credential: the body and headers of `/v1/storage/get/{cid}`, with a `Content-Length` always. Reached through `/v1/proxy/relay`. `401 FETCH_CAP_MISSING`, `403 FETCH_CAP_INVALID` (forged, expired, another CID or namespace, not a fetch capability), `403 FETCH_CAP_REVOKED`, `400 FETCH_CAP_NOT_ALONE` (a credential beside it), `503 FETCH_CAP_UNAVAILABLE`; every one `{error, code, hint}`. The request log row of this route has no address (`LogNoAddress`). |
| `/v1/storage/get/` | SDK | `storage.get()`, `storage.getBinary()`. A CID that does not parse, or is not in canonical form (a NUL or a space included), is `400 VALIDATION_FAILED`. On `get`, `status`, `pin` and `unpin`, a namespace database with no leader or past its deadline is a retryable `503`, not a `500`. |
| `/v1/storage/pin` | SDK | `storage.pin()` |
| `/v1/storage/status/` | SDK | `storage.status()` |
| `/v1/storage/unpin/` | SDK | `storage.unpin()` |
| `/v1/storage/upload` | SDK | `storage.upload()` |

### Functions

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/functions` | CLI | Deploy and list functions. `orama function deploy` / `list`. |
| `/v1/functions/` | CLI | Per-function management: info, delete, versions, logs, secrets, triggers, and the streaming invoke socket. `orama function …`. |
| `/v1/invoke/` | SDK | `functions.invoke()` |
| `/v1/serverless/ws/connections` | internal | WebSocket connection registry for streaming invokes. |
| `/v1/serverless/ws/connections/` | internal | One WebSocket connection by id. |

### Network and proxy

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/network/connect` | CLI | Topology mutation: `{multiaddr}` (`/ip4/.../tcp/.../p2p/<peer id>`). An operator's (a wallet session on the operator list, checked before the body is read, so anyone else is `403 NOT_AN_OPERATOR`); served by the index gateway like `/v1/network/peers`. A malformed body or an address that names no peer is 400, a body over 4 KiB 413, a dial the peer or network refuses 502, one that outlasts 10 s 504. |
| `/v1/network/disconnect` | CLI | Topology mutation: `{peer_id}`. Same access and refusals as connect; disconnecting a peer that is not connected is a 200 no-op. |
| `/v1/network/peers` | SDK | `network.peers()`. An operator's (a wallet session on the operator list), or another node's with a coordination MAC over the mesh. It was open to anyone. Every operator route (these, `/v1/node/status`, `logs`, `command` and `leave`, `/v1/operator/*`) is served by the index gateway even when the host is `ns-<name>`, because the list is in the cluster registry and not in a namespace's database: a namespace credential is refused `403 NOT_AN_OPERATOR`. |
| `/v1/network/status` | SDK | `network.status()`. Same as `/v1/network/peers`: an operator, or a node's IPFS Cluster peer discovery (`pkg/ipfs`) with a coordination MAC. |
| `/v1/proxy/anon` | SDK | `network.proxyAnon()` |
| `/v1/proxy/relay` | SDK | Anonymous, destination-pinned WebSocket tunnel of a relayed fetch: `?host=<ns host>&port=443[&circuit=session]`, no credential. Binary frames carry raw TCP bytes in both directions; a text frame ends the stream; the destination closing its side closes the socket. Refused before the upgrade with `{error, code, hint}`: `400 RELAY_DESTINATION_NOT_ALLOWED`, `429 RATE_LIMITED` (`Retry-After`), `503 RELAY_UNAVAILABLE` (Tor down, or the destination not reached through it; never a direct dial). The host is a plain ASCII hostname under the relay's allowed suffixes; `429` also answers an address already holding 4 open streams. No request log row or line is written for it (`LogNone`). See [the gateway](../vol1/12-gateway-architecture.md) and [relayed fetch](../vol1/36-sdks.md#relayed-fetch). |
| `/v1/proxy/tunnel` | direct | Raw CONNECT-style tunnelling through the anonymity proxy. Not a JSON call; the SDK has nothing to wrap. |

### Vault

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/vault/health` | direct | Aggregate guardian health. |
| `/v1/vault/pull` | direct | Retrieve a secret. Ed25519-signed per request. |
| `/v1/vault/push` | direct | Store a secret. Ed25519-signed per request; see [the vault](../vol1/28-vault.md). Deliberately not in `@orama-network/orama`. |
| `/v1/vault/status` | direct | Guardian count and threshold. |

### WebRTC

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/webrtc/config` | direct | `GET` and `PUT {"require_admission": bool}`: the namespace's WebRTC policy. With it on, a room admits only users the namespace's functions admitted (`webrtc_admit`). A credential that may change the namespace's settings; [WebRTC](../vol1/23-webrtc.md). |
| `/v1/webrtc/rooms` | direct | GET only: the SFU health JSON (status, room count). |
| `/v1/webrtc/signal` | direct | SFU signalling. |
| `/v1/webrtc/turn/credentials` | direct | Short-lived TURN credentials. Consumed by a WebRTC stack, not by this SDK; the SDK would only pass them through. |

### Push notifications

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/namespace/push-credentials` | direct | Push credential management, an owner's setting made over HTTP; the CLI has no command for it. |
| `/v1/namespace/push-credentials/` | direct | One push credential (`PUT`/`DELETE .../{provider}`). The CLI has no command for it. |
| `/v1/push/config` | direct | Legacy per-namespace push credentials, superseded by `/v1/namespace/push-credentials/`. The CLI has no command for it. |
| `/v1/push/devices` | direct | Register a device for push. A mobile client concern; a native SDK owns it, not this one. |
| `/v1/push/devices/` | direct | One registered device. |
| `/v1/push/send` | CLI | Server-side send, on the namespace's `push:send` permission (a developer or above, or a key minted with `push-send`; not the `runtime` role); a function or a backend calls it directly. |
| `/v1/push/topics` | direct | Register (POST) or remove (DELETE) a rotating push topic, proved by its secret. A mobile client concern, as `/v1/push/devices` is. |
| `/v1/push/topics/send` | direct | Server-side send to a topic, with `/v1/push/send`'s grant; a backend calls it directly, and a function uses `push_send_topic`. |

### Namespace management

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/namespace/backup` | CLI | Owner only. The namespace's RQLite snapshot, pinned CIDs and decrypted secrets, sealed to the X25519 public key in the body. The gateway never sees the private key. Namespace gateways only; one backup, restore, export or import at a time per gateway (429 otherwise), each with a five-minute budget; a database over 256 MiB or more than 50,000 pins is refused (413). `orama namespace backup`. |
| `/v1/namespace/delete` | CLI | Destroy a namespace. Idempotent: a namespace whose cluster is already gone is removed too. The removal does not stop when the client disconnects, and a cluster left in `deprovisioning` by an interrupted delete is finished by the tenant reconciler. 409 `NAMESPACE_DELETE_IN_PROGRESS` (`retryable`, `Retry-After`) when a delete of the namespace is already running, on this gateway or another. 200 with `cleanup_pending: true` when a node's teardown is still owed (recorded in `namespace_pending_cleanup`, replayed until the node confirms; the name cannot be created again and the node's port blocks stay reserved meanwhile); 500 `retryable` when the cluster could not be deprovisioned or an owed teardown could not be recorded. |
| `/v1/namespace/keys` | CLI | Mint and list scoped API keys. `orama namespace keys`. |
| `/v1/namespace/keys/` | CLI | Revoke a key. |
| `/v1/namespace/list` | CLI | Namespaces owned by the calling wallet. |
| `/v1/namespace/members` | CLI | Who else may work in this namespace, and at what role. `orama members list` or `add`. |
| `/v1/namespace/members/` | CLI | Remove a member, or transfer the namespace. `orama members remove|transfer`. A transfer to a wallet already at the per-wallet cap is a generic `403 TRANSFER_REFUSED` that names neither the wallet nor the limit (they are in the audit trail), and writes nothing (the refusal itself is one bit about the recipient: it cannot take another namespace); 503 when the cap cannot be read. |
| `/v1/namespaces` | CLI | Create a namespace: writes the owner grant and starts provisioning. Who may call it is `namespace_creation` (`operators`, `allowlist`, or `open`). A new cluster is `operators`; one that already had data stays `open` until an operator changes it. Per-wallet cap defaults to 10. 409 `NAMESPACE_TEARDOWN_PENDING` (`retryable`, `Retry-After`) when a deleted namespace of that name is still owed a teardown on an active node; 503 `NAMESPACE_PROVISION_FAILED` with a fixed message when the cluster could not be started (the cause is logged only); 503 `NAMESPACE_CAPACITY` when no node has room for another cluster (every node's namespace port range is full, or too few nodes report), which retrying does not clear. `orama namespace create`. |
| `/v1/namespace/cors-origins` | CLI | The browser origins the namespace's apps are served from, beyond the built-in rule (the base domain and the names under it, localhost, `*.localhost`, loopback addresses, the Tauri webviews). `GET` reads `{"namespace","origins"}`, `PUT {"origins": [...]}` replaces the list (canonical `scheme://host[:port]`, http, https or tauri, no path or wildcard, at most 20; an empty list clears it). `namespace:write` (an owner or an admin); index gateway. Applies to requests to `ns-<name>.<base>`: at once on the recording gateway, within 30 s on every other. `orama namespace cors-origins`. |
| `/v1/namespace/rate-limit` | CLI | Per-namespace rate limit. |
| `/v1/namespace/restore` | CLI | Owner only. Replaces the namespace's RQLite with a backup (the live keys and grants, in the cluster registry, are not touched; the image is scrubbed after the load), writes its secrets under this cluster's encryption root, keeps this cluster's storage quota and pins its CIDs. Refused before any write when over that quota (413) or when RQLite cannot batch (503). The secrets arrive sealed to `/v1/namespace/restore-key`. `orama namespace restore`. |
| `/v1/namespace/restore-key` | CLI | This gateway's X25519 restore public key for the namespace, derived from the cluster's current encryption root and the namespace name. `orama namespace restore-key`. |
| `/v1/namespace/devices` | direct | An operator's list of one account's devices (`?subject=<wallet>`), for recovering an account under the `approval` policy. The members-write permission. No CLI command. See [Identity](../vol1/13-identity.md). |
| `/v1/namespace/devices/` | direct | `DELETE /v1/namespace/devices/{id}` — an operator revokes a device of any account in the namespace. The members-write permission. No CLI command. |
| `/v1/namespace/attestation-policy` | CLI | `orama namespace attestation-policy`. `GET` reads, `PUT` changes the named fields of the namespace's device attestation policy (mode `off`, `monitor`, `enforce`; the iOS and Android app; minimum builds; re-attestation interval), validated whole. The namespace-write permission. See [Authorization](../vol1/14-authorization.md#device-attestation-at-the-gateway). |
| `/v1/namespace/session-policy` | CLI | `orama namespace session-policy`. `GET` reads, `PUT` sets `device_policy` (`optional`, `required`, `approval`: whether end-user sessions must be bound to a device, and whether a new device needs an existing one's approval) `sign_in` (`members`, the default, or `open`: whether a wallet holding no grant may sign in as an end user with no key), `end_user_topics`, and the per-IP limits `per_ip_limits` (`on` or `off`, the default), `new_wallets_per_ip_hour` (default 20) and `invokes_per_ip_minute` (default 1,200); any of them, a field left out keeps its value. The namespace-write permission. See [Identity](../vol1/13-identity.md). |
| `/v1/namespace/status` | CLI | Provisioning progress for a cluster id: the `poll_url` that `POST /v1/namespaces` returns. `orama namespace create` does not wait on it; `orama namespace list` shows each cluster's status. Errors: 400 without `id`, 404 `cluster not found` for an id that is not in the registry, 503 when the registry could not be read (the cluster may exist; retry; the cause is in the gateway log with the cluster id and is not in the response). |
| `/v1/namespace/webrtc/disable` | CLI | `orama namespace disable webrtc`. |
| `/v1/namespace/webrtc/enable` | CLI | `orama namespace enable webrtc`. |
| `/v1/namespace/webrtc/status` | CLI | `orama namespace webrtc-status`. |
| `/v1/namespace/webrtc/stealth/disable` | CLI | Stealth TURN. |
| `/v1/namespace/webrtc/stealth/enable` | CLI | Stealth TURN. See [SNI routing and stealth TURN](../vol1/26-sni-routing-and-stealth-turn.md). |

### Deployments

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/deployments/delete` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/domains/add` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/domains/list` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/domains/remove` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/domains/verify` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/env` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/grants` | CLI | What a deployment may do as itself. `orama app grants list\|set`. Admin grant; a deployment cannot be granted the control plane. |
| `/v1/deployments/env/set` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/events` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/get` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/go/update` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/go/upload` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/list` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/logs` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/nextjs/update` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/nextjs/upload` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/nodejs/update` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/nodejs/upload` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/rollback` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/static/update` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/static/upload` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/stats` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |
| `/v1/deployments/versions` | CLI | Application deployment. `orama deploy` and friends; an application does not deploy itself. |

### Application databases

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/db/sqlite/backup` | CLI | Per-application SQLite databases. `orama db …`. |
| `/v1/db/sqlite/backups` | CLI | Per-application SQLite databases. `orama db …`. |
| `/v1/db/sqlite/create` | CLI | Per-application SQLite databases. `orama db …`. |
| `/v1/db/sqlite/delete` | CLI | Per-application SQLite databases. `orama db …`. |
| `/v1/db/sqlite/list` | CLI | Per-application SQLite databases. `orama db …`. |
| `/v1/db/sqlite/query` | CLI | Per-application SQLite databases. `orama db …`. |

### Node and operator

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/node/command` | CLI | Operator command on a node. Like `/v1/node/status`, `logs` and `leave`, and every `/v1/operator/*` route, it is an operator route: only a wallet session on the cluster's operator list passes, and a namespace owner, an admin or any API key, even one of a namespace an operator owns, is `403 NOT_AN_OPERATOR`. |
| `/v1/node/enroll` | CLI | A node joins with an invite token. `orama maint node install`. |
| `/v1/node/leave` | CLI | `orama node remove`. |
| `/v1/node/logs` | CLI | `orama node logs`. |
| `/v1/node/status` | CLI | `orama node status`. |
| `/v1/chain/` | SDK | Proxy of CometBFT status, blocks, transactions, validators, norama supply, the staking pool and the staking validator list, and under `/v1/chain/index/` the chain indexer's status, a one-read `home` snapshot of what the explorer's home page shows, blocks and pages of them, transactions and pages of them (`filter` `all`, `failed`, `transfers`, `staking` or `storage`), hourly statistics, per-address summaries and transactions, cNFT assets by id and by owner, and the aggregates the indexer computes as it follows the chain (the closed emission epochs and their validators, what each epoch paid each validator and what an account or a validator was credited, the supply breakdown per epoch, each validator with its epochs, slashes and jail periods, and hourly, daily and weekly statistics; every list takes `page` up to 1000 and `limit` up to 100, except the five that grow with the chain or that one account can make long, `index/txs`, `index/accounts/{addr}/txs`, `index/blocks`, `index/accounts/{addr}/rewards` and `index/cnft/owners/{addr}/assets`, whose `page` stops at 10 and which go deeper by `cursor=<next_cursor>` (16 characters on the first three, 59 on the assets, 39 to 580 on the rewards), never with `page`; a successful index answer is `Cache-Control: public, max-age=2` when it depends on the head and `public, max-age=600` for a block, a transaction by hash or a closed epoch, and every error is `no-store`), `GET /v1/chain/head/stream`, the chain's head as server-sent events (`event: head` with `{height, time, chain_id}`, `event: unavailable` when the node cannot be read; one shared read of the node's status a second apart per gateway; at most 1,024 open streams per gateway, `503` with `Retry-After` beyond; 12 opens a minute, burst 6, and four open at once per client network; ends after 100 seconds); reads under `/v1/chain/index/` have a bucket of 1,200 a minute (burst 240) per client network and at most 32 in flight per gateway (`503` with `Retry-After` beyond), and under `/v1/chain/query/<package.Service>/<Method>` the Orama modules' Query services (x/nodes, x/storage, x/fees, x/archive, x/relay, ...) through CometBFT `abci_query`, answered as decoded JSON (`data=` base64 protobuf or `json=`, optional `height=` within the last 100 blocks; only an explicit list of bounded Query methods, never `Invariants` or an unbounded scan, no Msg or transaction paths, no proofs; its own rate-limit bucket and a cap of 16 in flight), the bounded cosmos-sdk and wasmd queries a wallet reads there (bank `Balance`, `AllBalances`, `SpendableBalances`; auth `Account`, `AccountInfo`; staking `Delegation`, `DelegatorDelegations`, `UnbondingDelegation`, `DelegatorUnbondingDelegations`, `Validator`, `Pool`, `Params`; distribution `DelegationRewards`, `DelegationTotalRewards`; `cosmwasm.wasm.v1.Query/ContractInfo`, a `404` for an address that is not a contract; paginated ones take `pagination.limit` up to 100), the reads of the explorer's token and contract pages (bank `TotalSupply` and `DenomOwners`; wasmd `Codes`, `CodeInfo`, `ContractsByCode`, `ContractsByCreator`, `ContractHistory` and `AllContractState`, never `SmartContractState` or `Code`), and two POST routes for a wallet with no node: `POST /v1/chain/simulate` and `POST /v1/chain/broadcast`, each `{"tx_bytes":"<base64 TxRaw>"}` of at most 1 MiB. Simulate answers `{gas_wanted, gas_used, fee, base_fee}` (the two gas figures are decimal strings, `gas_wanted` being the limit the transaction declares) or, for a transaction the chain refuses, `422 {code, codespace, log}` with a sanitised log; broadcast submits with `broadcast_tx_sync` (never commit; it answers when the mempool has checked the transaction, not when it is in a block) and answers `{code, codespace, log, tx_hash}`, 422 with the same members when `CheckTx` refused it, 503 when the mempool is full and a plain-text 502 when the chain does not answer. Each has its own in-flight cap and two rate-limit buckets (per client network and per route). `GET /v1/chain/tx?hash=` answers a transaction not yet in a block as `404` with `Retry-After`, not 502 ([chain architecture](../vol2/39-chain-architecture.md)). `POST /v1/chain/light` is the light-client route a joining node's state-sync names in `rpc_servers`: one CometBFT JSON-RPC request of at most 1 KiB, answered with the caller's id, for the methods `status`, `commit`, `validators` (`per_page` up to 100) and `consensus_params`, each parameter a positive integer. The gateway rebuilds the request from the checked method and parameters and never forwards what the caller wrote. Every other method is refused, `broadcast_evidence` and `broadcast_tx_*` included, and a node's error message is cleaned of paths and addresses. It has a cap of 16 in flight and its own two rate-limit buckets. Test networks only, and only on a gateway whose node.yaml turns `chain.faucet` on: `POST /v1/chain/faucet` `{"recipient":"orama1…","amount":"<norama>"}` (the amount is optional, a decimal string, 100 ORAMA by default; at most 1 KiB, no unknown field). The gateway signs `MsgFaucet` for the recipient with a key of its own, one transaction at a time and simulated first, and answers `200 {tx_hash, amount, height}` (amount and height as decimal strings) once the drip is in a block. A refusal is `{error, message}`: `400 bad_request`, `400 bad_recipient` (not a canonical orama address, the faucet's own account, or one the chain refuses, a module account), `400 bad_amount` (not a whole number of norama, or over `faucet_max_drip`), `429 cooldown`, `429 allowance_spent` (a client network may ask 20,000 ORAMA a day in all, and the gateway gives out 200,000 ORAMA a day to everyone together, so that no one client spends the epoch's cap), `503 epoch_cap`, `403 faucet_disabled`, `503 busy` and `503 unavailable` (the faucet account is missing or cannot pay a fee), `504 pending` (being made or sent, not yet in a block) and `502 faucet_failed`. It has its own rate-limit buckets (3 a minute, burst 3, per client network; 20 a minute, burst 6, for the route) and takes no credential. On a gateway with no faucet key, or on a chain id that is not a test network's, the path is a 404 or a `403`. Open: the chain charges the sender a fee, and the gateway bounds only the load. The handler refuses every other path. |
| `/v1/operator/invite` | CLI | Mint a node invite. `orama maint invite`. Optional body `{"expiry_seconds": N}` (or `expiry_minutes` from an older CLI); default and cap one hour. |
| `/v1/operator/node/register` | CLI | Record a node in the inventory. |
| `/v1/operator/rotate-signing-key` | CLI | Generate a new signing key for this gateway, publish it, and leave the outgoing one verifying what it already signed for one access-token lifetime. Operator route. `orama maint operator rotate-signing-key`. |
| `/v1/operator/rotate-secrets` | CLI | Rewrite stored ciphertext onto `enc:v1:<id>:` (a deployment's environment onto `enc:v2:<id>:`, sealed to its row) and enable bound writes. `--rotate` generates a new encryption root first. Operator route. `orama maint operator rotate-secrets`. |
| `/v1/operator/nodes` | CLI | Fleet inventory. |
| `/v1/operator/operators` | CLI | List the operator wallets (`GET`) or add one (`POST` `{"wallet":"0x…"}`). Operator route. `orama maint operator list`, `orama maint operator add`. |
| `/v1/operator/operators/` | CLI | `DELETE /v1/operator/operators/{wallet}` takes one wallet off the list and refuses to remove the last. `orama maint operator remove`. |
| `/v1/operator/settings` | CLI | Effective namespace-creation mode and per-wallet cap, and the auto-update settings (`auto_update`, `update_channel`, `update_window`, `release_repo`, with their defaults). Operator route. `orama maint cluster settings show`. |
| `/v1/operator/settings/` | CLI | `PUT /v1/operator/settings/namespace-creation` with `{"value":"operators"}`, `"allowlist"` or `"open"`, or `PUT /v1/operator/settings/max-namespaces-per-wallet` with `{"value":n}` from 1 to 10000, or `PUT /v1/operator/settings/auto-update` (`off`, `notify`, `auto`), `update-channel` (1 to 32 of a-z, 0-9, -), `update-window` (`start-end` hours UTC, or `""`) or `release-repo` (an https URL, or `""`), each with `{"value":"..."}`; a value the agent could not use is refused 400 and not stored. `orama maint cluster settings set`. |
| `/v1/operator/namespaces/remove` | CLI | `POST {"namespace","reason"}`: remove a namespace its owner can no longer delete (owner wallet lost, or a test run's throwaway wallet), with the same teardown as `/v1/namespace/delete`. Operator route; the lobby and reserved names are refused; recorded as `namespace.operator_remove` with the operator's wallet and the reason. `orama cluster namespace remove`. |
| `/v1/operator/creators` | CLI | List (`GET`) or add (`POST` `{"wallet":"0x…"}`) wallets allowed to create namespaces when creation is `allowlist`. `orama maint cluster creators list`, `orama maint cluster creators add`. |
| `/v1/operator/creators/` | CLI | `DELETE /v1/operator/creators/{wallet}` takes one wallet off that list. An empty list denies everyone. `orama maint cluster creators remove`. |
| `/v1/operator/health` | direct | The full health report `/v1/health` summarises: each check's latency and error, and the health of every namespace hosted on this node with its ports. Operator route. |
| `/v1/operator/telemetry` | CLI | `orama status`. The whole cluster: every node's health report and the alerts derived from them (`cluster.ClusterSnapshot`), assembled by this cluster gateway from its peers over the mesh and cached 5s. Operator route. |
| `/v1/operator/telemetry/stream` | CLI | `orama status` live view. Server-sent events: `event: snapshot` with the snapshot as one JSON line every `?interval=` seconds (2–60, default 5), `event: error` when none could be assembled, `: keepalive` comments. Ends after 100s; the client reconnects. Same authorization as `/v1/operator/telemetry`. |

### Internal (node to node)

| Route | Owner | Notes |
|-------|-------|-------|
| `/v1/internal/acme/cleanup` | internal | Caddy on this host, with a MAC under the ACME challenge key install gives it (`/etc/caddy/orama-acme.key`); only `_acme-challenge` records under the base domain. Anything else is 404 (unsigned) or 400 (a record it has no business writing). |
| `/v1/internal/acme/present` | internal | Same as cleanup. |
| `/v1/internal/deployments/replica/env` | internal | Node-to-node over the WireGuard overlay. Coordination MAC v2 signed for the receiving node's peer id (body and nonce covered) + overlay source; the former constant header is refused. Never reachable by a client. |
| `/v1/internal/deployments/replica/rollback` | internal | Node-to-node over the WireGuard overlay. Coordination MAC v2 signed for the receiving node's peer id (body and nonce covered) + overlay source; the former constant header is refused. Never reachable by a client. |
| `/v1/internal/deployments/replica/setup` | internal | Node-to-node over the WireGuard overlay. Coordination MAC v2 signed for the receiving node's peer id (body and nonce covered) + overlay source; the former constant header is refused. Never reachable by a client. |
| `/v1/internal/deployments/replica/teardown` | internal | Node-to-node over the WireGuard overlay. Coordination MAC v2 signed for the receiving node's peer id (body and nonce covered) + overlay source; the former constant header is refused. Never reachable by a client. |
| `/v1/internal/deployments/replica/update` | internal | Node-to-node over the WireGuard overlay. Coordination MAC v2 signed for the receiving node's peer id (body and nonce covered) + overlay source; the former constant header is refused. Never reachable by a client. |
| `/v1/internal/join` | internal | Node-to-node over the WireGuard overlay. Never reachable by a client. |
| `/v1/internal/namespace/repair` | internal | Node-to-node over the WireGuard overlay. Never reachable by a client. |
| `/v1/internal/secrets/reencrypt` | internal | Index fans out `orama maint operator rotate-secrets` to each namespace gateway. Coordination MAC, v2 only (the new root key is in the body) signed for the receiving node's peer id, + overlay. A root older than the gateway's is refused `409`. |
| `/v1/internal/namespace/spawn` | internal | Node-to-node over the WireGuard overlay. Never reachable by a client. |
| `/v1/internal/node/enrol-key` | internal | From the node's own process over loopback, stamped with the node's libp2p identity key. Refused from off the host. |
| `/v1/internal/node/heartbeat` | internal | From the node's own process over loopback, stamped with the key that node enrolled. Refused from off the host. |
| `/v1/internal/node/register` | internal | From the node's own process over loopback, stamped with the key that node enrolled. Refused from off the host. |
| `/v1/internal/ping` | internal | Node-to-node over the WireGuard overlay. Answers `{"status":"ok"}` and nothing else. |
| `/v1/internal/push/ntfy/` | internal | A peer gateway's push fan-out, relayed to this node's loopback ntfy (`/v1/internal/push/ntfy/<topic>`, topic one segment of `[-_A-Za-z0-9]`, at most 64). v2 coordination MAC (covers the body, audience is this node's peer id) + overlay source; anything else is refused. Only `Title`, `Priority` and `Tags` are relayed. |
| `/v1/internal/storage/evict` | internal | A peer gateway's immediate-reclaim fan-out (`unpin?immediate=true`). Coordination MAC over the request (v2 signed for the receiving node, or v1 during a rolling upgrade; the CID is in the query string, which the MAC covers; the body is ignored) + overlay source; the overlay and the old `X-Orama-Internal-Auth` marker alone are refused with 403. |
| `/v1/internal/telemetry` | internal | A peer's cluster gateway asking for this node's latest health report. Coordination MAC + overlay source; anything else is 404. |
| `/v1/internal/tls-store` | internal | The cluster's certificate store, for Caddy on this host (`caddy.storage.orama`): `POST` with an `op` of `load`, `stat`, `store`, `delete`, `list`, `lock`, `renew` or `unlock`. Coordination MAC v2 under the TLS store key install gives Caddy (`/etc/caddy/orama-tls-store.key`), body and nonce covered, from loopback only; anything else is 404. Values arrive sealed and are refused otherwise. Cluster gateway only. See [TLS and certificates](../vol1/25-tls-and-certificates.md). |
| `/v1/internal/tls/check` | internal | Node-to-node over the WireGuard overlay. Never reachable by a client. |
| `/v1/internal/webrtc/events` | internal | A namespace's SFU reporting a participant joining or leaving a room, published on `_orama/webrtc/<room>` ([WebRTC](../vol1/23-webrtc.md)). A MAC over the request keyed by the namespace's TURN secret; anything else is refused with 401. |
