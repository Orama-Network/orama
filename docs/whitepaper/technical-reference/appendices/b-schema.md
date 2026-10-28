# Database schema

> **At a glance.**
>
> - **Generated** from `core/migrations/` applied to an empty database, joined with `core/pkg/rqlite/schema_placement.go:tablePlacement` by `make whitepaper-gen`. Do not edit by hand: the gate fails when this file and the code disagree.

Placement says which database holds a table: `Cluster` tables live only in the cluster registry (the index RQLite); `Namespace` tables live in each namespace's own RQLite. Chapter 7 explains the split.

| Table | Placement | Trust | Why |
|---|---|---|---|
| `api_keys` | Cluster | Platform | a key is validated against the registry (bug-162) |
| `api_keys_expiry_cutoff` | Cluster | Platform | the highest api_keys id migration 051 backfilled; the contract release revokes keys above it |
| `apps` | Namespace | TenantData | the tenant's applications |
| `audit_events` | Cluster | Platform | a record its own subject could delete is not a record |
| `cluster_locks` | Namespace | Platform | the migration runner takes one on the database it is migrating |
| `cluster_settings` | Cluster | Platform | who may create namespaces, and how many one wallet may own; read on the index, so a copy in a tenant database is a policy the cluster never enforces |
| `deployment_domains` | Cluster | Platform | a deployment's custom domains, routed on the main gateway |
| `deployment_events` | Cluster | Platform | written beside the deployment it records |
| `deployment_health_checks` | Cluster | Platform | written beside the deployment it records |
| `deployment_history` | Cluster | Platform | a deployment's versions, which rollback restores |
| `deployment_replicas` | Cluster | Platform | which nodes run a deployment, read while serving and checking it |
| `deployments` | Cluster | Platform | deployment rows: written by `orama deploy` through the main gateway and read by host routing, recovery and every gateway's deployment service; a tenant copy was an empty table every namespace-host call read |
| `device_attestations` | Cluster | Platform | what each device's attestation proved; a forged row is an attested device |
| `device_authorizations` | Cluster | Platform | started on one gateway, approved on another |
| `dns_nameservers` | Cluster | Platform | read and written by the node process, CoreDNS (which dials the registry) and the CLI; no gateway package names it |
| `dns_nodes` | Namespace | Platform | cluster DNS; read on a namespace gateway through g.sqlDB (namespace_health.go) and by the deployment home-node and replica managers on ORMClient |
| `dns_records` | Namespace | Platform | cluster DNS; a namespace gateway writes it through g.sqlDB (namespace_health.go). The deployment service writes it in the registry on every gateway (gateway.go deploymentRegistry) |
| `encryption_roots` | Cluster | Platform | the IKM stored secrets are derived from; a tenant copy would be a KEK they can rewrite |
| `function_cron_triggers` | Namespace | Platform | served per namespace |
| `function_db_change_tracking` | Namespace | TenantData | served per namespace |
| `function_db_triggers` | Namespace | Platform | served per namespace |
| `function_env_vars` | Namespace | Platform | served per namespace |
| `function_invocations` | Namespace | Telemetry | served per namespace |
| `function_jobs` | Namespace | TenantData | served per namespace |
| `function_logs` | Namespace | Telemetry | served per namespace |
| `function_pubsub_triggers` | Namespace | Platform | served per namespace |
| `function_rate_limits` | Namespace | TenantData | served per namespace |
| `function_secrets` | Namespace | Platform | read on the invocation path, per namespace |
| `function_timers` | Namespace | TenantData | served per namespace |
| `functions` | Namespace | Platform | served per namespace |
| `global_deployment_subdomains` | Namespace | Platform | subdomain ownership; the deployment service claims and releases subdomains in the registry on every gateway (gateway.go deploymentRegistry), so a tenant copy is unused; it stays placed here until no other namespace-gateway code is found to touch it |
| `grants` | Cluster | Platform | who may do what in a namespace |
| `home_node_assignments` | Cluster | Platform | which node hosts a deployment, read while serving it |
| `invite_tokens` | Namespace | Platform | cluster join; the join, enrol and operator-invite handlers are built on ORMClient and mounted on every gateway (gateway.go, routes.go) |
| `ipfs_cid_refs` | Cluster | Platform | the cross-namespace reference count that decides whether an unpin may remove the shared cluster pin; a namespace RQLite only sees its own references |
| `ipfs_content_ownership` | Namespace | Platform | read through ORMClient by the storage handlers |
| `namespace_attestation_policy` | Cluster | Platform | which app builds a namespace admits; read by every gateway that serves it |
| `namespace_cluster_events` | Cluster | Platform | the cluster manager's log, written and read through cm.db, which exists only on the index gateway (WireCoreGateway) |
| `namespace_cluster_nodes` | Namespace | Platform | cluster topology; a namespace gateway reads it through g.sqlDB in the namespace health loop, which starts on every gateway (gateway.go) |
| `namespace_clusters` | Namespace | Platform | cluster topology; a namespace gateway reads it through g.sqlDB in the namespace health loop, which starts on every gateway (gateway.go) |
| `namespace_cors_origins` | Cluster | Platform | which sites may read a namespace's answers; read by the index gateway, which answers ns-&lt;name> hosts |
| `namespace_creators` | Cluster | Platform | the allowlist for namespace creation; a tenant copy would let the tenant add themselves |
| `namespace_ownership` | Cluster | Platform | 0.122.x's authorization, kept for the rolling window (050 is expand-only); the next release drops it |
| `namespace_pending_cleanup` | Cluster | Platform | the tenant reconciler's retry queue, run by the cluster manager on the index gateway through cm.db |
| `namespace_port_allocations` | Namespace | Platform | cluster port allocation; a namespace gateway reads it through g.sqlDB in the namespace health loop, which starts on every gateway (gateway.go) |
| `namespace_publish_seq` | Namespace | TenantData | per-namespace publish ordering |
| `namespace_push_config` | Namespace | Platform | read on the push path, per namespace |
| `namespace_push_credentials` | Namespace | Platform | read on the push path, per namespace |
| `namespace_quotas` | Namespace | Platform | read through ORMClient by the storage handlers |
| `namespace_rate_limit_config` | Namespace | Platform | read on every request to this namespace |
| `namespace_session_policy` | Cluster | Platform | what a sign-in must prove; read where sessions are issued |
| `namespace_sqlite_backups` | Namespace | Platform | beside namespace_sqlite_databases |
| `namespace_sqlite_databases` | Namespace | Platform | the tenant's own databases, listed per namespace |
| `namespace_webrtc_config` | Namespace | Platform | read on the WebRTC path, per namespace |
| `namespaces` | Namespace | Platform | the namespace's own row, which its local tables key on |
| `node_credentials` | Cluster | Platform | a node's own key; every gateway in the cluster verifies its stamps against this |
| `node_health_events` | Cluster | Platform | written by the peer health monitor, which starts only on the cluster gateway (gateway.go: !isNamespaceGateway), and read by the voter-eviction corroboration on the node |
| `nonces` | Cluster | Platform | a challenge issued on one gateway is consumed on another |
| `operators` | Cluster | Platform | who may operate the cluster |
| `port_allocations` | Cluster | Platform | a deployment's ports on a node, unique across every namespace on it |
| `principals` | Cluster | Platform | who the platform will authenticate |
| `push_devices` | Namespace | TenantData | the tenant's devices |
| `push_topics` | Namespace | TenantData | the tenant's devices, addressed by rotating topic (FEAT-265) |
| `raft_evicted_nodes` | Cluster | Platform | tombstones read and written only by the node process on its own index rqlite (eviction.go, the membership reconciler) and by the CLI; a namespace gateway never opens it |
| `refresh_tokens` | Cluster | Platform | a session must be refreshable and revocable from anywhere |
| `release_installs` | Cluster | Platform | which node installed or failed which release; a tenant copy would let a tenant mark a release bad or skip a node's turn |
| `request_logs` | Namespace | Telemetry | this gateway's own request log |
| `revoked_tokens` | Cluster | Platform | a revocation that reaches one gateway refuses nothing |
| `rqlite_backups` | Cluster | Platform | written by the node process right after it snapshots its index rqlite (backup_offbox.go); no gateway reads it |
| `schema_migrations` | Namespace | TenantData | the tenant's own tracker; core's lives in orama_schema_migrations |
| `session_devices` | Cluster | Platform | which devices hold sessions; revoked on one gateway, refused on every other |
| `signing_keys` | Cluster | Platform | publishing a key is minting authority; the cluster verifies against it |
| `status_uptime_hourly` | Cluster | Platform | public uptime history; written and read only by pkg/telemetry/hub, which runs on the cluster gateway |
| `subscriptions` | Namespace | TenantData | dead since 002_core; stripped separately by name collision |
| `tls_locks` | Cluster | Platform | which node is obtaining or renewing a certificate (pkg/tlsstore) |
| `tls_store` | Cluster | Platform | the cluster's certificates and ACME account, sealed; read and written by the index gateway for Caddy (pkg/tlsstore) |
| `wallet_api_keys` | Cluster | Platform | which key belongs to which wallet, beside api_keys |
| `webrtc_admissions` | Namespace | Platform | who the namespace's functions admitted to its rooms, checked on the join path of its gateways; written through webrtc_admit, which bounds the room, the user and the ttl |
| `webrtc_port_allocations` | Cluster | Platform | which node runs which SFU or TURN role; read through the registry handle (sfu_directory.go uses globalSQLDB) and written by the cluster manager on the index gateway |
| `webrtc_rooms` | Cluster | Platform | only the cluster manager and namespace delete touch it (both on the index gateway, both to delete a namespace's rows); no namespace gateway code reads or writes a room row |
| `webrtc_settings` | Namespace | Platform | the namespace's own WebRTC policy (require_admission), read on the join path of its gateways; written through /v1/webrtc/config, which validates it |
| `wireguard_peers` | Namespace | Platform | mesh membership; the wireguard, node-API, join and enrol handlers are built on ORMClient and mounted on every gateway (gateway.go, routes.go) |

## api_keys

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `key` | TEXT | yes | - |  |
| `name` | TEXT | no | - |  |
| `namespace_id` | INTEGER | yes | - |  |
| `scopes` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `last_used_at` | TIMESTAMP | no | - |  |
| `revoked_at` | TIMESTAMP | no | - |  |
| `expires_at` | TIMESTAMP | no | - |  |
| `rotated_from` | INTEGER | no | - |  |
| `principal_id` | INTEGER | no | - |  |
| `resources` | TEXT | no | - |  |

## api_keys_expiry_cutoff

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `max_key_id` | INTEGER | yes | - |  |
| `recorded_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## apps

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `app_id` | TEXT | yes | - |  |
| `name` | TEXT | no | - |  |
| `public_key` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## audit_events

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace` | TEXT | no | - |  |
| `actor` | TEXT | no | - |  |
| `action` | TEXT | yes | - |  |
| `resource` | TEXT | no | - |  |
| `result` | TEXT | yes | `'success'` |  |
| `ip` | TEXT | no | - |  |
| `user_agent` | TEXT | no | - |  |
| `metadata` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## cluster_locks

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `name` | TEXT | no | - | primary |
| `holder` | TEXT | yes | `''` |  |
| `acquired_at` | TIMESTAMP | no | - |  |
| `expires_at` | TIMESTAMP | no | - |  |

## cluster_settings

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `key` | TEXT | no | - | primary |
| `value` | TEXT | yes | - |  |
| `updated_by` | TEXT | yes | - |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## deployment_domains

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `deployment_id` | TEXT | yes | - |  |
| `namespace` | TEXT | yes | - |  |
| `domain` | TEXT | yes | - |  |
| `routing_type` | TEXT | yes | `'balanced'` |  |
| `node_id` | TEXT | no | - |  |
| `is_custom` | BOOLEAN | no | `FALSE` |  |
| `tls_cert_cid` | TEXT | no | - |  |
| `verified_at` | TIMESTAMP | no | - |  |
| `verification_token` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## deployment_events

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `deployment_id` | TEXT | yes | - |  |
| `event_type` | TEXT | yes | - |  |
| `message` | TEXT | no | - |  |
| `metadata` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | no | - |  |

## deployment_health_checks

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `deployment_id` | TEXT | yes | - |  |
| `node_id` | TEXT | yes | - |  |
| `status` | TEXT | yes | - |  |
| `response_time_ms` | INTEGER | no | - |  |
| `status_code` | INTEGER | no | - |  |
| `error_message` | TEXT | no | - |  |
| `checked_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## deployment_history

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `deployment_id` | TEXT | yes | - |  |
| `version` | INTEGER | yes | - |  |
| `content_cid` | TEXT | no | - |  |
| `build_cid` | TEXT | no | - |  |
| `deployed_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `deployed_by` | TEXT | yes | - |  |
| `status` | TEXT | yes | `'success'` |  |
| `error_message` | TEXT | no | - |  |
| `rollback_from_version` | INTEGER | no | - |  |

## deployment_replicas

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `deployment_id` | TEXT | yes | - | primary |
| `node_id` | TEXT | yes | - | primary |
| `port` | INTEGER | no | `0` |  |
| `status` | TEXT | yes | `'pending'` |  |
| `is_primary` | BOOLEAN | yes | `FALSE` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## deployments

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace` | TEXT | yes | - |  |
| `name` | TEXT | yes | - |  |
| `type` | TEXT | yes | - |  |
| `version` | INTEGER | yes | `1` |  |
| `status` | TEXT | yes | `'deploying'` |  |
| `content_cid` | TEXT | no | - |  |
| `build_cid` | TEXT | no | - |  |
| `home_node_id` | TEXT | no | - |  |
| `port` | INTEGER | no | - |  |
| `subdomain` | TEXT | no | - |  |
| `environment` | TEXT | no | - |  |
| `memory_limit_mb` | INTEGER | no | `256` |  |
| `cpu_limit_percent` | INTEGER | no | `50` |  |
| `disk_limit_mb` | INTEGER | no | `1024` |  |
| `health_check_path` | TEXT | no | `'/health'` |  |
| `health_check_interval` | INTEGER | no | `30` |  |
| `restart_policy` | TEXT | no | `'always'` |  |
| `max_restart_count` | INTEGER | no | `10` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `deployed_by` | TEXT | yes | - |  |

## device_attestations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace_id` | INTEGER | yes | - | primary |
| `device_id` | TEXT | yes | - | primary |
| `platform` | TEXT | yes | - |  |
| `app_id` | TEXT | yes | - |  |
| `app_version` | TEXT | yes | `''` |  |
| `development` | INTEGER | yes | `0` |  |
| `verified_boot_key` | TEXT | yes | `''` |  |
| `grapheneos` | INTEGER | yes | `0` |  |
| `ios_key_id` | TEXT | yes | `''` |  |
| `ios_public_key` | TEXT | yes | `''` |  |
| `ios_counter` | INTEGER | yes | `0` |  |
| `attested_at` | TIMESTAMP | yes | - |  |
| `signing_cert_digests` | TEXT | yes | `'[]'` |  |
| `os_patch_level` | INTEGER | yes | `0` |  |

## device_authorizations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `device_code` | TEXT | yes | - |  |
| `user_code` | TEXT | yes | - |  |
| `namespace` | TEXT | no | - |  |
| `subject` | TEXT | no | - |  |
| `approved_at` | TIMESTAMP | no | - |  |
| `denied_at` | TIMESTAMP | no | - |  |
| `claimed_at` | TIMESTAMP | no | - |  |
| `last_polled_at` | TIMESTAMP | no | - |  |
| `expires_at` | TIMESTAMP | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `device_key` | TEXT | no | - |  |
| `device_label` | TEXT | no | - |  |
| `approved_by_device` | TEXT | no | - |  |

## dns_nameservers

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `hostname` | TEXT | no | - | primary |
| `node_id` | TEXT | yes | - |  |
| `ip_address` | TEXT | yes | - |  |
| `domain` | TEXT | yes | - |  |
| `assigned_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## dns_nodes

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `ip_address` | TEXT | yes | - |  |
| `internal_ip` | TEXT | no | - |  |
| `region` | TEXT | no | - |  |
| `status` | TEXT | yes | `'active'` |  |
| `last_seen` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `capabilities` | TEXT | no | - |  |
| `metadata` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `operator_wallet` | TEXT | no | - |  |
| `environment` | TEXT | no | `'production'` |  |
| `ssh_user` | TEXT | no | `'root'` |  |
| `role` | TEXT | no | `'node'` |  |
| `stamp_level` | INTEGER | yes | `0` |  |
| `stamp_level_at` | TEXT | yes | `''` |  |

## dns_records

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `fqdn` | TEXT | yes | - |  |
| `record_type` | TEXT | yes | `'A'` |  |
| `value` | TEXT | yes | - |  |
| `ttl` | INTEGER | yes | `300` |  |
| `priority` | INTEGER | no | `0` |  |
| `namespace` | TEXT | yes | `'system'` |  |
| `deployment_id` | TEXT | no | - |  |
| `node_id` | TEXT | no | - |  |
| `is_active` | BOOLEAN | yes | `TRUE` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | yes | `'system'` |  |

## encryption_roots

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `slot` | TEXT | no | - | primary |
| `key_id` | TEXT | yes | - |  |
| `ikm` | TEXT | yes | - |  |
| `write_versioned` | INTEGER | yes | `0` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_cron_triggers

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `cron_expression` | TEXT | yes | - |  |
| `next_run_at` | TIMESTAMP | no | - |  |
| `last_run_at` | TIMESTAMP | no | - |  |
| `last_status` | TEXT | no | - |  |
| `last_error` | TEXT | no | - |  |
| `enabled` | BOOLEAN | yes | `TRUE` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_db_change_tracking

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `trigger_id` | TEXT | yes | - |  |
| `last_row_id` | INTEGER | no | - |  |
| `last_updated_at` | TIMESTAMP | no | - |  |
| `last_check_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_db_triggers

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `table_name` | TEXT | yes | - |  |
| `operation` | TEXT | yes | - |  |
| `condition` | TEXT | no | - |  |
| `enabled` | BOOLEAN | yes | `TRUE` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_env_vars

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `key` | TEXT | yes | - |  |
| `value` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_invocations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `request_id` | TEXT | yes | - |  |
| `trigger_type` | TEXT | yes | - |  |
| `caller_wallet` | TEXT | no | - |  |
| `input_size` | INTEGER | no | - |  |
| `output_size` | INTEGER | no | - |  |
| `started_at` | TIMESTAMP | yes | - |  |
| `completed_at` | TIMESTAMP | no | - |  |
| `duration_ms` | INTEGER | no | - |  |
| `status` | TEXT | no | - |  |
| `error_message` | TEXT | no | - |  |
| `memory_used_mb` | REAL | no | - |  |
| `node_id` | TEXT | no | - |  |
| `queue_ms` | INTEGER | no | - |  |
| `load_ms` | INTEGER | no | - |  |
| `db_calls` | INTEGER | no | - |  |
| `db_ms` | INTEGER | no | - |  |

## function_jobs

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `payload` | TEXT | no | - |  |
| `status` | TEXT | yes | `'pending'` |  |
| `progress` | INTEGER | yes | `0` |  |
| `result` | TEXT | no | - |  |
| `error` | TEXT | no | - |  |
| `started_at` | TIMESTAMP | no | - |  |
| `completed_at` | TIMESTAMP | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_logs

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `invocation_id` | TEXT | yes | - |  |
| `level` | TEXT | yes | - |  |
| `message` | TEXT | yes | - |  |
| `timestamp` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_pubsub_triggers

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `topic` | TEXT | yes | - |  |
| `enabled` | BOOLEAN | yes | `TRUE` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `topic_pattern` | TEXT | yes | `''` |  |
| `aggregation_window_ms` | INTEGER | yes | `0` |  |
| `aggregation_max_batch_size` | INTEGER | yes | `100` |  |

## function_rate_limits

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `window_key` | TEXT | yes | - |  |
| `count` | INTEGER | yes | `0` |  |
| `window_start` | TIMESTAMP | yes | - |  |

## function_secrets

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace` | TEXT | yes | - |  |
| `name` | TEXT | yes | - |  |
| `encrypted_value` | BLOB | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## function_timers

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `function_id` | TEXT | yes | - |  |
| `run_at` | TIMESTAMP | yes | - |  |
| `payload` | TEXT | no | - |  |
| `status` | TEXT | yes | `'pending'` |  |
| `error` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `completed_at` | TIMESTAMP | no | - |  |

## functions

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `name` | TEXT | yes | - |  |
| `namespace` | TEXT | yes | - |  |
| `version` | INTEGER | yes | `1` |  |
| `wasm_cid` | TEXT | yes | - |  |
| `source_cid` | TEXT | no | - |  |
| `memory_limit_mb` | INTEGER | yes | `64` |  |
| `timeout_seconds` | INTEGER | yes | `30` |  |
| `is_public` | BOOLEAN | yes | `FALSE` |  |
| `retry_count` | INTEGER | yes | `0` |  |
| `retry_delay_seconds` | INTEGER | yes | `5` |  |
| `dlq_topic` | TEXT | no | - |  |
| `status` | TEXT | yes | `'active'` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | yes | - |  |
| `ws_persistent` | BOOLEAN | no | `FALSE` |  |
| `ws_idle_timeout_sec` | INTEGER | no | `0` |  |
| `ws_max_frame_bytes` | INTEGER | no | `0` |  |
| `ws_max_inflight_per_conn` | INTEGER | no | `0` |  |
| `raw_http_response` | BOOLEAN | no | `FALSE` |  |
| `is_internal` | BOOLEAN | yes | `FALSE` |  |
| `ws_auth` | TEXT | yes | `''` |  |
| `rate_limit_caller_requests` | INTEGER | yes | `0` |  |
| `rate_limit_caller_window_sec` | INTEGER | yes | `0` |  |
| `rate_limit_ip_requests` | INTEGER | yes | `0` |  |
| `rate_limit_ip_window_sec` | INTEGER | yes | `0` |  |
| `wasm_sha256` | TEXT | no | - |  |
| `invocable_by` | TEXT | yes | `''` |  |
| `open_to_pending_devices` | BOOLEAN | yes | `FALSE` |  |

## global_deployment_subdomains

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `subdomain` | TEXT | no | - | primary |
| `namespace` | TEXT | yes | - |  |
| `deployment_id` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## grants

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `principal_id` | INTEGER | yes | - |  |
| `namespace_id` | INTEGER | yes | - |  |
| `role` | TEXT | yes | - |  |
| `resource` | TEXT | no | - |  |
| `expires_at` | TIMESTAMP | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | no | - |  |
| `revoked_at` | TIMESTAMP | no | - |  |

## home_node_assignments

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | no | - | primary |
| `home_node_id` | TEXT | yes | - |  |
| `assigned_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `last_heartbeat` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `deployment_count` | INTEGER | no | `0` |  |
| `total_memory_mb` | INTEGER | no | `0` |  |
| `total_cpu_percent` | INTEGER | no | `0` |  |

## invite_tokens

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `token` | TEXT | no | - | primary |
| `created_by` | TEXT | yes | - |  |
| `created_at` | DATETIME | no | `CURRENT_TIMESTAMP` |  |
| `expires_at` | DATETIME | yes | - |  |
| `used_at` | DATETIME | no | - |  |
| `used_by_ip` | TEXT | no | - |  |
| `operator_wallet` | TEXT | no | - |  |

## ipfs_cid_refs

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `cid` | TEXT | yes | - | primary |
| `namespace` | TEXT | yes | - | primary |
| `kind` | TEXT | yes | - | primary |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `holders` | INTEGER | yes | `1` |  |

## ipfs_content_ownership

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `cid` | TEXT | yes | - |  |
| `namespace` | TEXT | yes | - |  |
| `name` | TEXT | no | - |  |
| `size_bytes` | BIGINT | no | `0` |  |
| `is_pinned` | BOOLEAN | no | `FALSE` |  |
| `uploaded_at` | TIMESTAMP | yes | - |  |
| `uploaded_by` | TEXT | yes | - |  |
| `pin_requested_at` | TIMESTAMP | no | - |  |

## namespace_attestation_policy

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace_id` | INTEGER | no | - | primary |
| `mode` | TEXT | yes | - |  |
| `ios_team_id` | TEXT | yes | `''` |  |
| `ios_bundle_id` | TEXT | yes | `''` |  |
| `ios_allow_development` | INTEGER | yes | `0` |  |
| `ios_min_bundle_version` | TEXT | yes | `''` |  |
| `android_package` | TEXT | yes | `''` |  |
| `android_signing_digests` | TEXT | yes | `'[]'` |  |
| `android_allow_graphene` | INTEGER | yes | `0` |  |
| `android_min_version_code` | INTEGER | yes | `0` |  |
| `android_max_patch_age_months` | INTEGER | yes | `0` |  |
| `reattest_interval_sec` | INTEGER | yes | `604800` |  |
| `updated_by` | TEXT | yes | - |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_cluster_events

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace_cluster_id` | TEXT | yes | - |  |
| `event_type` | TEXT | yes | - |  |
| `node_id` | TEXT | no | - |  |
| `message` | TEXT | no | - |  |
| `metadata` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_cluster_nodes

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace_cluster_id` | TEXT | yes | - |  |
| `node_id` | TEXT | yes | - |  |
| `role` | TEXT | yes | - |  |
| `rqlite_http_port` | INTEGER | no | - |  |
| `rqlite_raft_port` | INTEGER | no | - |  |
| `olric_http_port` | INTEGER | no | - |  |
| `olric_memberlist_port` | INTEGER | no | - |  |
| `gateway_http_port` | INTEGER | no | - |  |
| `status` | TEXT | yes | `'pending'` |  |
| `process_pid` | INTEGER | no | - |  |
| `last_heartbeat` | TIMESTAMP | no | - |  |
| `error_message` | TEXT | no | - |  |
| `rqlite_join_address` | TEXT | no | - |  |
| `olric_peers` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_clusters

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `namespace_name` | TEXT | yes | - |  |
| `status` | TEXT | yes | `'provisioning'` |  |
| `rqlite_node_count` | INTEGER | yes | `3` |  |
| `olric_node_count` | INTEGER | yes | `3` |  |
| `gateway_node_count` | INTEGER | yes | `3` |  |
| `provisioned_by` | TEXT | yes | - |  |
| `provisioned_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `ready_at` | TIMESTAMP | no | - |  |
| `last_health_check` | TIMESTAMP | no | - |  |
| `error_message` | TEXT | no | - |  |
| `retry_count` | INTEGER | no | `0` |  |
| `deprovisioning_at` | TIMESTAMP | no | - |  |

## namespace_cors_origins

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace_id` | INTEGER | no | - | primary |
| `origins` | TEXT | yes | - |  |
| `updated_by` | TEXT | yes | - |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_creators

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `wallet` | TEXT | no | - | primary |
| `added_by` | TEXT | yes | - |  |
| `added_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_ownership

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `owner_type` | TEXT | yes | - |  |
| `owner_id` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_pending_cleanup

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace` | TEXT | yes | - |  |
| `node_id` | TEXT | yes | - |  |
| `node_ip` | TEXT | yes | - |  |
| `action` | TEXT | yes | - |  |
| `attempts` | INTEGER | yes | `0` |  |
| `last_error` | TEXT | yes | `''` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `last_attempt_at` | TIMESTAMP | no | - |  |
| `cluster_id` | TEXT | yes | `''` |  |
| `purge_data` | INTEGER | yes | `0` |  |
| `claimed_until` | TIMESTAMP | no | - |  |
| `claimed_by` | TEXT | no | - |  |

## namespace_port_allocations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `node_id` | TEXT | yes | - |  |
| `namespace_cluster_id` | TEXT | yes | - |  |
| `port_start` | INTEGER | yes | - |  |
| `port_end` | INTEGER | yes | - |  |
| `rqlite_http_port` | INTEGER | yes | - |  |
| `rqlite_raft_port` | INTEGER | yes | - |  |
| `olric_http_port` | INTEGER | yes | - |  |
| `olric_memberlist_port` | INTEGER | yes | - |  |
| `gateway_http_port` | INTEGER | yes | - |  |
| `allocated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_publish_seq

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | no | - | primary |
| `next_seq` | BIGINT | yes | `1` |  |
| `updated_at` | INTEGER | yes | - |  |

## namespace_push_config

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | no | - | primary |
| `ntfy_base_url` | TEXT | no | - |  |
| `ntfy_auth_token_encrypted` | TEXT | no | - |  |
| `expo_access_token_encrypted` | TEXT | no | - |  |
| `updated_at` | INTEGER | yes | - |  |
| `updated_by` | TEXT | no | - |  |

## namespace_push_credentials

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | yes | - | primary |
| `provider` | TEXT | yes | - | primary |
| `credentials_json` | TEXT | yes | - |  |
| `updated_at` | INTEGER | yes | - |  |
| `updated_by` | TEXT | no | - |  |

## namespace_quotas

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | no | - | primary |
| `max_sqlite_databases` | INTEGER | no | `10` |  |
| `max_storage_bytes` | BIGINT | no | `5368709120` |  |
| `max_ipfs_pins` | INTEGER | no | `1000` |  |
| `max_deployments` | INTEGER | no | `20` |  |
| `max_cpu_percent` | INTEGER | no | `200` |  |
| `max_memory_mb` | INTEGER | no | `2048` |  |
| `max_rqlite_queries_per_minute` | INTEGER | no | `1000` |  |
| `max_olric_ops_per_minute` | INTEGER | no | `10000` |  |
| `current_storage_bytes` | BIGINT | no | `0` |  |
| `current_deployments` | INTEGER | no | `0` |  |
| `current_sqlite_databases` | INTEGER | no | `0` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## namespace_rate_limit_config

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | no | - | primary |
| `requests_per_minute` | INTEGER | yes | - |  |
| `burst` | INTEGER | yes | - |  |
| `updated_at` | INTEGER | yes | - |  |
| `updated_by` | TEXT | no | - |  |

## namespace_session_policy

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace_id` | INTEGER | no | - | primary |
| `device_policy` | TEXT | yes | - |  |
| `updated_by` | TEXT | yes | - |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `sign_in` | TEXT | yes | `'members'` |  |
| `end_user_topics` | TEXT | yes | `'[]'` |  |
| `per_ip_limits` | TEXT | yes | `'off'` |  |
| `new_wallets_per_ip_hour` | INTEGER | yes | `20` |  |
| `invokes_per_ip_minute` | INTEGER | yes | `1200` |  |

## namespace_sqlite_backups

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `database_id` | TEXT | yes | - |  |
| `backup_cid` | TEXT | yes | - |  |
| `size_bytes` | BIGINT | yes | - |  |
| `backup_type` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | yes | - |  |

## namespace_sqlite_databases

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace` | TEXT | yes | - |  |
| `database_name` | TEXT | yes | - |  |
| `home_node_id` | TEXT | yes | - |  |
| `file_path` | TEXT | yes | - |  |
| `size_bytes` | BIGINT | no | `0` |  |
| `backup_cid` | TEXT | no | - |  |
| `last_backup_at` | TIMESTAMP | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `updated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | yes | - |  |

## namespace_webrtc_config

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace_cluster_id` | TEXT | yes | - |  |
| `namespace_name` | TEXT | yes | - |  |
| `enabled` | INTEGER | yes | `1` |  |
| `turn_shared_secret` | TEXT | yes | - |  |
| `turn_credential_ttl` | INTEGER | yes | `600` |  |
| `sfu_node_count` | INTEGER | yes | `3` |  |
| `turn_node_count` | INTEGER | yes | `2` |  |
| `enabled_by` | TEXT | yes | - |  |
| `enabled_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `disabled_at` | TIMESTAMP | no | - |  |
| `stealth_enabled` | BOOLEAN | no | `FALSE` |  |

## namespaces

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `name` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## node_credentials

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `node_id` | TEXT | no | - | primary |
| `public_key` | TEXT | yes | - |  |
| `enrolled_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `revoked_at` | TIMESTAMP | no | - |  |

## node_health_events

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `observer_id` | TEXT | yes | - |  |
| `target_id` | TEXT | yes | - |  |
| `status` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## nonces

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `wallet` | TEXT | yes | - |  |
| `nonce` | TEXT | yes | - |  |
| `purpose` | TEXT | no | - |  |
| `expires_at` | TIMESTAMP | no | - |  |
| `used_at` | TIMESTAMP | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## operators

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `wallet` | TEXT | no | - | primary |
| `added_by` | TEXT | yes | - |  |
| `added_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## port_allocations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `node_id` | TEXT | yes | - | primary |
| `port` | INTEGER | yes | - | primary |
| `deployment_id` | TEXT | yes | - |  |
| `allocated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## principals

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `type` | TEXT | yes | - |  |
| `identifier` | TEXT | yes | - |  |
| `display_name` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `created_by` | TEXT | no | - |  |
| `disabled_at` | TIMESTAMP | no | - |  |

## push_devices

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace` | TEXT | yes | - |  |
| `user_id` | TEXT | yes | - |  |
| `device_id` | TEXT | yes | - |  |
| `provider` | TEXT | yes | - |  |
| `token_encrypted` | TEXT | yes | - |  |
| `platform` | TEXT | no | - |  |
| `app_version` | TEXT | no | - |  |
| `created_at` | INTEGER | yes | - |  |
| `updated_at` | INTEGER | yes | - |  |
| `last_seen` | INTEGER | no | - |  |
| `token_fp` | TEXT | no | - |  |
| `session_device_id` | TEXT | no | - |  |

## push_topics

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | yes | - | primary |
| `topic_id` | TEXT | yes | - | primary |
| `provider` | TEXT | yes | - |  |
| `token_encrypted` | TEXT | yes | - |  |
| `token_fp` | TEXT | yes | - |  |
| `expires_at` | INTEGER | yes | - |  |

## raft_evicted_nodes

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `node_id` | TEXT | no | - | primary |
| `raft_addr` | TEXT | yes | `''` |  |
| `peer_id` | TEXT | yes | `''` |  |
| `reason` | TEXT | yes | - |  |
| `evicted_by` | TEXT | yes | `''` |  |
| `evicted_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## refresh_tokens

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `subject` | TEXT | yes | - |  |
| `token` | TEXT | yes | - |  |
| `audience` | TEXT | no | - |  |
| `expires_at` | TIMESTAMP | no | - |  |
| `revoked_at` | TIMESTAMP | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `custom_claims` | TEXT | no | - |  |
| `grace_used_at` | TIMESTAMP | no | - |  |
| `device_id` | TEXT | no | - |  |
| `session_id` | TEXT | no | - |  |

## release_installs

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `version` | TEXT | yes | - | primary |
| `node_id` | TEXT | yes | - | primary |
| `state` | TEXT | yes | - |  |
| `detail` | TEXT | yes | `''` |  |
| `recorded_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## request_logs

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `method` | TEXT | yes | - |  |
| `path` | TEXT | yes | - |  |
| `status_code` | INTEGER | yes | - |  |
| `bytes_out` | INTEGER | yes | `0` |  |
| `duration_ms` | INTEGER | yes | `0` |  |
| `ip` | TEXT | no | - |  |
| `api_key_id` | INTEGER | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## revoked_tokens

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `jti` | TEXT | no | - |  |
| `subject` | TEXT | no | - |  |
| `issued_before` | INTEGER | yes | `0` |  |
| `expires_at` | INTEGER | yes | - |  |
| `reason` | TEXT | no | - |  |
| `revoked_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `namespace` | TEXT | no | - |  |

## rqlite_backups

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `taken_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `taken_by` | TEXT | yes | `''` |  |
| `cid` | TEXT | yes | - |  |
| `sha256` | TEXT | yes | - |  |
| `size_bytes` | INTEGER | yes | - |  |
| `encrypted` | INTEGER | yes | `1` |  |

## schema_migrations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `version` | INTEGER | no | - | primary |
| `applied_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## session_devices

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `subject` | TEXT | yes | - |  |
| `public_key` | TEXT | yes | - |  |
| `label` | TEXT | yes | `''` |  |
| `state` | TEXT | yes | - |  |
| `approved_by_device` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `activated_at` | TIMESTAMP | no | - |  |
| `revoked_at` | TIMESTAMP | no | - |  |
| `revoked_by` | TEXT | no | - |  |
| `revocation_proof` | TEXT | no | - |  |
| `revocation_evidence` | TEXT | no | - |  |

## signing_keys

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `kid` | TEXT | no | - | primary |
| `namespace` | TEXT | no | - |  |
| `algorithm` | TEXT | yes | `'EdDSA'` |  |
| `public_key` | TEXT | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `retired_at` | TIMESTAMP | no | - |  |
| `last_seen_at` | TIMESTAMP | no | - |  |

## status_uptime_hourly

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `component` | TEXT | yes | - | primary |
| `hour` | TEXT | yes | - | primary |
| `operational_minutes` | INTEGER | yes | `0` |  |
| `degraded_minutes` | INTEGER | yes | `0` |  |
| `outage_minutes` | INTEGER | yes | `0` |  |

## subscriptions

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `app_id` | INTEGER | no | - |  |
| `topic` | TEXT | yes | - |  |
| `endpoint` | TEXT | no | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## tls_locks

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `name` | TEXT | no | - | primary |
| `holder` | TEXT | yes | - |  |
| `expires_unix_ms` | INTEGER | yes | - |  |

## tls_store

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `key` | TEXT | no | - | primary |
| `value` | TEXT | yes | - |  |
| `size` | INTEGER | yes | - |  |
| `modified_unix_ms` | INTEGER | yes | - |  |

## wallet_api_keys

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | INTEGER | no | - | primary |
| `namespace_id` | INTEGER | yes | - |  |
| `wallet` | TEXT | yes | - |  |
| `api_key_id` | INTEGER | yes | - |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## webrtc_admissions

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | yes | - | primary |
| `room` | TEXT | yes | - | primary |
| `user_id` | TEXT | yes | - | primary |
| `device_id` | TEXT | yes | `''` | primary |
| `expires_at` | INTEGER | yes | - |  |
| `revoked_at` | INTEGER | no | - |  |
| `muted` | INTEGER | yes | `0` |  |
| `created_at` | INTEGER | yes | - |  |
| `generation` | INTEGER | yes | `0` |  |
| `video_muted` | INTEGER | yes | `0` |  |

## webrtc_port_allocations

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `node_id` | TEXT | yes | - |  |
| `namespace_cluster_id` | TEXT | yes | - |  |
| `service_type` | TEXT | yes | - |  |
| `sfu_signaling_port` | INTEGER | no | - |  |
| `sfu_media_port_start` | INTEGER | no | - |  |
| `sfu_media_port_end` | INTEGER | no | - |  |
| `turn_listen_port` | INTEGER | no | - |  |
| `turn_tls_port` | INTEGER | no | - |  |
| `turn_relay_port_start` | INTEGER | no | - |  |
| `turn_relay_port_end` | INTEGER | no | - |  |
| `allocated_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## webrtc_rooms

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `id` | TEXT | no | - | primary |
| `namespace_cluster_id` | TEXT | yes | - |  |
| `namespace_name` | TEXT | yes | - |  |
| `room_id` | TEXT | yes | - |  |
| `sfu_node_id` | TEXT | yes | - |  |
| `sfu_internal_ip` | TEXT | yes | - |  |
| `sfu_signaling_port` | INTEGER | yes | - |  |
| `participant_count` | INTEGER | yes | `0` |  |
| `max_participants` | INTEGER | yes | `100` |  |
| `created_at` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |
| `last_activity` | TIMESTAMP | yes | `CURRENT_TIMESTAMP` |  |

## webrtc_settings

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `namespace` | TEXT | yes | - | primary |
| `require_admission` | INTEGER | yes | `0` |  |
| `updated_at` | INTEGER | yes | - |  |

## wireguard_peers

| Column | Type | Not null | Default | Key |
|---|---|---|---|---|
| `node_id` | TEXT | no | - | primary |
| `wg_ip` | TEXT | yes | - |  |
| `public_key` | TEXT | yes | - |  |
| `public_ip` | TEXT | yes | - |  |
| `wg_port` | INTEGER | no | `51820` |  |
| `created_at` | DATETIME | no | `CURRENT_TIMESTAMP` |  |
| `ipfs_peer_id` | TEXT | no | `''` |  |
| `operator_wallet` | TEXT | no | - |  |
| `confirmed_at` | TIMESTAMP | no | - |  |
| `agent_token` | TEXT | no | - |  |
