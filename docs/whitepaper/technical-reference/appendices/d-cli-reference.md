# CLI reference

> **At a glance.**
>
> - **Generated** from the `orama` binary's cobra command tree by `make whitepaper-gen`. Do not edit by hand: a test in `core/cmd/orama` fails when this file and the code disagree.

Every command the `orama` binary defines, with its flags. [The CLI](../vol1/35-the-cli.md) explains how the binary is built and how commands reach the cluster.

## Commands

- [`orama app`](#orama-app) - Manage deployed applications
  - [`orama app delete`](#orama-app-delete) - Delete a deployment
  - [`orama app env`](#orama-app-env) - Manage an app's environment variables
    - [`orama app env list`](#orama-app-env-list) - List an app's environment variable names
    - [`orama app env set`](#orama-app-env-set) - Set environment variables and restart the app
    - [`orama app env unset`](#orama-app-env-unset) - Remove environment variables and restart the app
  - [`orama app get`](#orama-app-get) - Get deployment details
  - [`orama app grants`](#orama-app-grants) - Say what a deployed app may do, as itself
    - [`orama app grants list`](#orama-app-grants-list) - Show what deployments in this namespace may do
    - [`orama app grants set`](#orama-app-grants-set) - Grant a deployment a role
  - [`orama app list`](#orama-app-list) - List all deployments
  - [`orama app logs`](#orama-app-logs) - Stream deployment logs
  - [`orama app rollback`](#orama-app-rollback) - Rollback a deployment to a previous version
  - [`orama app stats`](#orama-app-stats) - Show resource usage for a deployment
- [`orama audit`](#orama-audit) - Read this namespace's audit trail, or the cluster's
- [`orama auth`](#orama-auth) - Authentication management
  - [`orama auth approve`](#orama-auth-approve) - Approve a login waiting on another machine
  - [`orama auth list`](#orama-auth-list) - List all stored credentials
  - [`orama auth login`](#orama-auth-login) - Sign in, here or from another machine
  - [`orama auth logout`](#orama-auth-logout) - End this session on the gateway and clear it here
  - [`orama auth sessions`](#orama-auth-sessions) - Which machines are signed in as this wallet
    - [`orama auth sessions revoke`](#orama-auth-sessions-revoke) - End one session, or every one
  - [`orama auth status`](#orama-auth-status) - Show what is stored on this machine, without asking the gateway
  - [`orama auth switch`](#orama-auth-switch) - Switch between stored credentials
  - [`orama auth whoami`](#orama-auth-whoami) - Ask the gateway who this credential is and what it may do
- [`orama chain`](#orama-chain) - Read the Orama chain, send ORAMA, withdraw earnings; fund test accounts
  - [`orama chain balance`](#orama-chain-balance) - Show an account's bank balances
  - [`orama chain deal`](#orama-chain-deal) - Show a storage deal (x/storage)
  - [`orama chain earnings`](#orama-chain-earnings) - Show an account's earnings balance (x/fees)
  - [`orama chain faucet`](#orama-chain-faucet) - Fund an account on a test network (testnet, devnet)
  - [`orama chain node`](#orama-chain-node) - Show a registered node (x/nodes)
  - [`orama chain query`](#orama-chain-query) - Run any Orama module query through the gateway or --rpc
  - [`orama chain send`](#orama-chain-send) - Send ORAMA, privately by default; --public sends openly
  - [`orama chain status`](#orama-chain-status) - Show the chain's height, network and sync state
  - [`orama chain validator`](#orama-chain-validator) - List the validator set, or show one validator
  - [`orama chain withdraw-earnings`](#orama-chain-withdraw-earnings) - Move earnings to your own balance, where they can be sent
- [`orama cluster`](#orama-cluster) - Register this cluster on the chain, and remove a tenant namespace
  - [`orama cluster namespace`](#orama-cluster-namespace) - Operator actions on a namespace
    - [`orama cluster namespace remove`](#orama-cluster-namespace-remove) - Remove a namespace whose owner can no longer delete it
  - [`orama cluster register-onchain`](#orama-cluster-register-onchain) - Register this cluster's public name on the Orama chain
  - [`orama cluster retire-onchain`](#orama-cluster-retire-onchain) - Retire this cluster's public row on the Orama chain
- [`orama db`](#orama-db) - Manage SQLite databases
  - [`orama db backup`](#orama-db-backup) - Backup database to IPFS
  - [`orama db backups`](#orama-db-backups) - List backups for a database
  - [`orama db create`](#orama-db-create) - Create a new SQLite database
  - [`orama db delete`](#orama-db-delete) - Delete a database and its file
  - [`orama db list`](#orama-db-list) - List all databases
  - [`orama db query`](#orama-db-query) - Execute a SQL query
- [`orama deploy`](#orama-deploy) - Deploy applications to the Orama network
  - [`orama deploy go`](#orama-deploy-go) - Deploy a Go backend
  - [`orama deploy nextjs`](#orama-deploy-nextjs) - Deploy a Next.js application
  - [`orama deploy nodejs`](#orama-deploy-nodejs) - Deploy a Node.js backend
  - [`orama deploy static`](#orama-deploy-static) - Deploy a static site (React, Vue, etc.)
- [`orama domain`](#orama-domain) - Attach custom domains to your apps
  - [`orama domain add`](#orama-domain-add) - Attach a domain to an app
  - [`orama domain list`](#orama-domain-list) - List your custom domains
  - [`orama domain remove`](#orama-domain-remove) - Detach a domain
  - [`orama domain verify`](#orama-domain-verify) - Check the TXT record and activate the domain
- [`orama edit`](#orama-edit) - Change a node you already installed: storage size, exit role
- [`orama function`](#orama-function) - Manage serverless functions
  - [`orama function build`](#orama-function-build) - Build a function to WASM using TinyGo
  - [`orama function delete`](#orama-function-delete) - Delete a deployed function
  - [`orama function deploy`](#orama-function-deploy) - Deploy a function to the Orama Network
  - [`orama function disable`](#orama-function-disable) - Disable a function without deleting it
  - [`orama function enable`](#orama-function-enable) - Re-enable a previously disabled function
  - [`orama function get`](#orama-function-get) - Get details of a deployed function
  - [`orama function init`](#orama-function-init) - Create a new serverless function project
  - [`orama function invoke`](#orama-function-invoke) - Invoke a deployed function
  - [`orama function list`](#orama-function-list) - List deployed functions
  - [`orama function logs`](#orama-function-logs) - Get invocation history for a function
  - [`orama function rollback`](#orama-function-rollback) - Make an earlier version of a function the current one
  - [`orama function secrets`](#orama-function-secrets) - Manage function secrets
    - [`orama function secrets delete`](#orama-function-secrets-delete) - Delete a secret
    - [`orama function secrets list`](#orama-function-secrets-list) - List secret names
    - [`orama function secrets set`](#orama-function-secrets-set) - Set a secret
  - [`orama function triggers`](#orama-function-triggers) - Manage function PubSub and cron triggers
    - [`orama function triggers add`](#orama-function-triggers-add) - Add a PubSub or Cron trigger
    - [`orama function triggers delete`](#orama-function-triggers-delete) - Delete a trigger
    - [`orama function triggers list`](#orama-function-triggers-list) - List triggers for a function
  - [`orama function versions`](#orama-function-versions) - List all versions of a function
- [`orama global`](#orama-global) - Install and operate a global node, and build its chain messages
  - [`orama global bind`](#orama-global-bind) - Sign orama-global-bind-v1 for one service key
  - [`orama global bond`](#orama-global-bond) - Bond norama to one role on a global node
  - [`orama global capacity`](#orama-global-capacity) - Declare how many bytes a storage node will hold
  - [`orama global install`](#orama-global-install) - Install the global services on this node (run as root)
  - [`orama global register`](#orama-global-register) - Register a global node from signed service-key bindings
  - [`orama global restart`](#orama-global-restart) - Restart the installed global services in order (run as root)
  - [`orama global retire`](#orama-global-retire) - Retire a global node
  - [`orama global start`](#orama-global-start) - Start the installed global services, chain first (run as root)
  - [`orama global stop`](#orama-global-stop) - Stop the installed global services, chain last (run as root)
  - [`orama global tor`](#orama-global-tor) - This node's Tor identities and the consensus it holds
    - [`orama global tor info`](#orama-global-tor-info) - Show this node's Tor identities and the consensus it holds (run as root)
    - [`orama global tor relay-identity`](#orama-global-tor-relay-identity) - Sign what registering this node's Tor relay on the chain needs (run as root, on the relay's node)
  - [`orama global unbond`](#orama-global-unbond) - Start unbonding norama from one role
- [`orama maint`](#orama-maint) - Maintainer commands: build, release, inspect, install and repair
  - [`orama maint build`](#orama-maint-build) - Build pre-compiled binary archive for deployment
  - [`orama maint cluster`](#orama-maint-cluster) - Choose who may create namespaces on this cluster, and its update policy
    - [`orama maint cluster creators`](#orama-maint-cluster-creators) - Wallets that may create namespaces when creation is allowlist
      - [`orama maint cluster creators add`](#orama-maint-cluster-creators-add) - Let a wallet create namespaces when creation is allowlist
      - [`orama maint cluster creators list`](#orama-maint-cluster-creators-list) - List wallets allowed to create namespaces
      - [`orama maint cluster creators remove`](#orama-maint-cluster-creators-remove) - Take a wallet off the namespace-creator list
    - [`orama maint cluster settings`](#orama-maint-cluster-settings) - Show or change the cluster's settings
      - [`orama maint cluster settings set`](#orama-maint-cluster-settings-set) - Change namespace creation, the per-wallet cap or the update policy
      - [`orama maint cluster settings show`](#orama-maint-cluster-settings-show) - Show who may create namespaces, the per-wallet cap and the update policy
  - [`orama maint faucet`](#orama-maint-faucet) - Set up the public faucet of a test network on this node
    - [`orama maint faucet init`](#orama-maint-faucet-init) - Create this node's faucet key and print the account to fund
  - [`orama maint global`](#orama-maint-global) - Validator keys, chain binary staging, the Tor network's authorities and the transaction gate
    - [`orama maint global edit`](#orama-maint-global-edit) - Change this node's public storage size or exit role (run as root)
    - [`orama maint global refresh`](#orama-maint-global-refresh) - Bring the installed global services up to the staged release (run as root)
    - [`orama maint global stage-oramad`](#orama-maint-global-stage-oramad) - Place a TUF-verified oramad in the cosmovisor layout
    - [`orama maint global tor`](#orama-maint-global-tor) - The Orama Tor network: authority key ceremony, vote archive, relay monitor, onion list
      - [`orama maint global tor archive`](#orama-maint-global-tor-archive) - Archive this directory authority's consensus and votes (run by orama-global-tor-archive.timer)
      - [`orama maint global tor authority-keys`](#orama-maint-global-tor-authority-keys) - Make this machine's directory-authority keys, or read back the ones it has (run as root)
      - [`orama maint global tor ceremony`](#orama-maint-global-tor-ceremony) - Generate the directory authorities' keys and the network file (run on an offline machine)
      - [`orama maint global tor monitor`](#orama-maint-global-tor-monitor) - Write this relay's or directory authority's monitor.json for the node report (run by orama-global-tor-monitor.timer)
      - [`orama maint global tor onions`](#orama-maint-global-tor-onions) - The validator onion services the network file lists
        - [`orama maint global tor onions add`](#orama-maint-global-tor-onions-add) - Add validator onion services to the network file clients join with
    - [`orama maint global txgate`](#orama-maint-global-txgate) - Serve the validator's transaction gate on loopback (run by orama-global-txgate.service)
    - [`orama maint global validator`](#orama-maint-global-validator) - Back up, move and manage this node's validator key
      - [`orama maint global validator check-sign-floor`](#orama-maint-global-validator-check-sign-floor) - Fail when the chain must not start: key moved away or state behind its floor
      - [`orama maint global validator edit`](#orama-maint-global-validator-edit) - Build or send MsgEditValidator (description, commission)
      - [`orama maint global validator export-key`](#orama-maint-global-validator-export-key) - Write priv_validator_key.json sealed to the operator's public key (run as root)
      - [`orama maint global validator migrate`](#orama-maint-global-validator-migrate) - Move the validator key to another host without a double sign
        - [`orama maint global validator migrate cancel`](#orama-maint-global-validator-migrate-cancel) - Remove this host's prepared migration key (run on the new host)
        - [`orama maint global validator migrate export`](#orama-maint-global-validator-migrate-export) - Stop the chain and seal the key and its sign state (run on the old host)
        - [`orama maint global validator migrate import`](#orama-maint-global-validator-migrate-import) - Install a migrated key and record its sign floor (run on the new host)
        - [`orama maint global validator migrate prepare`](#orama-maint-global-validator-migrate-prepare) - Print this host's migration key (run on the new host)
      - [`orama maint global validator reseal`](#orama-maint-global-validator-reseal) - Turn a key backup into a migration bundle for a new host
      - [`orama maint global validator unjail`](#orama-maint-global-validator-unjail) - Build or send MsgUnjail for the operator's validator
  - [`orama maint inspect`](#orama-maint-inspect) - Inspect cluster health via SSH
  - [`orama maint invite`](#orama-maint-invite) - Mint an invite for a new node
  - [`orama maint network`](#orama-maint-network) - Maintain the published networks
    - [`orama maint network announce`](#orama-maint-network-announce) - Write networks/&lt;name>/ for a network that does not exist yet
    - [`orama maint network cluster-only`](#orama-maint-network-cluster-only) - Write networks/&lt;name>/ for a network of clusters that runs no chain
    - [`orama maint network publish`](#orama-maint-network-publish) - Write networks/&lt;name>/ for a chain that was just deployed
  - [`orama maint node`](#orama-maint-node) - Install, stage, recover and migrate nodes
    - [`orama maint node autoupdate`](#orama-maint-node-autoupdate) - Decide whether a newer release should be installed
      - [`orama maint node autoupdate run`](#orama-maint-node-autoupdate-run) - Look for a newer release on the cluster's channel and act on it (requires sudo)
    - [`orama maint node enroll`](#orama-maint-node-enroll) - Enroll an OramaOS node into the cluster
    - [`orama maint node install`](#orama-maint-node-install) - Install production node (requires sudo)
    - [`orama maint node migrate-conf`](#orama-maint-node-migrate-conf) - Register nodes.conf nodes with your wallet
    - [`orama maint node migrate-raft-id`](#orama-maint-node-migrate-raft-id) - Move nodes to stable, peer-id-based raft identities (one-time)
    - [`orama maint node recover-raft`](#orama-maint-node-recover-raft) - Recover RQLite cluster from split-brain
    - [`orama maint node schema`](#orama-maint-node-schema) - Inspect and apply gateway schema migrations against the local RQLite
      - [`orama maint node schema apply`](#orama-maint-node-schema-apply) - Apply pending migrations to the local RQLite
      - [`orama maint node schema status`](#orama-maint-node-schema-status) - Show required vs applied schema version + pending migrations
    - [`orama maint node stage-archive`](#orama-maint-node-stage-archive) - Verify a pushed build archive and put it in place (run by 'orama maint push')
    - [`orama maint node unlock`](#orama-maint-node-unlock) - Unlock an OramaOS genesis node
  - [`orama maint operator`](#orama-maint-operator) - Operate the cluster
    - [`orama maint operator add`](#orama-maint-operator-add) - Let another wallet operate this cluster
    - [`orama maint operator list`](#orama-maint-operator-list) - List the wallets that operate this cluster
    - [`orama maint operator remove`](#orama-maint-operator-remove) - Take a wallet off this cluster's operator list
    - [`orama maint operator rotate-secrets`](#orama-maint-operator-rotate-secrets) - Re-encrypt stored secrets, optionally under a new encryption root
    - [`orama maint operator rotate-signing-key`](#orama-maint-operator-rotate-signing-key) - Replace the key this gateway signs tokens with
  - [`orama maint push`](#orama-maint-push) - Push the binary archive to your nodes
  - [`orama maint release`](#orama-maint-release) - Cut and publish a signed release (maintainers)
    - [`orama maint release cut`](#orama-maint-release-cut) - List an archive on a channel and sign the metadata (3 approvals)
    - [`orama maint release init-root`](#orama-maint-release-init-root) - Make the repository's root from your RootWallet's release key
    - [`orama maint release publish`](#orama-maint-release-publish) - Upload what cut left: archives to Gitea, metadata to the release host
    - [`orama maint release refresh-timestamp`](#orama-maint-release-refresh-timestamp) - Re-sign only the timestamp (1 approval)
    - [`orama maint release renew-root`](#orama-maint-release-renew-root) - Make the next version of the root with a new expiry
  - [`orama maint rollout`](#orama-maint-rollout) - Build, push, and rolling upgrade every node in an environment
  - [`orama maint sandbox`](#orama-maint-sandbox) - Manage ephemeral Hetzner Cloud clusters for testing
    - [`orama maint sandbox create`](#orama-maint-sandbox-create) - Create a new 5-node sandbox cluster (~5 min)
    - [`orama maint sandbox destroy`](#orama-maint-sandbox-destroy) - Destroy a sandbox cluster and release resources
    - [`orama maint sandbox list`](#orama-maint-sandbox-list) - List active sandbox clusters
    - [`orama maint sandbox reset`](#orama-maint-sandbox-reset) - Delete all sandbox infrastructure and config to start fresh
    - [`orama maint sandbox rollout`](#orama-maint-sandbox-rollout) - Build + push + rolling upgrade to sandbox cluster
    - [`orama maint sandbox setup`](#orama-maint-sandbox-setup) - Interactive setup: Hetzner API key, domain, floating IPs, SSH key
    - [`orama maint sandbox ssh`](#orama-maint-sandbox-ssh) - SSH into a sandbox node (1-5)
    - [`orama maint sandbox status`](#orama-maint-sandbox-status) - Show cluster health report
  - [`orama maint vpn`](#orama-maint-vpn) - Route traffic through an Orama Tor network
    - [`orama maint vpn check`](#orama-maint-vpn-check) - Join an Orama Tor network and reach a validator onion service through it
    - [`orama maint vpn up`](#orama-maint-vpn-up) - Run a SOCKS5 proxy into an Orama Tor network
- [`orama members`](#orama-members) - Manage who may work in a namespace
  - [`orama members add`](#orama-members-add) - Give a wallet a role in this namespace
  - [`orama members list`](#orama-members-list) - List who holds a grant in this namespace
  - [`orama members remove`](#orama-members-remove) - Take a wallet's grant away
  - [`orama members transfer`](#orama-members-transfer) - Hand this namespace to another wallet
- [`orama namespace`](#orama-namespace) - Manage namespaces
  - [`orama namespace attestation-policy`](#orama-namespace-attestation-policy) - Show or set which app builds may call a namespace (device attestation)
  - [`orama namespace backup`](#orama-namespace-backup) - Take a backup of the namespace, sealed to your X25519 public key
  - [`orama namespace backup-open`](#orama-namespace-backup-open) - Decrypt a backup file with an X25519 private key
  - [`orama namespace backup-seal`](#orama-namespace-backup-seal) - Encrypt a backup file to an X25519 public key
  - [`orama namespace cors-origins`](#orama-namespace-cors-origins) - Show or set the browser origins a namespace's apps are served from
  - [`orama namespace create`](#orama-namespace-create) - Create a namespace and start its cluster
  - [`orama namespace delete`](#orama-namespace-delete) - Delete the current namespace and all its resources
  - [`orama namespace disable`](#orama-namespace-disable) - Disable a feature for a namespace
  - [`orama namespace enable`](#orama-namespace-enable) - Enable a feature for a namespace
  - [`orama namespace keys`](#orama-namespace-keys) - Manage scoped API keys (bugboard #148)
    - [`orama namespace keys create`](#orama-namespace-keys-create) - Mint a new scoped API key
    - [`orama namespace keys list`](#orama-namespace-keys-list) - List scoped API keys
    - [`orama namespace keys revoke`](#orama-namespace-keys-revoke) - Revoke a single API key by id
    - [`orama namespace keys revoke-legacy`](#orama-namespace-keys-revoke-legacy) - Revoke ALL legacy (unscoped) keys — the cutover step
    - [`orama namespace keys rotate`](#orama-namespace-keys-rotate) - Mint a successor to a key and keep the old one working for an overlap
  - [`orama namespace list`](#orama-namespace-list) - List namespaces owned by the current wallet
  - [`orama namespace repair`](#orama-namespace-repair) - Repair an under-provisioned namespace cluster
  - [`orama namespace restore`](#orama-namespace-restore) - Restore a namespace backup onto the namespace gateway (DESTRUCTIVE)
  - [`orama namespace restore-key`](#orama-namespace-restore-key) - Print the namespace gateway's restore public key
  - [`orama namespace rqlite`](#orama-namespace-rqlite) - Manage the namespace's internal RQLite database
    - [`orama namespace rqlite export`](#orama-namespace-rqlite-export) - Export the namespace's RQLite database to a local SQLite file
    - [`orama namespace rqlite import`](#orama-namespace-rqlite-import) - Import a SQLite dump into the namespace's RQLite (DESTRUCTIVE)
  - [`orama namespace session-policy`](#orama-namespace-session-policy) - Show or set who may sign in to a namespace and what its sessions bind
  - [`orama namespace webrtc-status`](#orama-namespace-webrtc-status) - Show WebRTC service status for a namespace
- [`orama network`](#orama-network) - Choose the network the CLI talks to
  - [`orama network add`](#orama-network-add) - Add a network by its manifest, or a cluster by its gateway
  - [`orama network current`](#orama-network-current) - Show the active network
  - [`orama network list`](#orama-network-list) - List every network and where it comes from
  - [`orama network remove`](#orama-network-remove) - Forget a network
  - [`orama network use`](#orama-network-use) - Make a network the active one
- [`orama node`](#orama-node) - Node operator commands
  - [`orama node dns`](#orama-node-dns) - Cluster DNS: what the outside world needs to reach its nameservers
    - [`orama node dns delegation`](#orama-node-dns-delegation) - Print the NS and glue records to create at the parent zone
  - [`orama node doctor`](#orama-node-doctor) - Diagnose common node issues
  - [`orama node invite`](#orama-node-invite) - Manage invite tokens for joining the cluster
  - [`orama node list`](#orama-node-list) - List your nodes across environments
  - [`orama node logs`](#orama-node-logs) - View production service logs
  - [`orama node remove`](#orama-node-remove) - Remove one node from the cluster, then erase it (replaced by orama remove)
  - [`orama node report`](#orama-node-report) - Output comprehensive node health data as JSON
  - [`orama node restart`](#orama-node-restart) - Restart all production services (requires sudo)
  - [`orama node setup`](#orama-node-setup) - Set up a fresh VPS as an Orama node (use orama setup)
  - [`orama node start`](#orama-node-start) - Start all production services (requires sudo)
  - [`orama node status`](#orama-node-status) - Show the service status of the node on this machine
  - [`orama node stop`](#orama-node-stop) - Stop all production services (requires sudo)
  - [`orama node tls`](#orama-node-tls) - This node's TLS certificates
    - [`orama node tls status`](#orama-node-tls-status) - Show whether this node holds its certificates, and why not (requires sudo)
  - [`orama node trust`](#orama-node-trust) - Manage what this node accepts code from, besides its operator's wallet
    - [`orama node trust add-root`](#orama-node-trust-add-root) - Adopt a TUF release root on this node (requires sudo)
  - [`orama node uninstall`](#orama-node-uninstall) - Remove production services (requires sudo)
  - [`orama node upgrade`](#orama-node-upgrade) - Upgrade existing installation (requires sudo)
  - [`orama node wipe`](#orama-node-wipe) - Erase Orama from remote nodes (target-side only)
- [`orama nodes`](#orama-nodes) - List your nodes across environments
- [`orama remove`](#orama-remove) - Remove one node from your network, then erase it
- [`orama setup`](#orama-setup) - Join an Orama network: turn fresh VPSes into nodes
- [`orama ssh`](#orama-ssh) - SSH into a node
- [`orama status`](#orama-status) - Show your nodes, the cluster, the chain and your account
  - [`orama status alerts`](#orama-status-alerts) - Alerts, most severe first, with what to do (one-shot)
  - [`orama status chain`](#orama-status-chain) - Orama L1 height, sync and validators (one-shot)
  - [`orama status cluster`](#orama-status-cluster) - Verdict, components and a row per node (one-shot)
  - [`orama status dns`](#orama-status-dns) - DNS and TLS health of the nameservers (one-shot)
  - [`orama status mesh`](#orama-status-mesh) - WireGuard mesh connectivity (one-shot)
  - [`orama status namespaces`](#orama-status-namespaces) - Namespace health across nodes (one-shot)
  - [`orama status node`](#orama-status-node) - Per-node health details (one-shot)
  - [`orama status report`](#orama-status-report) - Full cluster report as JSON (one-shot)
  - [`orama status service`](#orama-status-service) - Service status across the cluster (one-shot)
  - [`orama status traffic`](#orama-status-traffic) - Gateway requests, errors and latency (one-shot)
- [`orama storage`](#orama-storage) - Storage deals on the Orama chain
  - [`orama storage accept`](#orama-storage-accept) - Accept an assigned storage slot
  - [`orama storage create`](#orama-storage-create) - Open a private or public-pin storage deal
  - [`orama storage decline`](#orama-storage-decline) - Decline an assigned storage slot
  - [`orama storage extend`](#orama-storage-extend) - Add epochs to a storage deal
  - [`orama storage get`](#orama-storage-get) - Fetch and open a private file from its providers
  - [`orama storage grant`](#orama-storage-grant) - Grant a cluster a capped deal allowance
  - [`orama storage open`](#orama-storage-open) - Open one sealed storage slot
  - [`orama storage prove`](#orama-storage-prove) - Submit storage challenge proofs
  - [`orama storage put`](#orama-storage-put) - Upload sealed slots to the providers a deal assigned
  - [`orama storage repair`](#orama-storage-repair) - Restore the replicas a deal lost, from the providers that still hold them
  - [`orama storage revoke`](#orama-storage-revoke) - Revoke a deal allowance
  - [`orama storage rewrap`](#orama-storage-rewrap) - Rebuild one storage slot from another slot's ciphertext
  - [`orama storage seal`](#orama-storage-seal) - Seal a file into one ciphertext per storage slot
- [`orama upgrade`](#orama-upgrade) - Upgrade your nodes to the newest signed release of the network's channel
- [`orama version`](#orama-version) - Show version information

## orama app

Manage deployed applications

```text
orama app
```

Aliases: `apps`

```text
List, get, delete, rollback, and view logs/stats for your deployed applications.
```

Subcommands: `delete`, `env`, `get`, `grants`, `list`, `logs`, `rollback`, `stats`

## orama app delete

Delete a deployment

```text
orama app delete <name> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | `false` | Delete without asking |


## orama app env

Manage an app's environment variables

```text
orama app env
```

```text
Read and change the environment variables a deployed app runs with.

Setting or removing a variable restarts the app, on every node that runs it,
so it picks up the change. The command succeeds only when every node applied
it. If a node could not be reached it is named in the error, still runs the old
environment, and running the same command again retries it.

Values are never printed back. They are where secrets live, so 'list' shows
names only.
```

Subcommands: `list`, `set`, `unset`

## orama app env list

List an app's environment variable names

```text
orama app env list <app>
```


## orama app env set

Set environment variables and restart the app

```text
orama app env set <app> [flags]
```

```text
Set one or more variables and restart the app.

Values given with --env never appear in shell history if you read them from a
file instead: --env-file takes a .env and sends every variable in it.
```

| Flag | Default | Description |
|---|---|---|
| `--env-file` | — | Read variables from a .env file |
| `--env` | — | Variable as KEY=VALUE (repeatable) |


## orama app env unset

Remove environment variables and restart the app

```text
orama app env unset <app> <KEY>...
```


## orama app get

Get deployment details

```text
orama app get <name>
```


## orama app grants

Say what a deployed app may do, as itself

```text
orama app grants
```

```text
Read and change what a deployment is allowed to reach.

Your app is handed a short-lived token of its own at start, in the file named by
$ORAMA_TOKEN_FILE, and renews it with the gateway before it expires. It reaches
nothing until you grant it something — which is the point: an app that ships
with no credential cannot leak one.

A deployment cannot be granted the control plane. If something needs to deploy
or mint keys, that is a person or a CI key, not an app.
```

Subcommands: `list`, `set`

## orama app grants list

Show what deployments in this namespace may do

```text
orama app grants list [app]
```


## orama app grants set

Grant a deployment a role

```text
orama app grants set <app> <role> [flags]
```

```text
Give a deployment a role in its own namespace.

  runtime  the data plane: invoke, storage, push, webrtc, proxy, pubsub, cache;
           a deployed app also sends push notifications on it
  reader   nothing beyond the routes that ask for no grant

The change reaches a running app within seconds: a deployment's grant is read
wherever its token is checked (cached for 10 seconds on each node), so it needs
neither a redeploy nor a new token.
```

| Flag | Default | Description |
|---|---|---|
| `--resource` | — | Narrow the role to a resource, e.g. pubsub:topic=orders.* |


## orama app list

List all deployments

```text
orama app list
```


## orama app logs

Stream deployment logs

```text
orama app logs <name> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-f`, `--follow` | `false` | Follow log output |
| `-n`, `--lines` | `100` | Number of lines to show |


## orama app rollback

Rollback a deployment to a previous version

```text
orama app rollback <name> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--version` | `0` | Version to rollback to (required) |
| `--yes` | `false` | Roll back without asking |


## orama app stats

Show resource usage for a deployment

```text
orama app stats <name>
```


## orama audit

Read this namespace's audit trail, or the cluster's

```text
orama audit [flags]
```

```text
Print what has happened in a namespace: sign-ins, keys minted and revoked,
grants given and taken away, deployments, functions, secrets and namespace changes.

--cluster prints the cluster's own events instead, the ones recorded under no
namespace: invites minted, nodes joining and being claimed, and namespaces
deleted. Only a wallet on the cluster's operator list may read them.

Events are shown oldest first. --follow keeps the command running and prints new
ones as they are recorded.

Actions: auth.challenge, auth.verify, auth.refresh, auth.refresh.replay, auth.logout, key.issue, key.revoke, key.rotate, key.revoke_all, namespace.create, namespace.delete, namespace.operator_remove, secret.set, secret.delete, function.deploy, function.delete, function.purge, function.rollback, deployment.deploy, deployment.delete, operator.action, auth.legacy_credential, grant.add, grant.revoke, namespace.transfer, namespace.backup, namespace.restore, auth.device.start, auth.device.approve, auth.device.deny, auth.device.claim, auth.device.revoke, namespace.session_policy, namespace.sign_in_policy, namespace.attestation_policy, node.register, node.key.enrol
```

| Flag | Default | Description |
|---|---|---|
| `--action` | — | Show only this action |
| `--cluster` | `false` | Read the cluster's own events (invites, node joins and claims, namespace deletions); operators only |
| `--limit` | `0` | How many events to fetch at once (default 50, max 200) |
| `--namespace` | — | Namespace name |
| `--principal` | — | Show only what this wallet or key did |
| `--since` | — | Show only what happened after this time (RFC3339, or the created_at of a row) |
| `-f`, `--follow` | `false` | Keep running and print new events as they are recorded |


## orama auth

Authentication management

```text
orama auth
```

```text
Manage authentication with the Orama network.

Signing in is a wallet signature over a gateway challenge. On a machine with
RootWallet running it is signed here; on one without — a server reached over
SSH, a container, CI — 'orama auth login' prints a code and 'orama auth approve'
on a machine that does have a wallet approves it.

What is stored is a session, not a key: an access token lasting 15 minutes,
renewed transparently from a refresh token.
```

Subcommands: `approve`, `list`, `login`, `logout`, `sessions`, `status`, `switch`, `whoami`

## orama auth approve

Approve a login waiting on another machine

```text
orama auth approve <code> [flags]
```

```text
Approve the code 'orama auth login' printed on a machine with no wallet on it.

It costs the same wallet signature signing in does, which is what makes the code
on its own worthless. --deny refuses instead, so the waiting machine stops
rather than polling until the code expires.
```

| Flag | Default | Description |
|---|---|---|
| `--deny` | `false` | Refuse the login instead of approving it |
| `--namespace` | — | Namespace to sign in to (defaults to the one this machine is signed in to) |


## orama auth list

List all stored credentials

```text
orama auth list
```


## orama auth login

Sign in, here or from another machine

```text
orama auth login [flags]
```

```text
Sign in, here or from another machine.

Run at a terminal with no --namespace and a credential already saved, it first
offers the saved ones to switch to. With --namespace, or without a terminal
(a script, CI), it signs in straight away.

--device-key enrolls that Ed25519 key with this sign-in. The file is a private
JWK and stays on this machine; the gateway receives the public half and the
device's signature over the same message the wallet signs.
```

| Flag | Default | Description |
|---|---|---|
| `--device-key` | — | Ed25519 private JWK file to enroll with this sign-in |
| `--namespace` | — | Namespace name |


## orama auth logout

End this session on the gateway and clear it here

```text
orama auth logout [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--all` | `false` | End every session for this wallet, not only this machine's |


## orama auth sessions

Which machines are signed in as this wallet

```text
orama auth sessions
```

Subcommands: `revoke`

## orama auth sessions revoke

End one session, or every one

```text
orama auth sessions revoke [id] [flags]
```

```text
End a session listed by 'orama auth sessions'.

Ending a session stops it minting new access tokens, and refuses the access
tokens it already minted — and closes the sockets they hold open — within ten
seconds. A session issued before sessions carried an id cannot be named that
way: its access tokens work until they expire, at most 15 minutes, and the
command says so.
```

| Flag | Default | Description |
|---|---|---|
| `--all` | `false` | End every session for this wallet |


## orama auth status

Show what is stored on this machine, without asking the gateway

```text
orama auth status
```


## orama auth switch

Switch between stored credentials

```text
orama auth switch
```


## orama auth whoami

Ask the gateway who this credential is and what it may do

```text
orama auth whoami
```


## orama chain

Read the Orama chain, send ORAMA, withdraw earnings; fund test accounts

```text
orama chain [flags]
```

```text
Read the Orama chain. Every command here only reads, except 'faucet', which
funds an account on a test network, 'send', which pays another account, and
'withdraw-earnings', which moves your earnings to your own balance.

Three read paths exist, and each command uses one:

  --gateway  the gateway's read-only /v1/chain/ proxy (default: the active
             environment's gateway). Status, blocks, transactions, the
             validator set, supply, the indexer and the Orama module queries
             (x/nodes, x/storage, x/fees, ...) under /v1/chain/query/.
  --node     a node's Cosmos REST API, for example http://127.0.0.1:31003.
             Accounts, bank balances, staking validators.
  --rpc      a node's CometBFT RPC, for example http://127.0.0.1:31001. The
             Orama modules are on a node's REST API too, but the CLI reads them
             through abci_query; with --rpc set, earnings, node, deal and query read
             it directly instead of through the gateway.

Node, deal and cluster transactions are built and signed by 'orama global',
'orama storage' and 'orama cluster'; --onion on those submits through Tor.
'send' and 'withdraw-earnings' are signed by your RootWallet and sent through the
gateway, or --node; 'send' is private unless --public is given (see 'orama chain
send'). 'faucet' signs on a node over SSH (see 'orama chain faucet').
```

| Flag | Default | Description |
|---|---|---|
| `--gateway` | — | Gateway URL for /v1/chain/ reads (default: the active environment's gateway) |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003 |
| `--rpc` | — | CometBFT RPC, for example http://127.0.0.1:31001 |

Subcommands: `balance`, `deal`, `earnings`, `faucet`, `node`, `query`, `send`, `status`, `validator`, `withdraw-earnings`

## orama chain balance

Show an account's bank balances

```text
orama chain balance <address>
```

```text
Show an account's bank balances from --node's REST API. This is the account's
spendable bank balance. Earnings live in a separate account: see
'orama chain earnings'.
```


## orama chain deal

Show a storage deal (x/storage)

```text
orama chain deal <deal-id>
```

```text
Show a storage deal from x/storage through the gateway (or --rpc).
```


## orama chain earnings

Show an account's earnings balance (x/fees)

```text
orama chain earnings <address>
```

```text
Show the earnings balance x/fees holds for an account, through the gateway (or --rpc). Earnings
are what the account is paid for running nodes and services; they are not in
the bank balance.
```


## orama chain faucet

Fund an account on a test network (testnet, devnet)

```text
orama chain faucet <recipient> [flags]
```

```text
Send test ORAMA to an account from the chain's faucet (MsgFaucet).

The faucet exists only on a test network: a chain whose id is exactly orama-[<name>-]testnet-<id>, orama-[<name>-]devnet-<id> or orama-[<name>-]localnet-<id> and whose genesis turned it on (faucet_enabled). This
command reads the chain id from the node first and refuses any other chain
before it signs anything. The chain refuses a drip over its maximum and a second
drip to the same recipient inside its cooldown (24 hours by default).

The transaction is signed ON a node, with the node's operator key (the
"validator" key of oramad's test keyring), over SSH with the environment's
wallet-provided key. The key never leaves the node and nothing secret is
printed. The operator pays the fee from its earnings. The recipient need not
exist yet. --amount is in norama (1 ORAMA = 1000000000 norama).

Prints the transaction hash, the amount, and the recipient's bank balance once
the transaction is in a block.

  orama chain faucet orama1fvfzzvqv2ara2crn3z352zjhnfl0tw4rk82j53 --env testnet
  orama chain faucet <address> --env testnet --amount 5000000000 --node 57.129.166.16

--node is the SSH host of the node that signs (here it is not the REST URL the
other 'orama chain' commands take); without it the environment's first node
signs.
```

| Flag | Default | Description |
|---|---|---|
| `--amount` | `100000000000` | Amount of norama to send (1 ORAMA = 1000000000 norama) |
| `--env` | — | Environment whose node signs (default: the active environment) |
| `--node` | — | SSH host (IP) of the node that signs (default: the environment's first node) |


## orama chain node

Show a registered node (x/nodes)

```text
orama chain node <node-id>
```

```text
Show a node's record from x/nodes through the gateway (or --rpc): operator, roles, bonds, endpoints, capacity and status.
```


## orama chain query

Run any Orama module query through the gateway or --rpc

```text
orama chain query <Service/Method> [request-json] [flags]
```

```text
Run a gRPC query of an Orama module and print the response as JSON. By default it
goes through the gateway's GET /v1/chain/query/<Service>/<Method>; with --rpc it
goes to that node's CometBFT abci_query. The request is JSON with the proto
field names. For example:

  orama chain query orama.nodes.v1.Query/Node '{"node_id":"node-1"}'
  orama chain query orama.nodes.v1.Query/Node '{"node_id":"node-1"}' --rpc http://127.0.0.1:31001

'orama chain query --list' prints every query the CLI knows.
```

| Flag | Default | Description |
|---|---|---|
| `--list` | `false` | List every query the CLI knows and exit |


## orama chain send

Send ORAMA, privately by default; --public sends openly

```text
orama chain send <to> <amount> [--public] [flags]
```

```text
Send ORAMA to another account. <amount> is in ORAMA, with up to nine decimals
(12, 0.5, 0.000000001).

A send is PRIVATE unless you say otherwise: value moves inside the shielded pool,
and the chain shows no sender, recipient or amount. A private send needs the
RootWallet to build the shielded bundle, and this RootWallet cannot yet (its agent
refuses shielded messages), so today a send without --public stops with that
explanation. It never turns into a public payment on its own.

--public is the only way to pay openly, and it is a choice you make each time:
the sender, the recipient and the amount are then visible on the chain to everyone,
permanently. The command prints the payment and that warning and asks you to type
"yes"; --yes skips the question for scripts. The RootWallet then shows the
transaction and asks you to approve it.

<to> is a shielded address (oramas1...) for a private send, and an orama1... account
for a public send. Withdraw earnings to your balance first ('orama chain
withdraw-earnings'); earnings cannot be sent directly.

Transactions go through the gateway of the selected network, or --node (a chain
REST API, for example one reached over an SSH tunnel). Either must be https, or on
this machine. The wallet signs only for the chain the selected network names (from
its registry manifest): an endpoint that answers another chain id is refused, and a
network that names none needs --chain-id. The fee is worked out from the chain and
shown before you confirm; one over --max-fee (1 ORAMA unless you raise it) is
refused before it is signed.

  orama chain send orama1fvfzzvqv2ara2crn3z352zjhnfl0tw4rk82j53 12.5 --public
  orama chain send orama1fvfzzvqv2ara2crn3z352zjhnfl0tw4rk82j53 0.5 --public --yes
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id you expect, for a network that does not name one (a network from the registry already does); refused if the endpoint runs another |
| `--max-fee` | — | Most the transaction may pay in fee, in ORAMA (default 1): a higher fee is refused before it is signed |
| `--public` | `false` | Send publicly: the sender, recipient and amount are visible on the chain |
| `--yes` | `false` | Do not ask before a public send |


## orama chain status

Show the chain's height, network and sync state

```text
orama chain status
```

```text
Show CometBFT's status: the network id, the latest block and whether the node is
catching up. Reads the gateway's /v1/chain/status, or --rpc's /status.
```


## orama chain validator

List the validator set, or show one validator

```text
orama chain validator [oramavaloper-address]
```

```text
Without an argument, list the CometBFT validator set from the gateway's
/v1/chain/validators (or --rpc's /validators). With an oramavaloper address,
show that validator's staking record from --node's REST API.
```


## orama chain withdraw-earnings

Move earnings to your own balance, where they can be sent

```text
orama chain withdraw-earnings <amount> [flags]
```

```text
Move <amount> ORAMA of your earnings to your own bank balance
(MsgWithdrawEarnings). <amount> is in ORAMA, with up to nine decimals.

Earnings are what your nodes are paid; they sit in a separate account that cannot
be sent from. Withdrawing makes them spendable: the amount is the one thing you
choose, the destination is always your own account, and the chain refuses an
amount above your earnings. See what you have with 'orama chain earnings <address>'.

Then use them as you choose: send them publicly ('orama chain send <to> <amount>
--public'), or move them into the shielded pool to keep them private (this needs a
RootWallet that builds shielded bundles). Withdrawing is itself visible on the
chain: it shows that this account withdrew this amount.

The RootWallet shows the transaction and asks you to approve it. Transactions go
through the gateway of the selected network, or --node, over https or on this
machine, and the wallet signs only for the chain the selected network names (see
'orama chain send' for --chain-id and --max-fee).

  orama chain withdraw-earnings 25
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id you expect, for a network that does not name one (a network from the registry already does); refused if the endpoint runs another |
| `--max-fee` | — | Most the transaction may pay in fee, in ORAMA (default 1): a higher fee is refused before it is signed |


## orama cluster

Register this cluster on the chain, and remove a tenant namespace

```text
orama cluster
```

```text
Register the cluster's public name on the chain, retire it, and remove a tenant's
namespace as an operator. Who may create namespaces and the cluster's update
policy are maintainer commands: see 'orama maint cluster'.
```

Subcommands: `namespace`, `register-onchain`, `retire-onchain`

## orama cluster namespace

Operator actions on a namespace

```text
orama cluster namespace
```

Subcommands: `remove`

## orama cluster namespace remove

Remove a namespace whose owner can no longer delete it

```text
orama cluster namespace remove <namespace> [flags]
```

```text
Remove a namespace and everything in it: its cluster on every node, its
deployments, its stored content (unless another namespace holds the same), its
keys and its grants.

The owner deletes a namespace with 'orama namespace delete'. This is for a
namespace whose owner cannot: the owner's wallet is lost, or it belonged to a
test run's throwaway wallet. Such a namespace keeps its port blocks and
processes on three nodes until an operator removes it.

It needs you signed in with a wallet on the operator list. --reason is
required and is written to the audit trail with your wallet
(namespace.operator_remove). You are asked to type the namespace name unless
--force is given.
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Do not ask to type the namespace name |
| `--reason` | — | Why the namespace is removed; recorded in the audit trail [required] |


## orama cluster register-onchain

Register this cluster's public name on the Orama chain

```text
orama cluster register-onchain [flags]
```

```text
Register an optional public row for this cluster on the Orama chain.

The row is the operator, the cluster id, the base domain, the public
endpoints, and an optional metadata URI. It does not include node addresses,
tenants, or any cluster secret, and registering it does not join a node.

--node is that chain's REST API. The command reads the account there, builds
a SIGN_MODE_DIRECT transaction, asks the RootWallet agent to sign that one
transaction, and broadcasts it. Without --node it prints the sign document
and does not submit anything. --onion sends the same transaction to a validator
onion service through a Tor SOCKS proxy on this machine instead, on a fresh
circuit, and never falls back to the clearnet: when Tor or the service is
unreachable the command fails and the transaction is not sent.

The fee is an explicit amount of norama. There is no default.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--base-domain` | — | Public base domain [required] |
| `--chain-id` | — | Chain id [required] |
| `--endpoint` | — | Public endpoint (repeatable) [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Cluster id [required] |
| `--metadata-uri` | — | HTTPS metadata URI |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex; required when the account has not signed before |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama cluster retire-onchain

Retire this cluster's public row on the Orama chain

```text
orama cluster retire-onchain [flags]
```

```text
Retire the optional public cluster row. This does not change any node and
does not delete the cluster's namespaces. Without --node the command prints
the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Cluster id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama db

Manage SQLite databases

```text
orama db
```

```text
Create and manage per-namespace SQLite databases.
```

Subcommands: `backup`, `backups`, `create`, `delete`, `list`, `query`

## orama db backup

Backup database to IPFS

```text
orama db backup <database_name>
```


## orama db backups

List backups for a database

```text
orama db backups <database_name>
```


## orama db create

Create a new SQLite database

```text
orama db create <database_name>
```


## orama db delete

Delete a database and its file

```text
orama db delete <database_name> [flags]
```

```text
Permanently delete a database.

The file and its write-ahead log are removed from the node that holds them.
There is no undo: restore from a backup with 'orama db backups' if you need the
data again.
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | `false` | Skip the confirmation prompt |


## orama db list

List all databases

```text
orama db list
```


## orama db query

Execute a SQL query

```text
orama db query <database_name> <sql>
```


## orama deploy

Deploy applications to the Orama network

```text
orama deploy
```

```text
Deploy static sites, Next.js apps, Go backends, and Node.js backends.
A name that is already deployed in the namespace is refused (409) unless you
redeploy it as an update with --update.
```

Subcommands: `go`, `nextjs`, `nodejs`, `static`

## orama deploy go

Deploy a Go backend

```text
orama deploy go <source_path> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--env-file` | — | Read environment variables from a .env file |
| `--env` | — | Environment variable as KEY=VALUE (repeatable) |
| `--health-check` | — | Path the platform polls to decide the app is up (default /health) |
| `--name` | — | Deployment name (required) |
| `--subdomain` | — | Custom subdomain |
| `--update` | `false` | Update existing deployment |


## orama deploy nextjs

Deploy a Next.js application

```text
orama deploy nextjs <source_path> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--env-file` | — | Read environment variables from a .env file |
| `--env` | — | Environment variable as KEY=VALUE (repeatable) |
| `--health-check` | — | Path the platform polls to decide the app is up (default /health) |
| `--name` | — | Deployment name (required) |
| `--ssr` | `false` | Deploy with SSR (server-side rendering) |
| `--subdomain` | — | Custom subdomain |
| `--update` | `false` | Update existing deployment |


## orama deploy nodejs

Deploy a Node.js backend

```text
orama deploy nodejs <source_path> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--env-file` | — | Read environment variables from a .env file |
| `--env` | — | Environment variable as KEY=VALUE (repeatable) |
| `--health-check` | — | Path the platform polls to decide the app is up (default /health) |
| `--name` | — | Deployment name (required) |
| `--subdomain` | — | Custom subdomain |
| `--update` | `false` | Update existing deployment |


## orama deploy static

Deploy a static site (React, Vue, etc.)

```text
orama deploy static <source_path> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--env-file` | — | Read environment variables from a .env file |
| `--env` | — | Environment variable as KEY=VALUE (repeatable) |
| `--name` | — | Deployment name (required) |
| `--subdomain` | — | Custom subdomain |
| `--update` | `false` | Update existing deployment |


## orama domain

Attach custom domains to your apps

```text
orama domain
```

```text
Add, verify, list and remove custom domains.

A domain is proved yours with a TXT record before it serves traffic. 'add'
prints the record to create, 'verify' checks it.
```

Subcommands: `add`, `list`, `remove`, `verify`

## orama domain add

Attach a domain to an app

```text
orama domain add <domain> [flags]
```

```text
Register a domain against a deployment and print the TXT record that proves
you own it.

The domain does not serve traffic until 'orama domain verify' succeeds.
```

| Flag | Default | Description |
|---|---|---|
| `--app` | — | Deployment to attach the domain to [required] |
| `--verify` | `false` | Wait for the TXT record and verify in one step |
| `--wait` | `5m0s` | How long --verify waits for the record to propagate |


## orama domain list

List your custom domains

```text
orama domain list [flags]
```

```text
List every custom domain in the namespace, or only one app's with --app.
```

| Flag | Default | Description |
|---|---|---|
| `--app` | — | Only this deployment's domains |


## orama domain remove

Detach a domain

```text
orama domain remove <domain>
```

```text
Remove a custom domain and the DNS record that pointed it at your app.
```


## orama domain verify

Check the TXT record and activate the domain

```text
orama domain verify <domain> [flags]
```

```text
Ask the gateway to resolve the domain's TXT record and, if it matches, start
serving the domain.

With --wait the check is repeated until the record appears, which is what a
freshly created DNS record needs.
```

| Flag | Default | Description |
|---|---|---|
| `--wait` | `0s` | Keep checking until the record appears, up to this long |


## orama edit

Change a node you already installed: storage size, exit role

```text
orama edit [flags]
```

```text
Change a setting of a node that is already installed, without installing it again.
With no setting flag, in a terminal, it opens a form: choose the node, then the
settings it has. With flags it changes exactly what they name.

  --storage-gb N    the public storage capacity the node offers. The node's public
                    Kubo is sized for N GB (its StorageMax becomes N plus 10%) and
                    restarted, and the capacity is declared on the chain
                    (MsgDeclareCapacity, signed by your RootWallet through an SSH
                    tunnel to a node's chain). The chain refuses a capacity the
                    node's role bond does not back, and one below the bytes deals
                    already reserve; if it refuses, the node is left as it was.
                    The chain needs the node's id there: --chain-node-id (see
                    'orama chain node <id>'), or --no-chain to resize the node only.
  --exit=true|false switch the node's Tor relay between a plain relay and an exit by
                    rewriting only the exit section of its torrc, and restart the
                    relay. An exit needs a network whose Tor file allows exits. The
                    node's roles on the chain are not changed by this.
  --global=...      the global layer cannot be turned on or off here, and edit says
                    what does it: running setup again for the IP adds it, 'orama remove'
                    takes a node out.

The change is made on the node by its own CLI, one node at a time, and the plan is
shown first; --yes skips the question. A node on a release without the node-side
command needs 'orama upgrade' first.

Examples:
  orama edit                                    # The form
  orama edit --node 203.0.113.7 --storage-gb 200 --chain-node-id node-7
  orama edit --node 203.0.113.7 --exit=true --yes
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id you expect, for a network that is on no registry network (one from the registry already names it); the wallet signs for no other chain |
| `--chain-node-id` | — | The node's id in the chain's node registry, to declare its capacity there |
| `--env` | — | Network the node belongs to (default: the active one) |
| `--exit` | `false` | Make the node's Tor relay an exit (true) or a plain relay (false) |
| `--global` | `false` | Ask for the global layer on or off (refused, with what does it) |
| `--no-chain` | `false` | Resize the node without declaring the capacity on the chain |
| `--node` | — | Public IP of the node to edit (default: ask) |
| `--storage-gb` | `0` | Public storage capacity to offer, in GB |
| `--yes` | `false` | Do not ask for confirmation |


## orama function

Manage serverless functions

```text
orama function
```

```text
Deploy, invoke, and manage serverless functions on the Orama Network.

A function is a folder containing:
  function.go    — your handler code (uses the fn SDK)
  function.yaml  — configuration (name, memory, timeout, etc.)
  go.mod         — the Go module TinyGo builds

Quick start:
  orama function init my-function
  cd my-function
  orama function build
  orama function deploy
  orama function invoke my-function --data '{"name": "World"}'
```

Subcommands: `build`, `delete`, `deploy`, `disable`, `enable`, `get`, `init`, `invoke`, `list`, `logs`, `rollback`, `secrets`, `triggers`, `versions`

## orama function build

Build a function to WASM using TinyGo

```text
orama function build [directory]
```

```text
Compiles function.go in the given directory (or current directory) to a WASM binary.
Requires TinyGo to be installed (https://tinygo.org/getting-started/install/).
```


## orama function delete

Delete a deployed function

```text
orama function delete <name> [flags]
```

```text
Deletes a function from the Orama Network: every version, or only the one
named with --version. A deleted version is never invoked again and cannot be
enabled or rolled back to, but its row stays and 'orama function list --all'
still shows it.

--purge removes the rows for good, with the function's environment variables,
triggers, invocation history, logs and jobs. Purging takes the namespace's
owner or an admin. The namespace's secrets are shared by every function and
stay. Purging the current version leaves the function with none until
'orama function rollback'.
```

| Flag | Default | Description |
|---|---|---|
| `--purge` | `false` | Remove the function's rows, triggers and history for good (owner or admin) |
| `--version` | `0` | Delete only this version |
| `-f`, `--force` | `false` | Skip confirmation prompt |


## orama function deploy

Deploy a function to the Orama Network

```text
orama function deploy [directory]
```

```text
Deploys the function in the given directory (or current directory).
If no .wasm file exists, it will be built automatically using TinyGo.
Reads configuration from function.yaml.
```


## orama function disable

Disable a function without deleting it

```text
orama function disable <name>
```

```text
Disables a deployed function. The function row stays in the registry but
new invocations are rejected. Use 'orama function enable' to resume.

Useful during incident response — pause a misbehaving function until you
can root-cause without losing its deployed code or version history.
```


## orama function enable

Re-enable a previously disabled function

```text
orama function enable <name>
```

```text
Re-enables a function that was paused with 'orama function disable'.
```


## orama function get

Get details of a deployed function

```text
orama function get <name>
```

```text
Retrieves and displays detailed information about a specific function.
```


## orama function init

Create a new serverless function project

```text
orama function init <name>
```

```text
Scaffolds a new directory with function.go, function.yaml, go.mod and a copy of the function SDK, ready for 'orama function build'.
```


## orama function invoke

Invoke a deployed function

```text
orama function invoke <name> [flags]
```

```text
Sends a request to invoke the named function with optional JSON payload.
```

| Flag | Default | Description |
|---|---|---|
| `--data` | `{}` | JSON payload to send to the function |


## orama function list

List deployed functions

```text
orama function list [flags]
```

```text
Lists the current version of every function deployed in the current namespace.

--all lists every version row the namespace holds instead, whatever its
status: active, inactive (disabled), superseded (replaced by a later deploy
or a rollback) and deleted. A row stays until it is pruned (past the newest
10 versions) or purged ('orama function delete --purge').
```

| Flag | Default | Description |
|---|---|---|
| `--all` | `false` | List every version, including disabled, superseded and deleted ones |


## orama function logs

Get invocation history for a function

```text
orama function logs <name> [flags]
```

```text
Retrieves the most recent invocations for a deployed function.

Each invocation record shows: timestamp, request_id, status, duration_ms,
and (if any) the error message. A second line says where the duration went:
the node whose gateway ran it, the wait for an execution slot (queue), loading
the module (load: 0 when it was in memory) and the database calls with their
summed time (db). WASM functions that emit log entries via log_info /
log_error have those entries nested under each record.

Pass --wasm-only to retrieve only the WASM-emitted log lines (legacy
behavior; rarely useful on functions that don't call log_info).
```

| Flag | Default | Description |
|---|---|---|
| `--limit` | `50` | Maximum number of records to retrieve |
| `--wasm-only` | `false` | Show only WASM-emitted log entries (legacy view) |


## orama function rollback

Make an earlier version of a function the current one

```text
orama function rollback <name> <version>
```

```text
Makes <version> the function's current version. Only the current version
of a function is invoked: a deploy supersedes the version before it, and
name@N of a superseded version answers 404 FUNCTION_VERSION_SUPERSEDED.
Rolling back supersedes the version that was current, moves the function's
triggers to <version>, and serves it on every path (HTTP, WebSocket,
triggers). A deleted or pruned version cannot be rolled back to; see
'orama function versions <name>' for what is kept. The next deploy becomes
current again with the next version number.
```


## orama function secrets

Manage function secrets

```text
orama function secrets
```

```text
Set, list, and delete encrypted secrets for your serverless functions.

Functions access secrets at runtime via the get_secret() host function.
Secrets are scoped to your namespace and encrypted at rest with AES-256-GCM.

Examples:
  orama function secrets set API_KEY "sk-abc123"
  orama function secrets set CERT_PEM --from-file ./cert.pem
  orama function secrets list
  orama function secrets delete API_KEY
```

Subcommands: `delete`, `list`, `set`

## orama function secrets delete

Delete a secret

```text
orama function secrets delete <name> [flags]
```

```text
Permanently deletes a secret. Functions will no longer be able to access it.
```

| Flag | Default | Description |
|---|---|---|
| `-f`, `--force` | `false` | Skip confirmation prompt |


## orama function secrets list

List secret names

```text
orama function secrets list
```

```text
Lists all secret names in the current namespace. Values are never shown.
```


## orama function secrets set

Set a secret

```text
orama function secrets set <name> [value] [flags]
```

```text
Stores an encrypted secret. Functions access it via get_secret("name"). If --from-file is used, value is read from the file instead.
```

| Flag | Default | Description |
|---|---|---|
| `--from-file` | — | Read secret value from a file |


## orama function triggers

Manage function PubSub and cron triggers

```text
orama function triggers
```

```text
Add, list, and delete triggers for your serverless functions.

PubSub: when a message is published to a topic, every function with a
matching trigger is invoked with the message as input.

Cron: a function is invoked on a schedule (5-field crontab, or 6-field
crontab with a leading seconds column).

Examples:
  orama function triggers add my-function --topic calls:invite
  orama function triggers add my-function --schedule "0 3 * * *"
  orama function triggers add my-function --schedule "*/30 * * * * *"
  orama function triggers list my-function
  orama function triggers delete my-function <trigger-id>
```

Subcommands: `add`, `delete`, `list`

## orama function triggers add

Add a PubSub or Cron trigger

```text
orama function triggers add <function-name> [flags]
```

```text
Registers a trigger that invokes the function automatically.

Pass exactly one of --topic (PubSub) or --schedule (cron). Schedules
accept either 5-field crontab (minute hour dom month dow) or 6-field
with seconds (sec minute hour dom month dow).
```

| Flag | Default | Description |
|---|---|---|
| `--schedule` | — | Cron expression to trigger on (e.g. "0 3 * * *") |
| `--topic` | — | PubSub topic to trigger on |


## orama function triggers delete

Delete a trigger

```text
orama function triggers delete <function-name> <trigger-id>
```


## orama function triggers list

List triggers for a function

```text
orama function triggers list <function-name>
```


## orama function versions

List all versions of a function

```text
orama function versions <name>
```

```text
Shows all deployed versions of a specific function. WASM SHA256 is the
SHA-256 of the WASM each version was deployed with, as uploaded, to compare
with 'shasum -a 256 function.wasm'; "-" for a version deployed before the
gateway recorded it. The WASM CID is of the sealed blob and differs on every
deploy.
```


## orama global

Install and operate a global node, and build its chain messages

```text
orama global
```

```text
Operate the global role.

On the node, as root: install puts the global services on this machine;
start, stop and restart run their units in order, chain first.

bind signs the binding that proves a service key belongs to an operator. The
private key stays in its file; the command writes the public key and the
signature. register, bond, unbond, capacity and retire build the node's chain
messages.
```

Subcommands: `bind`, `bond`, `capacity`, `install`, `register`, `restart`, `retire`, `start`, `stop`, `tor`, `unbond`

## orama global bind

Sign orama-global-bind-v1 for one service key

```text
orama global bind [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | Chain id the binding is for [required] |
| `--key-file` | — | Service secret file [required] |
| `--key-type` | — | secp256k1, ed25519, or ed25519-expanded; required for a raw 32-byte file |
| `--operator` | — | Operator account (orama1...) [required] |
| `--service` | — | Service name, for example provider or tor [required] |


## orama global bond

Bond norama to one role on a global node

```text
orama global bond [flags]
```

```text
Move norama from the operator account into the node's role bond.

The amount is added to the bond that role already holds. The node must already
be registered with that role. Without --node the command prints the sign
document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--amount` | — | Amount of norama [required] |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--role` | — | Role: validator, storage, relay, exit, dirauth, archiver [required] |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama global capacity

Declare how many bytes a storage node will hold

```text
orama global capacity [flags]
```

```text
Declare the storage capacity of a registered node that has the storage role.

The chain refuses a declaration above the capacity the role bond backs, and
below the bytes already reserved by deals. Zero is a declaration of no
capacity. Without --node the command prints the sign document and does not
submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--bytes` | `0` | Declared capacity in bytes |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama global install

Install the global services on this node (run as root)

```text
orama global install [flags]
```

```text
Install global services on this machine: chain, and optionally ipfs,
provider, archiver, indexer, repair, and the roles of the Orama Tor network
(dirauth, relay, relay,exit, onion) and a directory authority's bandwidth
reporter (reporter). The chain is required unless the machine only runs dirauth
or relay: the other services reach it only on this host's loopback RPC. provider
needs ipfs beside it (it pins public deals through the public Kubo). provider
and repair are never installed together. indexer is optional: it serves the chain read API on
loopback for a node that runs an RPC or index endpoint.

For each service it creates the service's system account, copies its binaries
(oramad and this orama CLI for the chain, whose unit runs 'orama global
validator check-sign-floor' before every start; ipfs, Kubo v0.43.1, for ipfs;
orama-global for the others) from --staged-dir into /usr/lib/orama-global/bin
(root-owned, 0755; a symlink in the staged directory is refused, and as root the
directory must be root's and not writable by others). A release archive's bin/
directory (/opt/orama/bin once the release is extracted) is the staged directory,
and the release's manifest (--manifest, /opt/orama/manifest.json) must list every
file the install reads with the digest it has; a file that is not the release's is
refused before anything on the host changes. It writes and enables its
orama-global-* unit, and opens its public port in ufw (31000 tcp+udp for the
chain, 31010 tcp+udp for the public Kubo swarm, 31013 tcp for the provider)
with the comment orama-global. It does not start anything: 'orama global start'
does, chain first.

The chain unit runs oramad under cosmovisor v1.7.3. Stage the official
cosmovisor-v1.7.3-linux-<amd64|arm64>.tar.gz beside the other binaries: its
SHA-256 must equal the pin built into this CLI, and only its cosmovisor file is
installed. oramad itself is placed in the chain home's cosmovisor layout as the
genesis binary, together with the release's shielded verifier (orama-orchard-verifier,
which the release ships beside oramad and whose digest oramad pins): the chain unit
passes oramad the one in the layout's current/bin, so an upgrade brings its own.
The chain home must already have a genesis (--init-chain, or an existing home). A binary already staged there with different bytes is
refused: change the chain binary with 'orama maint global stage-oramad --upgrade'.

The ipfs service is a public Kubo of its own: no swarm.key, its own repo in
/var/lib/orama-global/ipfs, swarm on 31010, RPC on 127.0.0.1:31011 (198.18.0.2:31011 with --colocated) behind a
token only the provider's group can read, and a GC timer. --public-storage-gb
is the capacity you will declare with 'orama global capacity'; Kubo's
StorageMax is that plus 10%. It never touches a private cluster's Kubo.

--init-chain creates the chain home with 'oramad init' as orama-chain and puts
the network's --genesis in place. It is never done without the flag, and it is
refused when the home already has a genesis.

--external-address <public ip>:31000 writes the chain's config.toml and app.toml
(the settings chain/scripts/testnet/deploy.sh used to sed in): the address the
node announces to its peers (the chain itself listens at the namespace address,
198.18.0.2, which no peer can reach), peer exchange off, Prometheus on 127.0.0.1,
custom pruning (keep 100, every 10 blocks), and a state-sync snapshot every 1000
blocks with two kept. A setting the chain's template no longer has is an error,
not a skipped line. Giving --statesync-rpc twice, with --statesync-trust-height
and --statesync-trust-hash, makes the node restore a snapshot on its first start
instead of replaying the chain: the servers are two nodes' light-client routes
(https://<host>/v1/chain/light), the height and hash a block both agreed on, and
the trust period is 7 days, shorter than the 21-day unbonding. Running it again
with the same flags changes nothing.

An inactive ufw is refused unless --enable-firewall is given; then incoming is
denied by default, --ssh-port is allowed, and ufw is enabled; --ssh-port must
be a port 'sshd -T' reports, or nothing is changed. Running the
command again with the same flags changes nothing but the binaries' bytes.

The roles of the Orama Tor network (orama.network/docs/operator/tor-network) run the distro's tor,
installed from the Tor Project's repository, with a torrc this command writes
from the network's tor-network.json, staged beside the binaries; the network file
is checked before anything on the host changes.
  relay           a relay (ORPort 31020/tcp). --tor-address, --tor-contact and
                  --tor-node-id are required; the nickname is derived from the
                  node id.
  relay,exit      the same relay as an exit, under a reduced exit policy. Opt-in:
                  the network file must say allow_exit. Destinations in
                  /var/lib/orama-global/tor-exit-reject (one CIDR or address, with
                  an optional :port, per line) are refused first.
  dirauth         a directory authority (ORPort 31020/tcp, DirPort 31021/tcp). It
                  is a relay already, so it never goes beside relay. It needs
                  --tor-address to be one of the network's authorities and
                  --tor-authority-keys, its bundle from 'orama global tor
                  ceremony'; a bundle that is not this authority's is refused.
                  A dirauth or relay host needs no chain, unless it also runs
                  the reporter.
  onion           the validator's onion service, forwarding to a tx gate on
                  loopback that serves only account read, broadcast and tx lookup.
                  It publishes no port and needs the chain.
  reporter        the authority's bandwidth reporter, 'orama-global reporter': it
                  reports each closed epoch's relay bandwidth and uptime to x/relay
                  from the authority's votes. It goes beside dirauth and chain, with
                  --tor-reporter-operator. The install writes the reporter's home
                  (/var/lib/orama-global/reporter) with its operator, the
                  authority-id (the v3_ident the network file lists for
                  --tor-address) and the vote-interval (the network file's
                  voting_interval_minutes); the hot key is created when the reporter first
                  starts, and its address still has to be added to x/relay's
                  reporter set and funded.

--colocated installs the services on a machine that already runs a cluster node
(orama node setup first). The global units run in their own network namespace,
orama-global, joined to the root namespace by a veth pair (198.18.0.0/30): they
have their own loopback and port space, cannot reach the cluster's loopback,
WireGuard mesh or any private network, and only the ports they publish are
forwarded in. It writes orama-global-netns.service, two nftables rulesets and a
resolv.conf under /etc/orama-global, and records role both in preferences.yaml.
The machine must have iproute2, nftables, a kernel with network namespaces and
veth, and systemd 242 or newer; otherwise nothing is changed. A machine that is
co-located must keep using --colocated on later installs.

On a co-located machine the chain's RPC and REST API (and the indexer) are
reachable on the namespace address only by root and the cluster node's account.
--chain-client-user <name> (repeatable) also allows a local account, for example
the ssh login that tunnels to the chain or runs 'orama chain'; an unknown account
refuses the install, and the set is kept by later installs.
```

| Flag | Default | Description |
|---|---|---|
| `--chain-client-user` | — | With --colocated: a local account, besides root and the cluster node's, allowed to connect to the chain's RPC and REST ports on the namespace address (repeatable; kept by later installs) |
| `--chain-id` | — | Chain id, with --init-chain |
| `--colocated` | `false` | Run the services in their own network namespace on a machine that also runs a cluster node |
| `--enable-firewall` | `false` | Enable an inactive ufw (deny incoming, allow --ssh-port) |
| `--external-address` | — | chain: the &lt;public ip>:31000 the node announces to its peers; writes config.toml and app.toml (pruning, snapshots, no peer exchange) |
| `--genesis` | — | The network's genesis.json, with --init-chain |
| `--init-chain` | `false` | Create the chain home with oramad init and install --genesis |
| `--manifest` | `/opt/orama/manifest.json` | The release's manifest.json, which must list every file the install reads with its digest |
| `--moniker` | — | Node moniker, with --init-chain |
| `--persistent-peers` | — | Chain peers, id@host:port,... (written into the chain unit) |
| `--public-storage-gb` | `0` | Capacity in GB you will declare for the provider; sizes the public Kubo (required with ipfs) |
| `--services` | — | Services: chain[,ipfs,provider,archiver,indexer,repair,dirauth,relay,exit,onion,reporter] [required] |
| `--ssh-port` | `22` | SSH port --enable-firewall allows |
| `--staged-dir` | — | Directory holding the release's oramad, orama-orchard-verifier (and its .sha256), orama, orama-global, ipfs and the cosmovisor tarball: the release's bin/ [required] |
| `--statesync-rpc` | — | chain: a light-client server https://&lt;host>/v1/chain/light the node restores a snapshot through (twice, from independent nodes); with --external-address |
| `--statesync-trust-hash` | — | chain: that block's hash (64 hex characters); with --statesync-rpc |
| `--statesync-trust-height` | `0` | chain: the block height both state-sync servers agreed on; with --statesync-rpc |
| `--tor-address` | — | dirauth, relay: the public IPv4 address the relay publishes |
| `--tor-authority-keys` | — | dirauth: the authority's key bundle from 'orama global tor ceremony' (deploy/&lt;nickname>) |
| `--tor-bandwidth-mbit` | `0` | dirauth, relay: limit on what the relay carries for others, in Mbit/s each way (0 = unlimited) |
| `--tor-contact` | — | dirauth, relay: ContactInfo published in the descriptor (the operator, and where an abuse complaint goes) |
| `--tor-family` | — | dirauth, relay: the RSA fingerprints of the operator's other relays |
| `--tor-node-id` | — | relay: the on-chain node id the relay's nickname is derived from |
| `--tor-reporter-operator` | — | reporter: the operator account address (orama1...) the reporter runs for; its relays are left out of a report |


## orama global register

Register a global node from signed service-key bindings

```text
orama global register [flags]
```

```text
Build MsgRegisterNode from bindings that 'orama global bind' wrote.

The message names the operator, a node id, roles, a hot key that is not the
operator, the bindings, public endpoints, an optional region and an optional
--asn, the autonomous system number the node declares. The chain cannot verify
the ASN; a protocol deal slot goes only to a node that declared one, and slots
go to distinct ASNs. Reserved, documentation and private-use numbers are
refused. It does not include a tenant list or a cluster secret.

--node is the chain REST API. The command reads the account there, asks the
RootWallet agent to sign this one transaction, and broadcasts it. Without
--node it prints the sign document and does not submit anything.

Each --binding file is the JSON bind printed. Its signature must verify for
this --chain-id and --operator.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--asn` | `0` | Autonomous system number the node declares (0 leaves it undeclared) |
| `--binding` | — | Binding JSON from orama global bind [required] |
| `--chain-id` | — | Chain id [required] |
| `--endpoint` | — | Public endpoint (repeatable) |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--hot-key` | — | Hot key account, not the operator [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--region` | — | Region hint |
| `--role` | — | Role: validator, storage, relay, exit, dirauth, archiver [required] |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama global restart

Restart the installed global services in order (run as root)

```text
orama global restart [service...] [flags]
```

```text
Stop then start the named global services (all installed ones when none is
named). Restarting the chain restarts every installed service, chain first.

A directory authority is restarted one at a time, at least 30 minutes apart:
see "orama global stop". --force overrides the check. The refusal for an authority
that was started too recently exits with code 9 and changes nothing, so a script that
rolls authorities can wait and ask again on that code alone.
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Stop or restart a directory authority although another has started less than 30 minutes ago (the network may lose its consensus) or its state cannot be read |


## orama global retire

Retire a global node

```text
orama global retire [flags]
```

```text
Retire a global node. The chain records its service pubkeys so they cannot
be bound again. Without --node the command prints the sign document and does
not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama global start

Start the installed global services, chain first (run as root)

```text
orama global start [service...]
```

```text
Start the installed orama-global-* units, or only the named ones.

The chain starts first. Before it starts, a validator key migrated to this host
is checked against the sign state it last had on its old host; a state behind
it is refused, since it could sign a step the old host already signed. The other
services start once the chain's loopback RPC answers. Starting ipfs, provider,
archiver, indexer or repair alone needs the chain already running. The public
Kubo's GC timer starts and stops with it.
```


## orama global stop

Stop the installed global services, chain last (run as root)

```text
orama global stop [service...] [flags]
```

```text
Stop the installed orama-global-* units, or only the named ones, in reverse
start order. Stopping the chain stops every installed service that needs it
first.

A directory authority is not stopped while another has started less than 30
minutes ago: a fresh authority casts no Running vote for that long and a
consensus needs two of the three (website/src/docs/operator/tor-network.mdx, "Directory authorities").
--force overrides it.
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Stop or restart a directory authority although another has started less than 30 minutes ago (the network may lose its consensus) or its state cannot be read |


## orama global tor

This node's Tor identities and the consensus it holds

```text
orama global tor
```

```text
Show the Tor roles installed on this node. The authority key ceremony, the vote
archive, the relay monitor and the onion list are maintainer commands: see
'orama maint global tor'.
```

Subcommands: `info`, `relay-identity`

## orama global tor info

Show this node's Tor identities and the consensus it holds (run as root)

```text
orama global tor info
```

```text
For each Tor role installed on this node (directory authority, relay or exit,
validator onion service), print the nickname and fingerprints tor made (what
'MsgRegisterRelay' and a node's onion endpoint need), the onion address, and a
summary of the consensus the process holds: when it is valid, how many relays
it lists, and whether it lists this relay. A role that has not started yet shows
no identity. The root's --json prints the same as a JSON array.
```


## orama global tor relay-identity

Sign what registering this node's Tor relay on the chain needs (run as root, on the relay's node)

```text
orama global tor relay-identity [flags]
```

```text
Print the relay's RSA fingerprint, the binding that makes its ed25519 master identity the
operator's service key "relay", the cross-certificate that ties the fingerprint to the node, and the RSA
identity's own proof of the fingerprint. x/relay pays the relay that a directory authority's vote names
by that ed25519 identity and that fingerprint, so the chain needs both, signed by the relay's own keys;
a fingerprint is public, so the RSA proof is what shows the registrant holds the key behind it. Both
signatures cover whether the relay is an exit (--exit), so a relay's proofs register it only with the
declaration they were made for.

The signatures are made here with the relay's key in its DataDirectory
(/var/lib/orama-global/tor-relay); the key is never printed or copied. 'orama setup' runs this on the
node and sends the registration; nothing is submitted by this command.
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | Chain id the binding is for [required] |
| `--exit` | `false` | Sign for a relay declared an exit; both signatures cover this declaration |
| `--home` | `/var/lib/orama-global/tor-relay` | The relay's tor DataDirectory |
| `--node-id` | — | The chain id of the node the relay runs on [required] |
| `--operator` | — | Operator account (orama1...) [required] |


## orama global unbond

Start unbonding norama from one role

```text
orama global unbond [flags]
```

```text
Start unbonding norama from one role on a registered global node.

The amount has to be covered by that role's bond. Without --node the command
prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--amount` | — | Amount of norama [required] |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--role` | — | Role: validator, storage, relay, exit, dirauth, archiver [required] |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama maint

Maintainer commands: build, release, inspect, install and repair

```text
orama maint
```

```text
Commands for the people who build, release and repair a network. An operator
who joins a network does not need any of them; they are here, out of the way,
and they all work.

  build, push, rollout       build a signed archive and roll it onto a cluster
  inspect, sandbox           check a cluster over SSH; throwaway Hetzner clusters
  invite                     mint an invite for a node to join a cluster
  operator, cluster          the cluster's operator wallets, creators and update policy
  vpn                        a Tor client for an Orama Tor network
  node                       install and stage a node, auto-update, recovery, migration
  global                     validator keys, chain binary staging, the Tor network, tx gate
  network                    publish a network's manifest
  faucet                     set up a test network's public faucet on this node
```

Subcommands: `build`, `cluster`, `faucet`, `global`, `inspect`, `invite`, `network`, `node`, `operator`, `push`, `release`, `rollout`, `sandbox`, `vpn`

## orama maint build

Build pre-compiled binary archive for deployment

```text
orama maint build [flags]
```

```text
Cross-compile all Orama binaries and dependencies for Linux,
then package them into a deployment archive. The archive includes:
  - Orama binaries (CLI, node, gateway, identity, SFU, TURN)
  - Olric, IPFS Kubo, IPFS Cluster, RQLite, CoreDNS, Caddy (built from
    checked-in, checksum-pinned modules; Kubo and RQLite by pinned digest)
  - The global layer, for amd64: oramad (with the orchard verifier linked) and its
    out-of-process verifier orama-orchard-verifier, orama-global and the pinned
    cosmovisor release, which 'orama global install' puts on a node. It needs
    rustup with the x86_64-unknown-linux-musl target, cargo, make and rsync.
    --skip-global-layer leaves it out (a cluster-only archive; arm64 always is).
  - Systemd namespace templates
  - manifest.json with checksums of every file, and manifest.sig

The manifest is signed with your RootWallet (the agent's active account, through
its wallet:sign capability). Nodes install only archives signed by an address in
their trust anchor, /etc/orama/archive-signers, so signing is the default;
--unsigned makes an archive without a wallet signature: the CI build that release
signers sign. A node installs it only when its release root accepted it
('orama maint node stage-archive --release-only'); otherwise it is for local inspection.

--signers rotates the trusted signers: nodes that install this build replace
their list with the given addresses. The build must be signed by a signer the
nodes trust now, and the list must include that signer; retiring a key takes
two builds (the old key adds the new one, the new key then drops the old).

--release-root <root.json> puts a TUF release root in the signed manifest. A node that
installs the build adopts it (/etc/orama/release-root.json) the way it takes a signer
rotation, and from then on accepts releases signed under that root
('orama maint node stage-archive --release-only', the auto-update agent).

The build is reproducible: with SOURCE_DATE_EPOCH set (a release build sets it to
the commit's time) two builds of one commit produce the same archive, byte for
byte. See docs/whitepaper/technical-reference/vol1/29-build-signing-and-release.md, "Reproducible builds".

The resulting archive can be pushed to nodes with 'orama maint push'.

Examples:
  orama maint build
  orama maint build --signers 0xYourWallet,0xNewOperator
  orama maint build --unsigned --output /tmp/inspect.tar.gz
```

| Flag | Default | Description |
|---|---|---|
| `--arch` | `amd64` | Target architecture (amd64, arm64) |
| `--output` | — | Output archive path (default: /tmp/orama-&lt;version>-linux-&lt;arch>.tar.gz) |
| `--release-root` | — | A TUF root.json to put in the signed manifest: nodes that install this build adopt it as their release root |
| `--signers` | — | Rotate the trusted archive signers: nodes that install this build trust only these addresses (comma-separated) |
| `--skip-global-layer` | `false` | Leave out the global layer (oramad, its verifier, orama-global, cosmovisor): a cluster-only archive |
| `--test-local-release-repo` | `false` | FOR TEST FLEETS ONLY: build the orama CLI so that ORAMA_ALLOW_LOCAL_RELEASE_REPO=1 lets it fetch releases from a loopback or private address |
| `--unsigned` | `false` | Do not sign the manifest (a node installs it only through its adopted TUF release root) |
| `--verbose` | `false` | Verbose output |


## orama maint cluster

Choose who may create namespaces on this cluster, and its update policy

```text
orama maint cluster
```

```text
Who may create a namespace on this cluster, and how many one wallet may own.

A new cluster allows only its operators. A cluster that already had a
namespace besides the seeded default, a node, or an operator when this was
upgraded stays open — any signed-in wallet — until an operator changes it.
The per-wallet cap stays 10 until an operator raises or lowers it.

Changing a setting or the creator list needs you signed in with a wallet on
the operator list, and is written to the audit trail.
```

Subcommands: `creators`, `settings`

## orama maint cluster creators

Wallets that may create namespaces when creation is allowlist

```text
orama maint cluster creators
```

```text
The allowlist consulted when namespace creation is allowlist.

An operator is not on it unless added. An empty list lets nobody create a
namespace, and removing the last wallet does not lock operators out.
```

Subcommands: `add`, `list`, `remove`

## orama maint cluster creators add

Let a wallet create namespaces when creation is allowlist

```text
orama maint cluster creators add <wallet>
```


## orama maint cluster creators list

List wallets allowed to create namespaces

```text
orama maint cluster creators list
```


## orama maint cluster creators remove

Take a wallet off the namespace-creator list

```text
orama maint cluster creators remove <wallet>
```


## orama maint cluster settings

Show or change the cluster's settings

```text
orama maint cluster settings
```

Subcommands: `set`, `show`

## orama maint cluster settings set

Change namespace creation, the per-wallet cap or the update policy

```text
orama maint cluster settings set <setting> <value>
```

```text
namespace-creation is operators, allowlist or open.

  operators   only wallets on the operator list
  allowlist   only wallets added with orama maint cluster creators add
  open        any signed-in wallet

max-namespaces-per-wallet is an integer from 1 to 10000. The default is 10.

The cluster's automatic updates (docs/whitepaper/technical-reference/vol1/29-build-signing-and-release.md, "Auto-update"):

  auto-update      off, notify (the default) or auto. notify reports a newer
                   release in 'orama status'; auto installs it, one node at a
                   time, when the cluster is healthy and the hour is in the window
  update-channel   the release channel to follow: stable (the default) or nightly
  update-window    start-end hours UTC when auto may install, for example 1-5;
                   empty for any hour
  release-repo     the https URL of the release repository; empty (the default)
                   means no updates are looked up
```


## orama maint cluster settings show

Show who may create namespaces, the per-wallet cap and the update policy

```text
orama maint cluster settings show
```


## orama maint faucet

Set up the public faucet of a test network on this node

```text
orama maint faucet
```

Subcommands: `init`

## orama maint faucet init

Create this node's faucet key and print the account to fund

```text
orama maint faucet init [flags]
```

```text
Create the key a node's gateway signs faucet drips with, and print the account
it belongs to. Run it on the node, as root.

The faucet gives test ORAMA to whoever asks (POST /v1/chain/faucet): the gateway
signs MsgFaucet for the recipient with this key and the chain mints the drip.
It exists only on a test network (a chain id of the form orama-[<name>-]testnet-<id>, orama-[<name>-]devnet-<id> or orama-[<name>-]localnet-<id>; the gateway refuses to sign anywhere else), the chain keeps its own
limits (a maximum drip, a cooldown per recipient, a cap per epoch), and it must
be on in the genesis (faucet_enabled).

The key file is created owned by the gateway's account with mode 0600, and an
existing key is never replaced: running init again prints the same account. The
faucet account pays the transaction fee of every drip and mints the drip itself,
so it needs a small balance and nothing more: fund it from the genesis
(chain/scripts/testnet/deploy.sh does this on testnet) or from another faucet.

Then turn it on in node.yaml and restart the node:

  chain:
    faucet:
      enabled: true

  orama node restart

orama node upgrade keeps the block. Check it with:

  curl -sS -X POST https://<gateway>/v1/chain/faucet \
    -H 'Content-Type: application/json' -d '{"recipient":"orama1..."}'
```

| Flag | Default | Description |
|---|---|---|
| `--key-file` | `/opt/orama/.orama/secrets/chain-faucet.key` | Where the key goes (node.yaml chain.faucet.key_file, when it is not this default) |
| `--owner` | `orama` | The account that owns the key file: the one the gateway runs as |


## orama maint global

Validator keys, chain binary staging, the Tor network's authorities and the transaction gate

```text
orama maint global
```

Subcommands: `edit`, `refresh`, `stage-oramad`, `tor`, `txgate`, `validator`

## orama maint global edit

Change this node's public storage size or exit role (run as root)

```text
orama maint global edit [flags]
```

```text
Change a setting of the global layer installed on this node. 'orama edit' runs it over
SSH with the node's own CLI; it declares the same change on the chain from your
machine.

--storage-gb N sizes the public Kubo for N GB of declared capacity (its StorageMax
becomes N plus 10%) and restarts it; the repo, its identity and token stay. The
capacity itself is declared on the chain with MsgDeclareCapacity, which 'orama edit'
sends first: the chain refuses a capacity the role bond does not back, and one below
the bytes already reserved by deals.

--exit=true|false switches the Tor relay between a plain relay and an exit by
rewriting only the exit section of its torrc, then restarts the relay. An exit
needs a network whose file allows exits, and refuses the destinations listed in
the exit reject list.
```

| Flag | Default | Description |
|---|---|---|
| `--exit` | `false` | Make the Tor relay an exit (true) or a plain relay (false) |
| `--storage-gb` | `0` | Declared public storage capacity, in GB |


## orama maint global refresh

Bring the installed global services up to the staged release (run as root)

```text
orama maint global refresh [flags]
```

```text
Put the staged release's global binaries in place of the installed ones and restart
the services that run them. 'orama upgrade' runs it on every node that has the
global layer, after the node's cluster services are upgraded, one node at a time.

Each binary the installed services need (orama, orama-global, ipfs) is read from
--staged-dir, held to the release manifest, and replaced atomically when its bytes
differ. A service is restarted only if the binary its own process runs was
replaced: the provider, archiver, indexer, repair delegate and reporter run
orama-global, the public Kubo runs ipfs, the onion service's gate runs orama. The
chain and the Tor relay or directory authority are never restarted by a refresh
(the chain runs oramad from the cosmovisor layout; Tor runs the distro's tor).

oramad changes only through a governed upgrade. When the release carries an oramad
other than the one cosmovisor runs, the refresh reads the chain's scheduled
upgrade plan: with one, it stages the release's oramad and shielded verifier for
that plan, and cosmovisor switches to them at the plan's height; without one, it
keeps the running oramad and says so. The plan's info names the sha256 of this
platform's oramad and of its shielded verifier; the release's two files have to be
exactly those, or nothing is staged (and a plan that names none is not staged for).

The chain indexer refuses an index written in an older layout than the new
orama-global's. So when the indexer is installed, the refresh stops it, runs
'orama-global indexer migrate' as the indexer's own account, not as root (which
renames an old index to <state>/indexer/index.v<layout>-<time> and deletes
nothing, and refuses an index path that is a symlink or not a directory), and starts it again;
the indexer then builds a new index from the chain. An index of the current layout
is left as it is. An indexer that is already crash-looping on an old index is put
right the same way, and one stopped on purpose stays stopped.

A service is restarted when its running process executes a file that has been
replaced (/proc/<pid>/exe is "(deleted)"), not because of what this run replaced:
a refresh that was interrupted is finished by running it again. A service that is
not running is not started by it, except an indexer that was failing on an old index.
```

| Flag | Default | Description |
|---|---|---|
| `--manifest` | `/opt/orama/manifest.json` | The staged release's manifest.json |
| `--staged-dir` | `/opt/orama/bin` | The staged release's bin/ directory |


## orama maint global stage-oramad

Place a TUF-verified oramad in the cosmovisor layout

```text
orama maint global stage-oramad [flags]
```

```text
Place an oramad binary where cosmovisor runs it, after it verifies against
the release root adopted at /etc/orama/release-root.json.

--upgrade <name> stages <home>/cosmovisor/upgrades/<name>/bin/oramad for the
upgrade plan <name>; cosmovisor switches to it at the plan's height. --genesis
stages <home>/cosmovisor/genesis/bin/oramad and points current at genesis if
current does not exist yet. A binary already there is refused.

For an upgrade, oramad and the verifier are also held to the checksums the plan
names: the sha256 of this platform's oramad and shielded verifier, which the
SoftwareUpgrade proposal carried in its info and x/upgrade holds as the plan's
info. The plan is read from the chain's REST API and has to be the one named
--upgrade; --plan-info <file> reads the info JSON from a file instead, for a
machine whose chain cannot be asked. The sha256 is taken of the staged copy, in
the same pass that verifies it, so a file changed after the command started is
the file that is checked. A file the plan does not name, or a plan that names no
checksums, is refused and nothing is staged.

The binary is copied into a root-only staging directory and verified there,
through the descriptor that wrote it, as --release-target in the TUF metadata
in --release-metadata (threshold, timestamp expiry, snapshot rollback, length
and hashes); only then is it linked into place. Every directory on the way is
opened without following symlinks and must be root's; a symlink or a
directory another account owns or may write is refused. Nothing stages
automatically: a validator's operator runs this for every chain upgrade.

The chain unit 'orama global install' writes runs cosmovisor, which reads this
layout; install places the first oramad here as the genesis binary.
```

| Flag | Default | Description |
|---|---|---|
| `--binary` | — | The oramad binary to stage [required] |
| `--genesis` | `false` | Stage the genesis binary instead of an upgrade |
| `--home` | `/var/lib/orama-global/chain` | cosmovisor DAEMON_HOME |
| `--plan-info` | — | File with the upgrade plan's info JSON, instead of asking the chain (with --upgrade) |
| `--release-metadata` | — | Directory holding timestamp.json, snapshot.json and targets.json [required] |
| `--release-target` | — | Name the binary has in the release targets metadata [required] |
| `--upgrade` | — | Upgrade plan name to stage for |
| `--verifier-target` | — | Name the verifier has in the release targets metadata [required] |
| `--verifier` | — | The release's orama-orchard-verifier, staged beside oramad [required] |


## orama maint global tor

The Orama Tor network: authority key ceremony, vote archive, relay monitor, onion list

```text
orama maint global tor
```

```text
The Orama Tor network is a separate anonymity network built from unmodified
upstream Tor code, run by Orama's own directory authorities (orama.network/docs/operator/tor-network).
The roles are installed by 'orama global install --services dirauth|relay|relay,exit|onion'.
```

Subcommands: `archive`, `authority-keys`, `ceremony`, `monitor`, `onions`

## orama maint global tor archive

Archive this directory authority's consensus and votes (run by orama-global-tor-archive.timer)

```text
orama maint global tor archive [flags]
```

```text
Copy the consensus the authority holds, the votes that made it and, with
--bandwidth-file, the bandwidth file it voted with, from --data-dir into
--archive-dir/<valid-after>/ with a MANIFEST.json of SHA-256 digests. Every vote,
consensus and bandwidth file of the network is then recomputable by anyone who
holds the archive. Running it again changes nothing for a period that is
archived; a consensus is replaced only by the same consensus with more
signatures.
Before the authority's first consensus (up to one voting interval after the
authorities start) there is nothing to archive, and the run succeeds saying so.

With --export-votes-dir it also copies the authority's own vote of that period
to <dir>/<valid-after>.vote, the files the bandwidth reporter reads. The
directory must exist (the install makes it, shared read-only with the
reporter's group); nothing else of the data directory is copied there.
```

| Flag | Default | Description |
|---|---|---|
| `--archive-dir` | — | Where the archive is written [required] |
| `--bandwidth-file` | — | The bandwidth file the authority votes with |
| `--data-dir` | — | The authority's tor DataDirectory [required] |
| `--export-votes-dir` | — | Also copy the authority's own vote to &lt;dir>/&lt;valid-after>.vote for the bandwidth reporter (the directory must exist) |


## orama maint global tor authority-keys

Make this machine's directory-authority keys, or read back the ones it has (run as root)

```text
orama maint global tor authority-keys [flags]
```

```text
Make the keys of ONE directory authority on this machine, which will run it: the
relay identity (tor --list-fingerprint) and the authority identity key, signing key and
12-month certificate (tor-gencert), the same programs and key handling as
'orama maint global tor ceremony'. The tor package of the Tor Project's repository
is installed first if it is missing.

Below --dir (default /var/lib/orama-tor-authority, root only) it writes
  offline/<nickname>/authority_identity_key   the identity key, encrypted, mode 0400
  offline/<nickname>/passphrase               its passphrase, generated here, mode 0400
  deploy/<nickname>/keys/                     the signing key, the certificate and the
                                              relay identity: what 'orama global
                                              install --services dirauth' reads, with
                                              --tor-authority-keys deploy/<nickname>
Nothing secret is printed or leaves the machine. The identity key is never in the
bundle and never in a tor DataDirectory.

It prints the public identity as one line of JSON: the nickname, address, ports, v3
identity, relay fingerprint, ed25519 identity, the certificate's expiry and whether
this run made the keys. Running it again reads the keys back and checks them; keys
are never made over keys, and a directory that holds part of a set is an error.

This is how 'orama setup --create-network' makes the authorities of a new network's own Tor
network, on a test network. The passphrase beside the key is that arrangement's trade-off:
a production network runs the ceremony on an offline machine instead.
```

| Flag | Default | Description |
|---|---|---|
| `--address` | — | The authority's public IPv4 address [required] |
| `--dir` | `/var/lib/orama-tor-authority` | The keys home; only root may enter it |
| `--nickname` | — | The authority's nickname, 1 to 19 letters and digits [required] |


## orama maint global tor ceremony

Generate the directory authorities' keys and the network file (run on an offline machine)

```text
orama maint global tor ceremony [flags]
```

```text
Generate the keys of a set of directory authorities with the upstream tor and
tor-gencert, which must be installed on this machine, and write the network file.

Run it on an air-gapped machine. For each --authority NICKNAME=IPv4 it writes,
below --out:
  offline/<nickname>/authority_identity_key   the identity key, encrypted with the
                                              passphrase; it signs certificates and
                                              nothing else. Move it to offline media
                                              (encrypted, in two places, or an HSM)
                                              and delete it from this machine.
  deploy/<nickname>/keys/                     what the authority host installs: the
                                              signing key and its 12-month
                                              certificate, and the relay identity.
and once tor-network.json (public: the authority list every relay and client
needs, to stage beside the release) and TRANSCRIPT.txt (the fingerprints to read
aloud and sign). --out must not exist or be empty: a ceremony never writes over
keys.

--passphrase-file holds the identity-key passphrase (at least 16 characters, one
line, mode 0600). Authority ports are fixed at 31020 (ORPort) and 31021 (DirPort),
the ports the global firewall opens. --bootstrap (default) writes the network file
with bootstrap true, which a new network needs for its first consensus; set it to
false in the file once the first consensus is signed. --allow-exit puts allow_exit
in the file: only a network whose owner runs exits sets it.
```

| Flag | Default | Description |
|---|---|---|
| `--allow-exit` | `false` | Let nodes of this network be installed as exits |
| `--allow-shared-subnets` | `false` | Let circuits use two relays of one /16 (a network with fewer /16 networks than hops needs it) |
| `--authority` | — | A directory authority, NICKNAME=IPv4 (repeatable, at least three) [required] |
| `--bootstrap` | `true` | Write bootstrap true: a new network assumes reachability until its first consensus |
| `--dist-delay-seconds` | `300` | Seconds authorities wait for signatures |
| `--hsdir-min-uptime-hours` | `0` | Hours of uptime before a relay gets the HSDir flag (0 = Tor's default of 96; a new network sets a few) |
| `--name` | — | Network name, lowercase letters, digits and dashes [required] |
| `--out` | — | Output directory; must not exist or be empty [required] |
| `--passphrase-file` | — | File holding the identity-key passphrase, mode 0600 [required] |
| `--tor-gencert` | — | The tor-gencert binary (default: tor-gencert on PATH) |
| `--tor` | — | The tor binary (default: tor on PATH) |
| `--vote-delay-seconds` | `300` | Seconds authorities wait for votes |
| `--voting-interval-minutes` | `60` | Minutes between consensuses; must divide 24 hours |


## orama maint global tor monitor

Write this relay's or directory authority's monitor.json for the node report (run by orama-global-tor-monitor.timer)

```text
orama maint global tor monitor [flags]
```

```text
Write <home>/monitor.json with whether the consensus the relay or directory authority
holds lists it: {"in_consensus": true|false}. 'orama status node' shows it on the Global
line and the node report raises a warning when the node is not listed. The field is left
out (the file is "{}") while the node has no consensus yet or the one it holds has
expired, so an unknown state is never reported as a no. It reads only the role's own
DataDirectory and writes only monitor.json there.
```

| Flag | Default | Description |
|---|---|---|
| `--home` | — | The relay's or directory authority's tor DataDirectory [required] |


## orama maint global tor onions

The validator onion services the network file lists

```text
orama maint global tor onions
```

Subcommands: `add`

## orama maint global tor onions add

Add validator onion services to the network file clients join with

```text
orama maint global tor onions add ADDR.onion[:PORT]... [flags]
```

```text
A validator's onion address exists only once its onion role has started, which
is after the ceremony wrote tor-network.json. Read it on the validator with
'orama global tor info' (as root), then add it to the network file here and
republish the file to clients: orama maint vpn, --onion-network and the relay reporter
all read validator_onions from it.

Each address is checked as a v3 onion address with an optional port (default 80,
the port the onion service answers on). An address already listed is not listed
twice. The file is replaced atomically and must already be a valid network file;
nothing is written when an address is refused. Relays and authorities ignore
validator_onions, so adding an onion never needs a node restart.
```

| Flag | Default | Description |
|---|---|---|
| `--network-file` | — | The tor-network.json to update [required] |


## orama maint global txgate

Serve the validator's transaction gate on loopback (run by orama-global-txgate.service)

```text
orama maint global txgate [flags]
```

```text
Serve the three calls a wallet needs to submit one transaction (read the signer's
account, broadcast, look the transaction up) from the chain's REST API, and
nothing else. The validator's onion service forwards to this listener, so the
rest of the chain API is not reachable over the onion. Requests arrive from the
local Tor process, so the limits are on the whole gate and no request is logged.
--listen must be a loopback address.
```

| Flag | Default | Description |
|---|---|---|
| `--burst` | `40` | Requests that may arrive at once |
| `--listen` | — | Loopback host:port to listen on [required] |
| `--max-in-flight` | `16` | Most requests asked of the chain API at once |
| `--rate` | `20` | Requests per second the gate forwards, in total |
| `--upstream` | — | The chain REST API, http://host:port [required] |


## orama maint global validator

Back up, move and manage this node's validator key

```text
orama maint global validator
```

Subcommands: `check-sign-floor`, `edit`, `export-key`, `migrate`, `reseal`, `unjail`

## orama maint global validator check-sign-floor

Fail when the chain must not start: key moved away or state behind its floor

```text
orama maint global validator check-sign-floor
```

```text
The double-sign guard. orama-global-chain.service runs it as root before every
start (ExecStartPre), from /usr/lib/orama-global/bin, where 'orama global
install' puts this CLI. With no sign floor recorded it passes. With one, it
fails when priv_validator_key.json is missing (the key moved to another host)
or priv_validator_state.json is behind the floor.
```


## orama maint global validator edit

Build or send MsgEditValidator (description, commission)

```text
orama maint global validator edit [flags]
```

```text
Build x/staking MsgEditValidator for the operator's validator. Only the flags
given change; every other description field is sent as [do-not-modify]. An empty
value clears that field. --commission-rate is a decimal from 0 to 1; x/staking
allows one commission change per 24 hours, within the validator's
max-change-rate. Without --node the command prints the sign document.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--commission-rate` | — | New commission rate, 0 to 1 |
| `--details` | — | New details |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--identity` | — | New identity (for example a keybase id) |
| `--moniker` | — | New moniker |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Validator operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--security-contact` | — | New security contact |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--website` | — | New website |


## orama maint global validator export-key

Write priv_validator_key.json sealed to the operator's public key (run as root)

```text
orama maint global validator export-key [flags]
```

```text
Seal priv_validator_key.json to --recipient, an X25519 public key (64 hex
characters), with the same ORBK seal as a namespace backup, and write it to --to.
The node never holds the private half, so it cannot open the file. --to must
not exist.

To restore the key on a new host, run 'orama maint global validator migrate prepare'
there, then 'orama maint global validator reseal' on the machine holding the private
key, then 'orama maint global validator migrate import' on the new host. Restore only
when the old host is gone: two hosts signing with one key is a double sign.
```

| Flag | Default | Description |
|---|---|---|
| `--recipient` | — | Operator X25519 public key, hex [required] |
| `--to` | — | File to write; must not exist [required] |


## orama maint global validator migrate

Move the validator key to another host without a double sign

```text
orama maint global validator migrate
```

```text
Move priv_validator_key.json and priv_validator_state.json from this host to
another, in three steps, each run as root:

  1. on the new host:  orama maint global validator migrate prepare
  2. on the old host:  orama maint global validator migrate export --recipient <key> --to <file>
  3. copy <file> to the new host, then:
                       orama maint global validator migrate import --from <file>

export stops and disables the old host's chain (and stops the services that
need it) before it reads anything. It seals the key and state in memory, records
the state as the old host's sign floor, keeps a copy of the state, moves the key
out of the chain home, and then writes the bundle. The chain unit checks the
floor before every start, so the old host's chain no longer starts: the floor is
recorded and the key is gone. import refuses while the new host's chain runs,
records the old host's last sign state as the new host's floor, writes the
state, and installs the key last; the chain unit then refuses to start from a
state behind the floor. cancel removes a prepared migration key.

A bundle from 'orama maint global validator reseal' (a restored backup) has no sign
state. Its import needs --old-host-destroyed and --floor-height with the
network's latest committed height H. The floor and the state become height H+1,
round 0, before any step: the restored key signs nothing at or below H, in any
round. It can sign at H+1, so a vote the lost host cast at H+1 is excluded only
if that host stopped before H+1 began.
```

Subcommands: `cancel`, `export`, `import`, `prepare`

## orama maint global validator migrate cancel

Remove this host's prepared migration key (run on the new host)

```text
orama maint global validator migrate cancel
```


## orama maint global validator migrate export

Stop the chain and seal the key and its sign state (run on the old host)

```text
orama maint global validator migrate export [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--recipient` | — | The new host's migration key, from prepare [required] |
| `--to` | — | Bundle file to write; must not exist [required] |


## orama maint global validator migrate import

Install a migrated key and record its sign floor (run on the new host)

```text
orama maint global validator migrate import [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--floor-height` | `0` | For a reseal bundle: the network's latest committed height; the key signs only above it |
| `--from` | — | Bundle file from export or reseal [required] |
| `--old-host-destroyed` | `false` | For a reseal bundle: confirm the old host can never start again |


## orama maint global validator migrate prepare

Print this host's migration key (run on the new host)

```text
orama maint global validator migrate prepare
```


## orama maint global validator reseal

Turn a key backup into a migration bundle for a new host

```text
orama maint global validator reseal [flags]
```

```text
Open a key backup from 'orama maint global validator export-key' with the operator's
X25519 private key (--identity-file, hex, mode 0600) and seal the key to the new
host's migration key (--recipient, printed by 'orama maint global validator migrate
prepare'). Run it on the machine that holds the private key, not on a node. The
bundle carries no sign state: nobody knows what a lost host last signed. Its
import therefore needs --old-host-destroyed and --floor-height <the network's
latest committed height>; the key then signs only above that height.
```

| Flag | Default | Description |
|---|---|---|
| `--from` | — | Key backup from export-key [required] |
| `--identity-file` | — | File holding the operator X25519 private key, hex, mode 0600 [required] |
| `--recipient` | — | The new host's migration key, hex [required] |
| `--to` | — | Bundle file to write; must not exist [required] |


## orama maint global validator unjail

Build or send MsgUnjail for the operator's validator

```text
orama maint global validator unjail [flags]
```

```text
Build x/slashing MsgUnjail for the validator whose operator account is
--operator (the same bytes as its oramavaloper address), signed by that account.

x/slashing refuses it while the jail period runs, when the validator has no
self-delegation or less than its minimum, and for a tombstoned validator, which
can never unjail. Without --node the command prints the sign document and does
not submit it; with --node the RootWallet agent signs and it is broadcast.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--operator` | — | Validator operator account (orama1...) [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |


## orama maint inspect

Inspect cluster health via SSH

```text
orama maint inspect [flags]
```

```text
SSH into cluster nodes and run health checks.
Supports AI-powered failure analysis and result export.

The report is written to stdout and progress to stderr, so --format json is one
JSON document. A bad flag value (an unknown --subsystem or --format, a timeout
that is not positive) is refused as usage before any node is contacted.

Every node's host key must already be pinned in ~/.orama/known_hosts, where
'orama node setup' writes it. A node with no pinned key refuses the run, never
trusted on first use, and a key that differs from the pinned one is refused.
```

| Flag | Default | Description |
|---|---|---|
| `--ai` | `false` | Enable AI analysis of failures |
| `--api-key` | — | OpenRouter API key (or OPENROUTER_API_KEY env) |
| `--config` | — | Read nodes from this file instead of resolving them |
| `--env` | — | Environment to inspect (devnet, testnet) |
| `--format` | `table` | Output format (table, json) |
| `--model` | `moonshotai/kimi-k2.5` | OpenRouter model for AI analysis |
| `--output` | — | Save results to directory as markdown (e.g., ./results) |
| `--subsystem` | `all` | Subsystem to inspect (rqlite,olric,ipfs,dns,wg,system,network,tor,global,all) |
| `--timeout` | `30s` | SSH command timeout |
| `--verbose` | `false` | Verbose output |


## orama maint invite

Mint an invite for a new node

```text
orama maint invite [flags]
```

```text
Create a single-use invite that lets a new node join the cluster.

The invite names one node of the cluster: its public address, the domain to
present to it, and the fingerprint of the TLS certificate it serves. The
joining node connects to exactly that node and pins exactly that certificate,
rather than resolving the cluster's domain — which reaches any nameserver,
each with a certificate of its own — or trusting whatever certificate it is
first shown. There is nothing else to copy across.

The node is the lowest address the environment's domain resolves to, or the
one named with --node. The token is minted through that node, on the same
connection whose certificate is pinned.

This is the same token as 'orama node invite', which does the same thing from
an existing node instead of from here.
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Environment to invite into (default: active) |
| `--expiry` | `1h0m0s` | How long the invite stays usable (the gateway caps it at 1h) |
| `--node` | — | Public IP of the node the invite names (default: the lowest address the environment's domain resolves to) |


## orama maint network

Maintain the published networks

```text
orama maint network
```

Subcommands: `announce`, `cluster-only`, `publish`

## orama maint network announce

Write networks/&lt;name>/ for a network that does not exist yet

```text
orama maint network announce [flags]
```

```text
Write networks/<name>/ (release-root.json and, last, manifest.json) for a
network whose chain has not been created: its name, the chain id it will have, the
channel, the release repository and the release root its releases are verified
against, whether it has a faucet, and its seeds. The manifest pins no genesis, so
the network is listed but cannot be joined: 'orama setup --network <name>' says it
has not been created yet.

The person who creates the network then runs 'orama setup --create-network <name>'
with no --release-root: the chain id, the release repository, the channel and the
release root come from the announcement (a flag overrides). When the chain exists
the creation publishes the full manifest, with the genesis, over the announcement.

An announcement can be written again until the network is created. A network that
is already created is refused: a reset needs a new chain id from the creation.

Afterwards run 'make -C core sync-networks' so the binary embeds the manifest, and
commit networks/ and core/pkg/netregistry/embedded/ together.
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id the network will have [required] |
| `--channel` | — | Release channel: nightly, main or dev/&lt;branch> [required] |
| `--dir` | `networks` | The repository's networks directory |
| `--faucet` | `false` | The network funds new operators from a faucet |
| `--min-version` | `0.3.11` | Oldest orama version that may join, X.Y.Z |
| `--name` | — | Network name, for example testnet [required] |
| `--release-repo` | — | https base URL of the release repository [required] |
| `--release-root` | — | The release-root.json file its releases are verified against [required] |
| `--seed` | — | A seed DNS name (repeatable; default: the creator's ns&lt;N>.&lt;name>.orama.network) |


## orama maint network cluster-only

Write networks/&lt;name>/ for a network of clusters that runs no chain

```text
orama maint network cluster-only [flags]
```

```text
Write networks/<name>/ (release-root.json and, last, manifest.json) for a
cluster-only network: one that runs no chain, only clusters whose nodes follow a
release channel. The manifest names the release repository, the channel, the
oldest version that may join and the release root its releases are verified
against. Its chain_id and genesis_sha256 are empty and it has no seeds, faucet or
Tor network file.

A cluster on such a network is upgraded from its channel with 'orama upgrade'
once its environment names the network ('orama network add <name> <gateway URL>
--network <name>'). 'orama setup' installs only cluster nodes on it (--cluster-only),
and a command that signs for a chain is refused there: it has none.

It can be written again to change the channel, the repository, the minimum version
or the root. A network whose manifest names a chain, announced or created, is
refused. A cluster-only network that is to get a chain is announced
('orama maint network announce') or created over it.

Afterwards run 'make -C core sync-networks' so the binary embeds the manifest, and
commit networks/ and core/pkg/netregistry/embedded/ together.
```

| Flag | Default | Description |
|---|---|---|
| `--channel` | — | Release channel: nightly, main or dev/&lt;branch> [required] |
| `--dir` | `networks` | The repository's networks directory |
| `--min-version` | `0.3.11` | Oldest orama version that may join, X.Y.Z |
| `--name` | — | Network name, for example devnet [required] |
| `--release-repo` | — | https base URL of the release repository [required] |
| `--release-root` | — | The release-root.json file its releases are verified against [required] |


## orama maint network publish

Write networks/&lt;name>/ for a chain that was just deployed

```text
orama maint network publish [flags]
```

```text
Write networks/<name>/ (genesis.json, release-root.json, tor-network.json when the
network has one and, last, manifest.json) from the genesis a deploy built, for
the chain id it was built under. The manifest carries the SHA-256 of the genesis,
of the release root and of the Tor network file, so what a joining operator
fetches can be checked. --tor-network pins the network's Tor network file
(tor-network.json, the private Orama Tor network its relays join), which
'orama setup' then gives to every relay it installs; the Tor network is a
different thing from the chain, so a reset of the chain keeps the file.

An unset field keeps its value from the manifest already published; the first
publish of a network sets --seeds, --channel, --min-version, --release-repo and
--release-root. A chain id that is already published keeps its genesis: a reset
of the network needs a new chain id (orama-testnet-5 becomes orama-testnet-6),
and publishing the old id with another genesis is refused.

Afterwards run 'make -C core sync-networks' so the binary embeds the new manifest,
and commit networks/ and core/pkg/netregistry/embedded/ together.
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id the genesis was built under [required] |
| `--channel` | — | Release channel: nightly, main or dev/&lt;branch> (default: the published one) |
| `--dir` | `networks` | The repository's networks directory |
| `--faucet` | `false` | Whether the network funds new operators from a faucet (default: the published one) |
| `--genesis` | — | The genesis.json file [required] |
| `--min-version` | — | Oldest orama version that may join, X.Y.Z (default: the published one) |
| `--name` | — | Network name, for example testnet [required] |
| `--release-repo` | — | https base URL of the release repository (default: the published one) |
| `--release-root` | — | The release-root.json file (default: the published one) |
| `--seeds` | — | Seed DNS names, comma-separated (default: the published ones) |
| `--tor-network` | — | The Tor network's tor-network.json (default: the published one, if any) |


## orama maint node

Install, stage, recover and migrate nodes

```text
orama maint node
```

```text
Node commands for maintainers and for the installer.

Install and stage a release on this machine, simulate the auto-update decision,
recover a cluster that lost its raft quorum, migrate older state, apply gateway
schema migrations, and enroll or unlock an OramaOS node.
```

Subcommands: `autoupdate`, `enroll`, `install`, `migrate-conf`, `migrate-raft-id`, `recover-raft`, `schema`, `stage-archive`, `unlock`

## orama maint node autoupdate

Decide whether a newer release should be installed

```text
orama maint node autoupdate [flags]
```

```text
Report what this cluster should do with a candidate release.

This answers the question for the values you give it and changes nothing. The
agent that asks it of the cluster's real state, and acts, is 'orama node
autoupdate run'.

The default mode is notify: a newer verified release is reported and not
installed. auto means the node may install, and only when the cluster is
healthy, the release is newer, and the maintenance window is open.

A release that fails TUF verification, including a rolled-back snapshot or
an expired timestamp, is refused. So is a downgrade and a release a previous
health-gate failure marked bad.

A validator (--role validator) is never auto: on auto the decision is skip, with
the reason, and the command exits 0; chain upgrades are staged explicitly with
'orama maint global stage-oramad'.
```

| Flag | Default | Description |
|---|---|---|
| `--bad` | `false` | candidate was marked bad by a failed health gate |
| `--candidate` | — | version being considered |
| `--channel` | `stable` | release channel |
| `--current` | — | version installed now |
| `--degraded` | `false` | cluster is already degraded |
| `--healthy-voters` | `2` | raft voters that are up |
| `--mode` | `notify` | off, notify, or auto |
| `--role` | `cluster` | this node's role: cluster or validator |
| `--verify` | — | simulated TUF failure: rollback, freeze, threshold, or hash |
| `--voters` | `3` | raft voters |
| `--window` | — | maintenance window as start-end hours, for example 1-5 |

Subcommands: `run`

## orama maint node autoupdate run

Look for a newer release on the cluster's channel and act on it (requires sudo)

```text
orama maint node autoupdate run
```

```text
Run this node's auto-update agent once. orama-autoupdate.timer runs it every
15 minutes on every node; running it by hand does the same thing.

The agent reads the cluster's policy (orama maint cluster settings show): auto-update
off, notify or auto; the channel; the maintenance window; the release repository.
It does nothing unless the cluster stored a release repository and this node
adopted a release root (orama node trust add-root).

It fetches the channel's metadata and verifies it against the adopted root:
every role at its threshold, an unexpired timestamp, a snapshot no older than the
newest this node has accepted, and the channel's own keys for the channel's own
paths. What does not verify is refused, reported in 'orama status', and never
installed.

With notify (the default) a newer release is reported in 'orama status' and
nothing is installed. With auto the node installs it only when

  - the cluster is not degraded and a majority of the raft voters are up;
  - the hour is inside the maintenance window, if there is one;
  - no node has failed the release (a failure anywhere marks the release bad for
    every node, until a newer release supersedes it);
  - it is this node's turn in the rollout plan: followers first, the leader
    last, nameservers spaced, one node at a time;
  - it holds the cluster-wide rollout lock.

The install is 'orama maint node stage-archive --release-only', keeping the release it
replaces, then 'orama node upgrade --restart', then the health gate. If the
upgrade or the gate fails, the previous release is put back and the node is
upgraded onto it again; the release is then marked bad for the cluster. A
validator (a machine that runs the chain) is never installed automatically.

If a run is killed in the middle of an install, the next run finishes it first
(the intent is in /var/lib/orama-autoupdate/install-intent.json), whatever the
policy now says. One agent runs at a time on a machine.
```


## orama maint node enroll

Enroll an OramaOS node into the cluster

```text
orama maint node enroll [flags]
```

```text
Enroll a freshly booted OramaOS node into the cluster.

The OramaOS node prints a registration code on its console. Provide that code
along with an invite token. The Gateway pushes cluster configuration
(WireGuard, secrets, peer list) to the node, sealed under the code.

The code is not served over the network. A GET on port 9999 used to return it.

Usage:
  orama maint node enroll --node-ip <ip> --code <code> --token <invite-token> --gateway <url>

--gateway must be an https:// URL: the invite token is a credential and is never
sent in the clear.

The node must be reachable over the public internet on port 9999 (enrollment only).
After enrollment, port 9999 is permanently closed and all communication goes over WireGuard.
```

| Flag | Default | Description |
|---|---|---|
| `--code` | — | Registration code from the node's console (required) |
| `--env` | `production` | Environment name |
| `--gateway` | — | Gateway URL (required, e.g. https://gateway.example.com) |
| `--node-ip` | — | Public IP of the OramaOS node (required) |
| `--token` | — | Invite token for cluster joining (required) |


## orama maint node install

Install production node (requires sudo)

```text
orama maint node install [flags]
```

```text
Install and configure an Orama production node on this machine.
For the first node, this creates a new cluster. For subsequent nodes,
use --join and --token to join an existing cluster.

Run it on the node itself with sudo, or from your own machine with --remote to
drive the install over SSH against --vps-ip. Which of the two happened used to
be decided by whether you had used sudo.

The build archive must be extracted at /opt/orama and signed by a wallet in the
node's trust anchor, /etc/orama/archive-signers. A genesis install creates the
anchor from --operator-wallet (required) before it verifies the archive; a
joining node takes it from the cluster in the join response. With --remote,
--archive names the build: it is verified on this machine against
--operator-wallet before it is uploaded.
```

| Flag | Default | Description |
|---|---|---|
| `--acme-ca` | — | ACME directory for TLS certificates: letsencrypt (production, the default), letsencrypt-staging (clusters rebuilt many times a week) or an https URL |
| `--archive` | — | With --remote: the build archive to upload, verified here against --operator-wallet first |
| `--base-domain` | — | Base domain for deployment routing (e.g., example.com) |
| `--ca-fingerprint` | — | SHA-256 fingerprint of the gateway's TLS cert; the invite carries this, so it is only needed to override it |
| `--domain` | — | Domain for HTTPS (auto-generated for non-nameserver nodes if omitted) |
| `--dry-run` | `false` | Show what would be done without making changes |
| `--environment` | — | Environment name (devnet, testnet, etc.) |
| `--expect-archive-signers` | — | When joining: the archive signers the cluster must send (comma-separated); the archive is verified against them before the join |
| `--force` | `false` | Force reconfiguration even if already installed |
| `--host-key` | — | Expected SSH host-key fingerprint (SHA256:...) for --remote; omit to confirm it interactively |
| `--ipfs-addrs` | — | Comma-separated multiaddrs of existing IPFS node |
| `--ipfs-cluster-addrs` | — | Comma-separated multiaddrs of existing IPFS Cluster node |
| `--ipfs-cluster-peer` | — | Peer ID of existing IPFS Cluster node |
| `--ipfs-peer` | — | Peer ID of existing IPFS node to peer with |
| `--join-sni` | — | Server name to present to --join; the invite carries it, so it is only needed to override it |
| `--join` | — | Gateway to join; the invite carries this, so it is only needed to override it |
| `--nameserver` | `false` | Make this node a nameserver (runs CoreDNS + Caddy) |
| `--node-names-zone` | — | Zone this node's cluster publishes node identification names under, strictly below --base-domain (for example nodes.&lt;base-domain>); omit to keep the zone node.yaml carries |
| `--operator-wallet` | — | Operator wallet address |
| `--peers` | — | Comma-separated list of bootstrap peer multiaddrs |
| `--release-repo` | — | When creating a cluster: the https release repository to store as its release_repo setting, so the auto-update agent has one to look in; a setting the cluster already has is kept |
| `--remote` | `false` | Install the machine at --vps-ip over SSH, instead of this machine |
| `--skip-checks` | `false` | Skip minimum resource checks (disk, RAM, CPU) |
| `--skip-firewall` | `false` | Skip UFW firewall setup (for users who manage their own firewall) |
| `--ssh-user` | — | SSH user for remote management |
| `--token` | — | Invite from 'orama maint invite'; it carries the gateway to join and the certificate to pin |
| `--update-channel` | — | When creating a cluster: the release channel (nightly, main, dev/&lt;branch>) to store as its update_channel setting; a setting the cluster already has is kept |
| `--vps-ip` | — | Public IP of this VPS (required) |


## orama maint node migrate-conf

Register nodes.conf nodes with your wallet

```text
orama maint node migrate-conf [flags]
```

```text
One-time migration: reads nodes from nodes.conf for an environment
and registers each with your wallet via the gateway API. After migration,
these nodes will appear in 'orama nodes' output.

Requires: orama auth login (for API authentication)
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Environment to migrate (default: active) |


## orama maint node migrate-raft-id

Move nodes to stable, peer-id-based raft identities (one-time)

```text
orama maint node migrate-raft-id [flags]
```

```text
Give each node a raft identity that survives an address change.

RQLite defaults a node's raft id to its raft advertise address, so identity has
been a function of routing: give the same machine a new overlay address — a
replacement, a WireGuard re-provision, a 10.0.0.x reassignment — and it mints a
new raft id, joins as a SECOND member, and the old entry stays in the
configuration as a voter nothing can reach. Two such events on a five-voter
cluster leave quorum at 3-of-7 with five live voters; one more failure freezes
the registry.

RQLite cannot rename a member in place, so this is a deliberate migration rather
than something an upgrade does silently. Nodes are migrated ONE AT A TIME. For
each: the quorum arithmetic is checked, the old id is removed from the raft
configuration and tombstoned, the node's local raft state is discarded, and it
rejoins under its libp2p peer id and replicates back from the leader. The next
node is not touched until the previous one is back in the configuration.

Safe to re-run: nodes already on a stable id are skipped, so an interrupted run
continues where it stopped.

Examples:
  orama maint node migrate-raft-id --env testnet --dry-run
  orama maint node migrate-raft-id --env testnet
  orama maint node migrate-raft-id --env testnet --node 1.2.3.4
```

| Flag | Default | Description |
|---|---|---|
| `--dry-run` | `false` | Report what would change and exit |
| `--env` | — | Target environment [required] |
| `--force` | `false` | Skip the confirmation prompt |
| `--node` | — | Migrate only this public IP. Default: every node that needs it |


## orama maint node recover-raft

Recover RQLite cluster from split-brain

```text
orama maint node recover-raft [flags]
```

```text
Recover the RQLite Raft cluster from split-brain failure.

One node's data is kept. Every other node's raft log and database are DELETED
and rebuilt from it. Nothing is backed up: there is no copy to restore from
afterwards, and the deleted nodes' data is gone. Take a backup yourself first
if the surviving node might not be the right one.

What happens:
  1. Stop orama-node on every node
  2. Reset the kept node to a single-member cluster, preserving its data,
     raft log and raft term
  3. Start it and confirm it comes back as Leader with its data intact
  4. Delete raft.db, raft/, db.sqlite (+shm/wal) and wsnapshots (rsnapshots) on every other
     node, and record the kept node as the member each one re-joins
     (data/cluster-membership.json)
  5. Start them one at a time; each pulls a full snapshot from the kept node
  6. Verify cluster health

Which node is kept decides which copy of the data survives. Without --leader
the command reads every node's applied index, keeps the furthest ahead, and
prints what each one reported before asking you to confirm. --leader overrides
that.

Use --leader-raft-addr when quorum is already lost and rqlite is not answering
anywhere, so the leader's raft address cannot be read from the cluster.

This is a DESTRUCTIVE operation. Use --force to skip confirmation.

Examples:
  orama maint node recover-raft --env testnet
  orama maint node recover-raft --env testnet --leader 1.2.3.4
  orama maint node recover-raft --env devnet --leader-raft-addr 10.0.0.1:10101 --force
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Target environment (devnet, testnet) [required] |
| `--force` | `false` | Skip confirmation (DESTRUCTIVE) |
| `--leader-raft-addr` | — | Explicit leader raft address host:port (e.g. 10.0.0.1:10101). Use when quorum is already lost so the leader can't be auto-resolved; bypasses the live-Leader check. |
| `--leader` | — | IP of the node whose data to keep; default is the node with the highest applied index |


## orama maint node schema

Inspect and apply gateway schema migrations against the local RQLite

```text
orama maint node schema [flags]
```

```text
Schema lifecycle commands.

The gateway binary embeds a set of SQL migrations. Each migration is numbered;
the highest number is the schema version the binary requires. After deploying
a new gateway binary, run 'orama maint node schema apply' on every namespace's RQLite
to bring the schema up to date — otherwise function deploys fail at runtime
with cryptic missing-column errors.
```

| Flag | Default | Description |
|---|---|---|
| `--dsn` | — | RQLite DSN (default: this node's index rqlite from /opt/orama/.orama/configs/node.yaml) |

Subcommands: `apply`, `status`

## orama maint node schema apply

Apply pending migrations to the local RQLite

```text
orama maint node schema apply [flags]
```

```text
Apply every embedded migration not yet recorded in schema_migrations.

Each migration runs as one transaction together with its schema_migrations
row, so it is applied and recorded, or not applied at all. A statement whose
effect is already in place (an existing column, table or index, left by an older
engine that applied migrations statement by statement) is skipped. Any other
error aborts the run at that migration, which leaves no trace; re-running is
safe because each migration is independently versioned.
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | `false` | Skip the confirmation prompt |


## orama maint node schema status

Show required vs applied schema version + pending migrations

```text
orama maint node schema status
```


## orama maint node stage-archive

Verify a pushed build archive and put it in place (run by 'orama maint push')

```text
orama maint node stage-archive [flags]
```

```text
Verify a build archive against this node's trust anchor, /etc/orama/archive-signers,
and only then replace the archive files under /opt/orama with it.

'orama maint push' runs this on every node with the node's installed orama. The
archive is extracted into a private directory, its manifest signature must
recover to a trusted signer and every file must match the signed manifest;
anything else leaves /opt/orama untouched. The replacement is undone if any
step of it fails, and holds the lock install and upgrade take on /opt/orama.

--trust-signers creates the anchor on a node installed before archives were
signed, and only after the archive has verified against those addresses. It
never changes an existing anchor.

--release-metadata and --release-target opt in to the release root adopted at
/etc/orama/release-root.json. Before anything is extracted, the archive file
must be that target in the TUF metadata: the root signs timestamp, snapshot and
targets, the timestamp is unexpired, the snapshot is not older than the one
recorded in /etc/orama/release-seen.json, and the file has the target's length
and hashes. Any failure refuses the archive; the wallet check is not tried
instead. An archive that passes is then verified against the trust anchor as
above: the release root is required in addition to it, not in place of it.

--release-only is the one case where the release root is enough: the archive
is an unsigned release (the CI build), it must not name signers or a release
root, and the node records in /etc/orama/release-staged.json that it was staged
through the release root, which is what lets 'orama node upgrade' install it.
A channel target ('nightly/orama-...') is a target of the top-level targets
metadata, under its channel's path prefix.
```

| Flag | Default | Description |
|---|---|---|
| `--archive` | — | The pushed archive on this node [required] |
| `--release-metadata` | — | Directory holding timestamp.json, snapshot.json and targets.json; requires --release-target |
| `--release-only` | `false` | Accept the archive on the release root's checks alone, without a wallet signature (an installed node; needs --release-metadata and --release-target) |
| `--release-target` | — | Name the archive has in the release targets metadata; requires --release-metadata |
| `--trust-signers` | — | Create a missing trust anchor with these addresses (nodes installed before archive signing only) |


## orama maint node unlock

Unlock an OramaOS genesis node

```text
orama maint node unlock [flags]
```

```text
Manually unlock a genesis OramaOS node that cannot reconstruct its LUKS key
via Shamir shares (not enough peers online).

This is only needed for the genesis node before enough peers have joined for
Shamir-based unlock. Once 5+ peers exist, the genesis node transitions to
normal Shamir unlock and this command is no longer needed.

The encrypted genesis key is written where the node was created, and the
OramaOS agent does not serve it, so --key-file is required. The command used to
try fetching it from the node first, on a path the agent has never served, and
spent ten seconds timing out before telling you to pass the flag.

Usage:
  orama maint node unlock --genesis --node-ip <wg-ip> --key-file <path>

The node must be reachable over WireGuard on port 9998.
```

| Flag | Default | Description |
|---|---|---|
| `--genesis` | `false` | Confirm genesis node unlock |
| `--key-file` | — | Path to the encrypted genesis key file (required) |
| `--node-ip` | — | WireGuard IP of the OramaOS node (required) |


## orama maint operator

Operate the cluster

```text
orama maint operator
```

```text
Commands for the wallets on the cluster's operator list.

Every one of them needs you signed in with a wallet on that list. No role in a
namespace and no API key is an operator's, not even a key of a namespace an
operator owns.
```

Subcommands: `add`, `list`, `remove`, `rotate-secrets`, `rotate-signing-key`

## orama maint operator add

Let another wallet operate this cluster

```text
orama maint operator add <wallet>
```


## orama maint operator list

List the wallets that operate this cluster

```text
orama maint operator list
```


## orama maint operator remove

Take a wallet off this cluster's operator list

```text
orama maint operator remove <wallet>
```


## orama maint operator rotate-secrets

Re-encrypt stored secrets, optionally under a new encryption root

```text
orama maint operator rotate-secrets [flags]
```

```text
Rewrite function secrets, push tokens, TURN secrets, deployment
environments and agent tokens onto the versioned envelope (enc:v1:<id>:).
A deployment's environment is written as enc:v2:<id>:, sealed to its namespace
and deployment id, so a copy of the ciphertext in another deployment's row does
not open. This is also what turns on bound writes: until it has run, gateways
keep writing deployment environments in the envelope an older gateway reads.

Without --rotate the IKM does not change: leftover plaintext and the legacy
enc: form are rewritten so a captured snapshot of the old format is no longer
the live one, and Decrypt can fail closed.

With --rotate a new encryption root is generated. Existing ciphertext is
re-encrypted under it. A disk that holds only the previous root cannot open
the new rows. IPFS-Cluster and the mesh bearer are not touched.

Do not run this until every gateway is on a binary that can read enc:v2:.
The walker is idempotent; if it is interrupted, run it again.
```

| Flag | Default | Description |
|---|---|---|
| `--rotate` | `false` | Generate a new encryption root and re-encrypt under it |


## orama maint operator rotate-signing-key

Replace the key this gateway signs tokens with

```text
orama maint operator rotate-signing-key
```

```text
Generate a new signing key for the gateway, publish it, and start signing
with it.

Nobody is signed out. The outgoing key keeps verifying the tokens it already
signed until they expire on their own, so both keys are accepted for one
access-token lifetime and then the old one stops.

The key used to be derived from the cluster secret, which meant there was
nothing to rotate to: changing it meant changing the cluster secret, which
invalidates every token in the cluster at once.
```


## orama maint push

Push the binary archive to your nodes

```text
orama maint push [flags]
```

```text
Upload the pre-built binary archive to nodes and extract it.

The archive is uploaded from this machine to each node in turn: node SSH keys
never leave it, and no node is a hub. --direct is accepted and changes nothing.

--archive names the build: the path 'orama maint build' printed. There is no
default — the newest archive in /tmp may be another checkout's build.

Examples:
  orama maint push --env devnet --archive /tmp/orama-0.200.0-linux-amd64.tar.gz
  orama maint push --env devnet --archive <path> --node 1.2.3.4
  orama maint push --host 1.2.3.4 --archive <path>           # A node not in the inventory yet
  orama maint push --env devnet --archive <path> --trust-signers 0xYourWallet  # Nodes from before archive signing

Each node verifies the archive with its installed orama before anything under
/opt/orama changes: the manifest signature must recover to an address in the
node's /etc/orama/archive-signers and every file must match the manifest.
```

| Flag | Default | Description |
|---|---|---|
| `--archive` | — | The build archive to push (the path `orama maint build` printed) [required] |
| `--direct` | `false` | Accepted and ignored: every push uploads from this machine to each node in turn |
| `--env` | — | Target environment (default: active) |
| `--host` | — | Push to a node that is not in the inventory yet |
| `--node` | — | Push to a single node IP from the inventory |
| `--trust-signers` | — | Create the archive trust anchor on nodes that have none (installed before archive signing); never changes an existing one |
| `--user` | — | SSH user for --host (default: root) |


## orama maint release

Cut and publish a signed release (maintainers)

```text
orama maint release
```

```text
Cut a release into the Orama release repository and publish it.

The metadata is TUF, signed with the one release key in your RootWallet. Every
signature is approved by you in the RootWallet desktop app; the command says
what each approval is for before it asks. The release key is listed in the
root for all four roles, so the same wallet signs everything.

  init-root          make the repository's root (once); prints the digest networks pin
  renew-root         the next version of the root, same keys, a new expiry
  cut                list an archive on a channel and sign it (3 approvals);
                     nightly and main need a passing, runner-signed fleet e2e report
  refresh-timestamp  re-sign only the timestamp (1 approval)
  publish            upload the archives to Gitea and the metadata to the release host

A channel is nightly, main, or dev/<branch>. cut writes only to the repository
directory (--dir); publish is the step that touches the network, so cut
--dry-run and cut work offline.
```

Subcommands: `cut`, `init-root`, `publish`, `refresh-timestamp`, `renew-root`

## orama maint release cut

List an archive on a channel and sign the metadata (3 approvals)

```text
orama maint release cut --channel nightly|main|dev/<branch> --archive <path> --fleet-report <dir> [flags]
```

```text
List one or more archives of one version (amd64 and arm64) under a channel and
sign the result: targets.json, then snapshot.json, then timestamp.json, three
approvals in the RootWallet desktop app. Each is announced first. The command
checks that a client would accept the metadata before it writes anything, and
writes only to --dir; it uploads nothing. Run publish to upload.

An archive must be one built with "orama maint build --unsigned" (a release is trusted
through the release root, not a wallet), and an amd64 one carries the global layer.
A version must be dotted numeric (0.3.1) and newer than the channel's newest;
a release is immutable (--replace is for a dev/<branch> build that reuses a version).
Only the newest --retention versions of the channel stay listed. The timestamp
is valid 7 days for nightly and dev, 30 for main.

A nightly or main release stands on a fleet e2e run of the archives' commit:
--fleet-report is that run's artifact directory, and its report.json must be
signed by a runner key compiled into this binary (core/pkg/releasepub/runner-keys.txt),
name exactly that commit with a clean working tree, and have the verdict PASS
(every feature green, the coverage gate held). A dev/<branch> build needs none,
but one it names is checked the same way.

--dry-run plans the release and stops: nothing is signed, written or uploaded,
and the RootWallet is not contacted.
```

| Flag | Default | Description |
|---|---|---|
| `--allow-cluster-only` | `false` | Release an amd64 archive built with --skip-global-layer (nodes cannot orama global install from it) |
| `--archive` | — | An orama-&lt;version>-linux-&lt;arch>.tar.gz to release; repeatable [required] |
| `--channel` | — | nightly, main or dev/&lt;branch> [required] |
| `--dir` | — | The release repository working directory (default ~/.orama/release-repo) |
| `--dry-run` | `false` | Plan the release; sign, write and upload nothing |
| `--fleet-report` | — | The artifact directory of the passing, runner-signed fleet e2e run of the archives' commit [required for nightly and main] |
| `--replace` | `false` | Let a listed path change its bytes (dev builds only) |
| `--retention` | `3` | How many versions of the channel stay listed |


## orama maint release init-root

Make the repository's root from your RootWallet's release key

```text
orama maint release init-root [flags]
```

```text
Make version 1 of the repository's TUF root: your RootWallet's release public key
for the root, timestamp, snapshot and targets roles, threshold 1, valid for a
year. One approval. Prints the root's SHA-256, the digest network manifests pin
(release_root_sha256), and leaves 1.root.json and root.json in --dir. It refuses
a directory that already has a root.

Today one release key holds all four roles: whoever holds it can sign a root, a
targets file, a snapshot and a timestamp, and it is the only key that can sign
the next root. --keys <file> makes a root that splits the roles when the wallet
can hold more keys, without any other change: a JSON file naming, per role, the
keys (64 hex digits of an ed25519 public key, or "wallet" for your own release
key) and the threshold, for example

  {"root": {"keys": ["wallet"]},
   "targets": {"keys": ["<hex>", "<hex>"], "threshold": 2}}

A role that is left out is your release key at threshold 1. This command signs
the root once, with your key, so your key must be among the root keys and the root
threshold must be 1.
```

| Flag | Default | Description |
|---|---|---|
| `--dir` | — | The release repository working directory (default ~/.orama/release-repo) |
| `--keys` | — | A JSON file naming the keys and threshold of each role (default: your release key for all four roles, threshold 1) |


## orama maint release publish

Upload what cut left: archives to Gitea, metadata to the release host

```text
orama maint release publish --dir <dir> [flags]
```

```text
Upload the last cut or refresh. Before anything is sent the metadata in --dir is
checked as a client would check it, and each archive against the hash the
signed targets name. Then the archives go to the Gitea release for the tag
(created if it is not there; an archive already there is an error), then the
root, targets and snapshot go to the release host with rsync, and the timestamp
last.

Needs GITEA_TOKEN (a Gitea token with repository write) and ssh access to the
release host. --dry-run prints the plan.
```

| Flag | Default | Description |
|---|---|---|
| `--dir` | — | The release repository working directory (default ~/.orama/release-repo) |
| `--dry-run` | `false` | Check the directory and print the plan; send nothing |
| `--gitea-api` | `https://git.icxcnika.io/api/v1` | The Gitea instance API base |
| `--gitea-repo` | `ICXCNIKA/orama` | The Gitea repository (owner/name) whose releases hold the archives |
| `--metadata-dest` | `releases.orama.network:/opt/orama-releases/` | The rsync destination of the metadata on the release host |


## orama maint release refresh-timestamp

Re-sign only the timestamp (1 approval)

```text
orama maint release refresh-timestamp --channel nightly|main|dev/<branch> [flags]
```

```text
Re-sign the timestamp alone: the next version, naming the snapshot already in
--dir, valid 7 days (nightly, dev) or 30 (main) from now. Clients refuse a
timestamp that has expired, so a repository nobody cuts into needs this before
the last one runs out. Then publish.
```

| Flag | Default | Description |
|---|---|---|
| `--channel` | — | Sets the validity: nightly, main or dev/&lt;branch> [required] |
| `--dir` | — | The release repository working directory (default ~/.orama/release-repo) |


## orama maint release renew-root

Make the next version of the root with a new expiry

```text
orama maint release renew-root [flags]
```

```text
Make the next version of the root: the same keys, a new year of validity. One
approval. Clients that hold the previous root fetch <N+1>.root.json, verify it
against the root they hold and adopt it, so nobody is handed a new file. Do it
before the root expires; an expired root stops every release.
```

| Flag | Default | Description |
|---|---|---|
| `--dir` | — | The release repository working directory (default ~/.orama/release-repo) |


## orama maint rollout

Build, push, and rolling upgrade every node in an environment

```text
orama maint rollout [flags]
```

```text
Full deployment pipeline: build the binary archive, push it to every node,
then upgrade them one at a time.

The rolling upgrade prints its plan — which node holds the raft leadership and
the order the restarts happen in — and stops unless --yes is given.

Examples:
  orama maint rollout --env testnet             # Build, push, then print the plan
  orama maint rollout --env testnet --yes       # Execute the plan
  orama maint rollout --env testnet --no-build  # Reuse the existing archive
```

| Flag | Default | Description |
|---|---|---|
| `--archive` | — | With --no-build: the build archive to roll out |
| `--delay` | `300` | Seconds a node has to rejoin the cluster after its upgrade before the rollout stops |
| `--env` | — | Target environment (devnet, testnet) [required] |
| `--no-build` | `false` | Skip the build step; roll out the archive named by --archive |
| `--yes` | `false` | Execute the rollout plan instead of only printing it |


## orama maint sandbox

Manage ephemeral Hetzner Cloud clusters for testing

```text
orama maint sandbox
```

```text
Spin up temporary 5-node Orama clusters on Hetzner Cloud for development and testing.

Setup (one-time):
  orama maint sandbox setup

Usage:
  orama maint sandbox create [--name <name>] [--archive <path>]
                                           Create a new 5-node cluster
  orama maint sandbox destroy [--name <name>]    Tear down a cluster
  orama maint sandbox list                       List active sandboxes
  orama maint sandbox status [--name <name>]     Show cluster health
  orama maint sandbox rollout [--name <name>] [--archive <path>]
                                           Build + push + rolling upgrade
  orama maint sandbox ssh <node-number>          SSH into a sandbox node (1-5)
  orama maint sandbox reset                      Delete all infra and config to start fresh

The archive (--archive, or this checkout built now) must be signed by the
RootWallet account that is unlocked: it is the only signer a sandbox trusts.
Create and rollout install it the way 'orama node setup' and 'orama maint push' do.
```

Subcommands: `create`, `destroy`, `list`, `reset`, `rollout`, `setup`, `ssh`, `status`

## orama maint sandbox create

Create a new 5-node sandbox cluster (~5 min)

```text
orama maint sandbox create [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--archive` | — | Build archive to deploy (default: build this checkout now) |
| `--name` | — | Sandbox name (random if not specified) |


## orama maint sandbox destroy

Destroy a sandbox cluster and release resources

```text
orama maint sandbox destroy [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Skip confirmation |
| `--name` | — | Sandbox name (uses active if not specified) |


## orama maint sandbox list

List active sandbox clusters

```text
orama maint sandbox list
```


## orama maint sandbox reset

Delete all sandbox infrastructure and config to start fresh

```text
orama maint sandbox reset
```

```text
Deletes floating IPs, firewall, and SSH key from Hetzner Cloud,
then removes the local config (~/.orama/sandbox.yaml) and SSH keys.

Use this when you need to switch datacenter locations (floating IPs are
location-bound) or to completely start over with sandbox setup.
```


## orama maint sandbox rollout

Build + push + rolling upgrade to sandbox cluster

```text
orama maint sandbox rollout [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--archive` | — | Build archive to roll out (default: build this checkout now) |
| `--name` | — | Sandbox name (uses active if not specified) |


## orama maint sandbox setup

Interactive setup: Hetzner API key, domain, floating IPs, SSH key

```text
orama maint sandbox setup
```


## orama maint sandbox ssh

SSH into a sandbox node (1-5)

```text
orama maint sandbox ssh <node-number> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--name` | — | Sandbox name (uses active if not specified) |


## orama maint sandbox status

Show cluster health report

```text
orama maint sandbox status [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--name` | — | Sandbox name (uses active if not specified) |


## orama maint vpn

Route traffic through an Orama Tor network

```text
orama maint vpn
```

```text
Join an Orama Tor network from this machine.

A network is described by its tor-network.json file: the directory authorities
and, optionally, the validator onion services it lists. up starts an unmodified
upstream tor on it and offers a SOCKS5 proxy on loopback; check joins the
network and proves a circuit reaches a validator's onion service.

The client has one route: the tor it starts, configured with the network's
authorities and no others. It never falls back to the public Tor network or to
a direct connection. When tor stops, the proxy port closes and whatever was
using it fails; nothing is routed around it. This is a proxy, not a system-wide
tunnel: only applications pointed at the SOCKS port, with names resolved by the
proxy (socks5h), use the network.

Only a private network can be joined: the public Orama network is not launched.
```

Subcommands: `check`, `up`

## orama maint vpn check

Join an Orama Tor network and reach a validator onion service through it

```text
orama maint vpn check [flags]
```

```text
Start tor on the network (stopped again when the check ends) and read an account
through each validator onion service's tx gate, over a fresh circuit each. The gate
serves only the account read, the broadcast and the tx lookup, so the check uses the
account read; the chain answering "account not found" for the probe address passes.

By default every validator onion service the network file lists is tried, and the check
passes when at least one answers; --onion tries only the one given. It fails when tor
cannot bootstrap on the network's authorities, when none of the onion services answers, and
when the network file lists none and no --onion is given. Nothing is tried outside the
network.
```

| Flag | Default | Description |
|---|---|---|
| `--data-dir` | — | Tor state directory (default: the user cache directory, per network) |
| `--network` | — | Orama Tor network file (tor-network.json) [required] ($ORAMA_ONION_NETWORK) |
| `--onion` | — | Check only this validator onion service (addr.onion[:port]) |
| `--tor` | `tor` | The tor binary to run |


## orama maint vpn up

Run a SOCKS5 proxy into an Orama Tor network

```text
orama maint vpn up [flags]
```

```text
Start tor on the network and keep it running until interrupted.

The SOCKS5 proxy listens on loopback only (--socks). Point an application at it
as socks5h, so the proxy resolves names, and each distinct SOCKS username gets
its own circuit. --dns also offers a DNS resolver on loopback that answers
through the network.

If tor stops, up exits with an error and the proxy port closes; applications
using it fail instead of connecting some other way.
```

| Flag | Default | Description |
|---|---|---|
| `--data-dir` | — | Tor state directory (default: the user cache directory, per network) |
| `--dns` | — | Loopback address for a DNS resolver that answers through the network (off by default) |
| `--network` | — | Orama Tor network file (tor-network.json) [required] ($ORAMA_ONION_NETWORK) |
| `--socks` | `127.0.0.1:9150` | Loopback address for the SOCKS5 proxy |
| `--tor` | `tor` | The tor binary to run |


## orama members

Manage who may work in a namespace

```text
orama members
```

```text
List, add and remove the wallets that hold a grant in a namespace, and
transfer the namespace itself.

A namespace has exactly one owner. Everybody else holds a role:

  admin      the control plane — deployments, functions, secrets, members, keys,
             raw database; everything the owner may do except transfer, back up
             and restore the namespace or export its whole database
  developer  deployments, functions, databases, secrets, sending push, and the
             data plane; not members, keys or the namespace's settings
  runtime    the data plane — invoke, storage, push registration, webrtc, proxy,
             pubsub, cache; not sending push notifications
  reader     a member with no grant at all

Who may run these: list, add and remove need the owner or an admin, signed in
with 'orama auth login --namespace <ns>', or an admin-scope key in ORAMA_TOKEN.
Transfer needs the owner's own wallet session. Any other member is refused, so
'orama auth whoami' is how a member sees its own role.

Ownership is not a role you can hand out: use 'orama members transfer'.
```

Subcommands: `add`, `list`, `remove`, `transfer`

## orama members add

Give a wallet a role in this namespace

```text
orama members add <wallet> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--expires-in-hours` | `0` | Expire the grant after this many hours (default: never) |
| `--name` | — | Human label for this member |
| `--namespace` | — | Namespace name |
| `--resource` | — | Narrow the role to a resource, e.g. storage:avatars/* (applied in the cache, fn, pubsub, storage domains; any other is refused) |
| `--role` | — | Role to grant (reader, runtime, developer, admin) |


## orama members list

List who holds a grant in this namespace

```text
orama members list [flags]
```

Aliases: `ls`

| Flag | Default | Description |
|---|---|---|
| `--namespace` | — | Namespace name |


## orama members remove

Take a wallet's grant away

```text
orama members remove <wallet> [flags]
```

Aliases: `rm`

| Flag | Default | Description |
|---|---|---|
| `--namespace` | — | Namespace name |


## orama members transfer

Hand this namespace to another wallet

```text
orama members transfer <wallet> [flags]
```

```text
Make another wallet the owner of this namespace.

Only the current owner may do this, signed in with its own wallet (no key holds
the owner's role), and it is one step rather than a removal and a grant, so
there is no moment where the namespace has no owner.
You keep an admin grant, so handing a project over does not lock you out of it;
the new owner removes it with 'orama members remove' if they want you gone.
Keys minted before the transfer keep working until they expire or are revoked.
The new owner signs in with 'orama auth login --namespace <ns>', and
'orama namespace list' then shows the namespace as theirs.
<wallet> must be a wallet address (0x and 40 hex digits, or a Solana public key);
the gateway refuses anything else rather than hand the namespace to nobody.
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Skip confirmation prompt |
| `--namespace` | — | Namespace name |


## orama namespace

Manage namespaces

```text
orama namespace
```

Aliases: `ns`

```text
List, delete, and repair namespaces on the Orama network.
```

Subcommands: `attestation-policy`, `backup-open`, `backup-seal`, `backup`, `cors-origins`, `create`, `delete`, `disable`, `enable`, `keys`, `list`, `repair`, `restore-key`, `restore`, `rqlite`, `session-policy`, `webrtc-status`

## orama namespace attestation-policy

Show or set which app builds may call a namespace (device attestation)

```text
orama namespace attestation-policy [flags]
```

```text
With no flags, show the namespace's attestation policy. With flags, change them; a
flag left out keeps its value.

A namespace that attests admits to its functions and data plane only the genuine
build of its app on a genuine device: iOS through App Attest, Android (GrapheneOS
included) through hardware key attestation. A script holding a valid session and
the app's API key is refused.

  --mode off|monitor|enforce   monitor refuses nothing and counts what enforce
                               would refuse (orama status traffic); enforce refuses
  --ios-team-id, --ios-bundle-id   the iOS App ID accepted
  --allow-development          accept App Attest's development environment (devnet)
  --min-ios-version            the oldest CFBundleVersion accepted (iOS 27 and later
                               sign it; older iOS is accepted with the version unknown)
  --android-package            the Android package accepted
  --android-signing-digest     a lowercase-hex SHA-256 of an accepted signing
                               certificate; repeat for several (replaces the list)
  --allow-grapheneos           accept GrapheneOS beside stock verified boot
  --min-android-version-code   the oldest versionCode accepted
  --max-patch-age-months       refuse an Android OS patch older than this (0: no limit)
  --reattest-interval          how long an Android attestation lasts (default 168h)

Changing the policy needs write access to the namespace.

  orama namespace attestation-policy --namespace anchat --mode monitor \
    --android-package chat.anchat --android-signing-digest <sha256> --allow-grapheneos
```

| Flag | Default | Description |
|---|---|---|
| `--allow-development` | `false` | Accept App Attest's development environment (devnet) |
| `--allow-grapheneos` | `false` | Accept GrapheneOS beside stock verified boot |
| `--android-package` | — | Package name of the Android app |
| `--android-signing-digest` | — | Lowercase-hex SHA-256 of an accepted signing certificate; repeat for several |
| `--ios-bundle-id` | — | Bundle id of the iOS app |
| `--ios-team-id` | — | Apple team id of the iOS app |
| `--max-patch-age-months` | `0` | Refuse an Android OS patch older than this many months (0: no limit) |
| `--min-android-version-code` | `0` | Oldest versionCode accepted (0 for none) |
| `--min-ios-version` | — | Oldest CFBundleVersion accepted ("" for none) |
| `--mode` | — | off \| monitor \| enforce |
| `--namespace` | — | Namespace name |
| `--reattest-interval` | — | How long an Android attestation lasts, e.g. 168h |


## orama namespace backup

Take a backup of the namespace, sealed to your X25519 public key

```text
orama namespace backup [flags]
```

```text
Ask the namespace gateway for a backup: its RQLite snapshot, the CIDs it
has pinned, and its secrets, decrypted by the cluster and sealed with the rest
to the public key you give. The cluster never holds the private key and cannot
open what it wrote. Keep the private key off the cluster.

--out writes the sealed file. --deal-dir also seals it into one slot-N file per
replica of a private storage deal (under your orama-storage-v1 key and repair
seed) and prints the 'orama storage create' and 'orama storage put' commands
that open the deal and upload the slots. Restore it from the deal with
'orama namespace restore --from-deal'. The backup is taken when you run the
command; the cluster does not take or store backups by itself.

It goes to the namespace's own gateway (the host 'orama auth login --namespace'
stored). With ORAMA_TOKEN, set ORAMA_API_URL to that host
(https://ns-<name>.<domain>): the environment's gateway does not serve backup.
```

| Flag | Default | Description |
|---|---|---|
| `--deal-dir` | — | also seal the backup into one slot-N file per replica of a private storage deal, in this directory |
| `--deal-nonce` | — | the deal's 32-byte nonce, hex; the same value goes to 'orama storage create --nonce' (needed with --deal-dir) |
| `--deal-replicas` | `3` | number of slots to write; the same value goes to 'orama storage create --replicas' |
| `--key` | — | your X25519 backup public key, 64 hex characters |
| `--out` | — | file to write the sealed backup to |
| `--repair-seed-file` | — | file holding your repair seed, hex, mode 0600 (needed with --deal-dir) |
| `--storage-key-file` | — | file holding your orama-storage-v1 key, hex, mode 0600 (needed with --deal-dir) |


## orama namespace backup-open

Decrypt a backup file with an X25519 private key

```text
orama namespace backup-open [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--in` | — | input file |
| `--key` | — | 32-byte X25519 key, hex (public for seal, private for open) |
| `--out` | — | output file |


## orama namespace backup-seal

Encrypt a backup file to an X25519 public key

```text
orama namespace backup-seal [flags]
```

```text
Encrypt a file to the owner's backup public key.

The cluster holds only that public key. It cannot decrypt the file.
This seals any file. A namespace's own backup is 'orama namespace backup', and
putting one back is 'orama namespace restore'.
```

| Flag | Default | Description |
|---|---|---|
| `--in` | — | input file |
| `--key` | — | 32-byte X25519 key, hex (public for seal, private for open) |
| `--out` | — | output file |


## orama namespace cors-origins

Show or set the browser origins a namespace's apps are served from

```text
orama namespace cors-origins [flags]
```

```text
With no flags, show the origins a browser page may call the namespace from,
beyond the ones every gateway admits: the base domain and the names under it,
localhost, any *.localhost name, the loopback addresses, and the Tauri webviews
(tauri://localhost on macOS, Linux and iOS; http(s)://tauri.localhost on
Windows and Android).

--set replaces the list (repeat it for several origins, at most 20); --clear
empties it. An origin is scheme://host[:port], http, https or tauri, with no
path and no wildcard. The list applies to requests addressed to the
namespace's host (ns-<name>.<base domain>), at once on the gateway that
recorded it and within 30 seconds on every other.

Changing the list needs write access to the namespace (an owner or an admin).

  orama namespace cors-origins --namespace anchat --set https://app.anchat.chat --set https://anchat.chat
```

| Flag | Default | Description |
|---|---|---|
| `--clear` | `false` | Remove every allowed origin |
| `--namespace` | — | Namespace name |
| `--set` | — | An allowed origin; repeat for several (replaces the list) |


## orama namespace create

Create a namespace and start its cluster

```text
orama namespace create <name>
```

```text
Create a namespace. The wallet you are signed in as becomes its owner.

Creating a namespace used to happen by itself: signing in to a name that did
not exist created it. So a typo made a namespace, and one belonged to whoever
happened to sign in first.

  orama namespace create myapp
  orama auth login --namespace myapp
```


## orama namespace delete

Delete the current namespace and all its resources

```text
orama namespace delete [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Skip confirmation prompt |


## orama namespace disable

Disable a feature for a namespace

```text
orama namespace disable <feature> [flags]
```

```text
Disable a feature for a namespace. Supported features: webrtc, webrtc-stealth
```

| Flag | Default | Description |
|---|---|---|
| `--namespace` | — | Namespace name |


## orama namespace enable

Enable a feature for a namespace

```text
orama namespace enable <feature> [flags]
```

```text
Enable a feature for a namespace. Supported features: webrtc, webrtc-stealth
```

| Flag | Default | Description |
|---|---|---|
| `--namespace` | — | Namespace name |


## orama namespace keys

Manage scoped API keys (bugboard #148)

```text
orama namespace keys
```

```text
Create, list, rotate and revoke a namespace's scoped API keys. Profiles:
invoke-only | app-runtime | admin.

Who may run these: the namespace's owner or an admin member, signed in with
'orama auth login --namespace <ns>' (RootWallet signs the challenge), or an
admin-scope key in ORAMA_TOKEN. A developer, runtime or reader member, and a key
holding anything less than admin, is refused with INSUFFICIENT_SCOPE: managing
keys is handing out authority. 'orama auth whoami' shows the role a credential
holds. The commands act on the namespace the credential belongs to; --namespace
picks which signed-in session to use.
```

Subcommands: `create`, `list`, `revoke-legacy`, `revoke`, `rotate`

## orama namespace keys create

Mint a new scoped API key

```text
orama namespace keys create [flags]
```

```text
Mint a new scoped API key. The key is shown once.

Which key goes where:
  - An app that needs nothing before sign-in ships no key: the sign-in
    handshake and public functions need no credential, and after sign-in the
    user's session reaches everything else.
  - An app that must call functions before sign-in ships an invoke-only key,
    narrowed with --resource to those functions.
  - A function that reclaims storage gets its own key, --scope storage, kept
    in the namespace's secrets. No key that ships in an app holds storage.
  - A backend that sends push notifications over HTTP gets its own key,
    --scope push-send, kept on the server. 'push' registers devices; only
    push-send sends, to any user or topic of the namespace.

Profiles: invoke-only (invoke), app-runtime (invoke, push, webrtc, proxy;
never storage or push-send), admin (the control plane; CI and a developer's
machine only).

--resource narrows the key to the functions it names: the gateway refuses it on
every other function, on HTTP invoke and the function WebSocket alike, with the
403 a member narrowed by 'members add --resource' gets. Without it the key
reaches the whole of its scopes.
```

| Flag | Default | Description |
|---|---|---|
| `--expires-in-days` | `0` | How long the key lives, in days (default 90, max 365). A key that never expires is not on offer |
| `--label` | — | Human label for the key |
| `--namespace` | — | Namespace name |
| `--resource` | — | Narrow the key to what this selector matches, e.g. fn:premium-status or 'fn:premium-*'. Repeat for several; the key then reaches only these. Each must narrow one of its scopes (fn: needs invoke), and every scope must be narrowed by one |
| `--scope` | — | Profile (invoke-only\|app-runtime\|admin) or a comma-separated grant list (admin, cache, invoke, proxy, pubsub, push, push-send, storage, webrtc) |


## orama namespace keys list

List scoped API keys

```text
orama namespace keys list [flags]
```

Aliases: `ls`

| Flag | Default | Description |
|---|---|---|
| `--namespace` | — | Namespace name |


## orama namespace keys revoke

Revoke a single API key by id

```text
orama namespace keys revoke [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--id` | `0` | Key id to revoke |
| `--namespace` | — | Namespace name |


## orama namespace keys revoke-legacy

Revoke ALL legacy (unscoped) keys — the cutover step

```text
orama namespace keys revoke-legacy [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Skip confirmation prompt |
| `--namespace` | — | Namespace name |


## orama namespace keys rotate

Mint a successor to a key and keep the old one working for an overlap

```text
orama namespace keys rotate [flags]
```

```text
Mint a new key with the same grants, resources and label, and shorten the
original's life to the overlap.

Rotating by minting a new key and revoking the old one in the same breath is an
outage: whatever is deployed with the old key stops the moment the new one
exists. The overlap is the window in which to deploy the successor — both keys
work, and the original then expires on its own.
```

| Flag | Default | Description |
|---|---|---|
| `--expires-in-days` | `0` | How long the successor lives, in days (default 90) |
| `--id` | `0` | Key id to rotate |
| `--namespace` | — | Namespace name |
| `--overlap-days` | `0` | How long the old key keeps working (default 7, max 30) — the window to deploy the new one |


## orama namespace list

List namespaces owned by the current wallet

```text
orama namespace list
```

Aliases: `ls`


## orama namespace repair

Repair an under-provisioned namespace cluster

```text
orama namespace repair <namespace>
```

```text
Repair an under-provisioned namespace cluster. Run it on a node. It talks to that node's gateway on the node's WireGuard address; localhost is where public traffic arrives, so a repair sent there is refused.
```


## orama namespace restore

Restore a namespace backup onto the namespace gateway (DESTRUCTIVE)

```text
orama namespace restore [flags]
```

```text
Open a backup on this machine with your private key, seal its secrets to
the destination gateway's restore key (--dest-key, from 'orama namespace
restore-key'), and send it to the namespace gateway you are signed in to.

The gateway replaces the namespace's entire RQLite database with the backup,
writes the secrets under its own cluster's encryption root, and pins every CID
in the backup. The namespace must already exist on the destination, and
--namespace must name the namespace the backup was taken of. A wrong key, a
corrupt file or a different namespace stops before anything is sent.

The sealed backup is the file at --in, or the private storage deal that holds
it (--from-deal, with --rpc and your storage key and repair seed files): the
first slot a provider serves with the on-chain root is fetched and opened.
Give one of the two.

The gateway also refuses, before writing anything, a restore that would put
the namespace over its storage quota on the destination, and it keeps the
destination's quota rather than the one in the backup. It runs one backup or
restore at a time and answers 429 while one is running.

The live keys, grants and sessions (in the cluster's registry) are not
restored: a restore neither brings back a key that was revoked nor removes one
minted since. The backup's database still carries stale copies of those rows,
which nothing reads. Before loading, the gateway checks the database image and
refuses (400, nothing written) one that is damaged or carries a trigger or view
tenant SQL could not create, or stored-object records for content only other
namespaces hold; it removes plaintext API keys and other namespaces' records an older backup
may carry, and checks again, after the load.

With ORAMA_TOKEN, set ORAMA_API_URL to the namespace's gateway
(https://ns-<name>.<domain>): the environment's gateway does not serve restore.
```

| Flag | Default | Description |
|---|---|---|
| `--dest-key` | — | destination gateway's restore public key, from 'orama namespace restore-key' |
| `--from-deal` | `0` | read the sealed backup from this private storage deal instead of --in |
| `--in` | — | sealed backup file |
| `--key-file` | — | file holding your X25519 backup private key, 64 hex characters |
| `--namespace` | — | namespace the backup was taken of; must match the backup |
| `--repair-seed-file` | — | file holding your repair seed, hex, mode 0600 (needed with --from-deal) |
| `--rpc` | — | oramad CometBFT RPC, for example http://127.0.0.1:31001 (needed with --from-deal) |
| `--storage-key-file` | — | file holding your orama-storage-v1 key, hex, mode 0600 (needed with --from-deal) |


## orama namespace restore-key

Print the namespace gateway's restore public key

```text
orama namespace restore-key
```

```text
Print the X25519 public key a restore's secrets are sealed to. It is
derived from the destination cluster's encryption root and the namespace, so
it is different for every namespace and changes when that root is rotated.
Pass it to 'orama namespace restore --dest-key'.
```


## orama namespace rqlite

Manage the namespace's internal RQLite database

```text
orama namespace rqlite
```

```text
Export and import the namespace's internal RQLite database: your own tables and the
namespace's functions, function secrets, stored-object records, quotas and push and
WebRTC settings. Keys, grants and deployments are in the cluster registry, not in it.

Both go to the namespace's own gateway (the host 'orama auth login --namespace' stored).
With ORAMA_TOKEN, set ORAMA_API_URL to that host (https://ns-<name>.<domain>): the
environment's gateway does not serve them.
```

Subcommands: `export`, `import`

## orama namespace rqlite export

Export the namespace's RQLite database to a local SQLite file

```text
orama namespace rqlite export [flags]
```

```text
Downloads a consistent SQLite snapshot of the namespace's internal RQLite database.
```

| Flag | Default | Description |
|---|---|---|
| `-o`, `--output` | — | Output file path (default: rqlite-export.db) |


## orama namespace rqlite import

Import a SQLite dump into the namespace's RQLite (DESTRUCTIVE)

```text
orama namespace rqlite import [flags]
```

```text
Replaces the namespace's entire RQLite database with the contents of the provided SQLite file.

WARNING: This is a destructive operation. All existing data in the namespace's RQLite
(your tables, functions, function secrets, stored-object records, quotas and push and
WebRTC settings) will be replaced with the imported file. The file must be a SQLite
database, as 'export' writes; at most 256 MiB. Live keys and grants (in the cluster
registry) are not replaced, and stale copies in the file are ignored. The namespace's
storage quota stays as it was. The gateway checks the file before loading it and
refuses (400, nothing written) one that is damaged or carries a trigger or view
tenant SQL could not create, or stored-object records for content only other
namespaces hold;
it removes plaintext API keys and other namespaces' records an older file may carry,
and checks again, after the load. One backup, restore, export or
import runs at a time on a gateway.
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | `false` | Replace the database without typing the namespace name |
| `-i`, `--input` | — | Input SQLite file path |


## orama namespace session-policy

Show or set who may sign in to a namespace and what its sessions bind

```text
orama namespace session-policy [flags]
```

```text
With no flags, show the namespace's session policy. With flags, set them; a flag
left out keeps its value.

  --sign-in members   only wallets holding a grant sign in (the default)
  --sign-in open      a wallet holding none may sign in too, as an end user of
                      the application. It gets a session and no API key, is
                      never granted anything, and reaches only what a grantless
                      wallet reaches. Closing it again ends those sessions at
                      their next refresh. A namespace nobody owns stays closed.

  --device-policy     optional | required | approval | function: what an end
                      user's sign-in must bind. Requiring devices revokes the
                      sign-in keys end users already hold. function: an
                      account's first device is active, and each later one
                      gets a restricted session until one of the namespace's
                      functions approves it (approve_device).

  --end-user-topics   the pub/sub topics an end user may subscribe to, list and
                      see presence on, comma-separated patterns ('*' matches any
                      run of characters, '/' included). It replaces the list,
                      and '' clears it. The default is none: an end user reads
                      no topic until it is listed. Members holding a grant read
                      what their grant says. A function's socket that bridges
                      topics to its users is not affected.

  --per-ip-limits     on | off (the default): ceilings on what one client
                      network (an IP address, an IPv6 /64) does, whatever
                      wallets it presents. For a namespace with open sign-in,
                      where a wallet costs nothing. Off by default because many
                      people can share one address (an office, carrier NAT).
  --new-wallets-per-ip-hour
                      when on, how many wallets that never signed in to the
                      namespace one network may sign in per hour, as end users
                      (default 20). Past it: 429 SIGN_IN_RATE_LIMITED.
  --invokes-per-ip-minute
                      when on, how many authenticated function invokes one
                      network may make per minute on each gateway (default
                      1200). Past it: 429 RATE_LIMITED scope=per_ip_authenticated.

Changing any of them needs write access to the namespace.

  orama namespace session-policy --namespace myapp --sign-in open
  orama namespace session-policy --namespace myapp --end-user-topics 'presence/*,users/global'
  orama namespace session-policy --namespace myapp --per-ip-limits on --new-wallets-per-ip-hour 10
```

| Flag | Default | Description |
|---|---|---|
| `--device-policy` | — | What an end user's sign-in must bind: optional \| required \| approval \| function |
| `--end-user-topics` | — | Pub/sub topic patterns end users may read, comma-separated; '' clears the list |
| `--invokes-per-ip-minute` | `0` | When per-IP limits are on: authenticated invokes one network may make per minute per gateway |
| `--namespace` | — | Namespace name |
| `--new-wallets-per-ip-hour` | `0` | When per-IP limits are on: new wallets one network may sign in per hour |
| `--per-ip-limits` | — | Per-client-network ceilings: on \| off |
| `--sign-in` | — | Who may sign in: members \| open |


## orama namespace webrtc-status

Show WebRTC service status for a namespace

```text
orama namespace webrtc-status [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--namespace` | — | Namespace name |


## orama network

Choose the network the CLI talks to

```text
orama network
```

```text
List, choose, add and remove the networks the CLI knows.

A network is a name for something you can reach: a cluster, through its
gateway, and, for a network of the registry such as testnet, the chain it runs,
the seeds to join through and the release root its software is verified against.
Every other command talks to the active network, or to the one --env names.

  orama network list                          every network and where it comes from
  orama network use <name>                    make one active
  orama network add <name> <gateway-url>      reach a cluster by its gateway
  orama network add <manifest-url>            trust a network that publishes a manifest
  orama network current                       the active network
  orama network remove <name>                 forget one
```

Subcommands: `add`, `current`, `list`, `remove`, `use`

## orama network add

Add a network by its manifest, or a cluster by its gateway

```text
orama network add <manifest-url> | <name> <gateway-url> [description] [flags]
```

```text
Add a network, one of two ways.

With one argument, a manifest URL: https://<host>/<path>/manifest.json. The
manifest names the chain id, the genesis digest, the seeds, the release channel
and the digest of the release root the network's software is verified against.
The command fetches it (https only, size-bounded) and the release-root.json
beside it, checks the root against the digest, shows the chain id and the digest,
and asks you to type yes. Nothing is stored before that. --yes confirms for a
script; the digest is then the only thing you trust, so a script should pass the
URL of a manifest it already checked.

With a name and a gateway URL, a cluster you reach through that gateway (and an
optional description). The URL must be https:// with a host (http:// only for a
gateway on this machine: localhost or a loopback address), because every command
sends its credential there. --ca-file trusts a PEM bundle for this gateway's
domain and every name under it, in addition to the system roots: a cluster on
Let's Encrypt's staging CA, or on a private CA. It is not trusted for any other
host. --network records which registry network the cluster runs on.
```

| Flag | Default | Description |
|---|---|---|
| `--ca-file` | — | PEM CA bundle to trust for this gateway's domain only |
| `--network` | — | Registry network this cluster runs on |
| `--yes` | `false` | Trust the manifest's network without asking |


## orama network current

Show the active network

```text
orama network current
```


## orama network list

List every network and where it comes from

```text
orama network list
```

```text
List the networks of the registry (built into this binary or added by URL) and the
gateways you configured, one row each. A name that is both shows both. The active
network is marked with *.
```


## orama network remove

Forget a network

```text
orama network remove <name>
```

```text
Forget the network of that name: the one you added by URL and the gateway you
configured, whichever exist. A network built into this binary stays in the list.
A name that is neither is not an error.
```


## orama network use

Make a network the active one

```text
orama network use <name>
```


## orama node

Node operator commands

```text
orama node
```

```text
Operate Orama nodes, both the one on this machine and the fleet you own.

Local, run on the node itself and needing root (sudo):
  uninstall, upgrade, start, stop, restart, status, logs, doctor, report, invite,
  trust, tls status

Remote, run from your machine and reaching nodes over SSH:
  list, setup, remove, wipe, dns delegation

Installing a node's software, staging an archive, auto-update, recovery and
migration are maintainer commands: see 'orama maint node'.
```

Subcommands: `dns`, `doctor`, `invite`, `list`, `logs`, `report`, `restart`, `start`, `status`, `stop`, `tls`, `trust`, `uninstall`, `upgrade`, `wipe`

## orama node dns

Cluster DNS: what the outside world needs to reach its nameservers

```text
orama node dns
```

Subcommands: `delegation`

## orama node dns delegation

Print the NS and glue records to create at the parent zone

```text
orama node dns delegation [flags]
```

```text
Print exactly the records the operator must create at the parent zone (or
registrar) so the internet reaches this cluster's nameservers: one NS record
per nameserver, and the glue A record that gives each nameserver its address.

Nameserver slots (ns1, ns2, …) are claimed by the --nameserver nodes as they
come up, so which address holds which name is only known to the cluster. This
reads it from the cluster over SSH. Only slots whose glue the cluster has
written are listed — the same set the cluster's own zone publishes.

After the records it asks the parent zone's own nameservers whether they
return them, never the local caching resolver (which can keep a "no such host"
from before the records existed for the zone's negative TTL), and says which
NS or glue record is missing or points at another address. The answer is
stored on the environment (environments.json), replacing the last result for
the domain. A parent zone that cannot answer is reported and nothing is
stored. --json adds "delegated" and "findings" to each domain.

Run it again after adding or removing a nameserver, and update the parent
zone to match. See orama.network/docs/operator/nameserver.
```

| Flag | Default | Description |
|---|---|---|
| `--cloudflare-token-file` | — | Create or update the NS and glue records in the parent Cloudflare zone, then check DNS |
| `--env` | — | Environment to read (devnet, testnet, …) [required] |


## orama node doctor

Diagnose common node issues

```text
orama node doctor
```

```text
Run a series of diagnostic checks on this node to identify
common issues with services, connectivity, disk space, and more.
```


## orama node invite

Manage invite tokens for joining the cluster

```text
orama node invite [flags]
```

```text
Generate invite tokens that allow new nodes to join the cluster.
Running without a subcommand creates a new token (same as 'invite create').
```

| Flag | Default | Description |
|---|---|---|
| `--expiry` | `1h0m0s` | How long the token stays valid |
| `--raw` | `false` | Print only the invite, for scripts (orama node setup --join-via reads it this way) |


## orama node list

List your nodes across environments

```text
orama node list [flags]
```

```text
List all nodes owned by your wallet. Queries the network API
with your stored credentials, falling back to nodes.conf.

Requires: orama auth login (for API-based resolution)
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Filter by environment (default: active environment) |


## orama node logs

View production service logs

```text
orama node logs <service> [flags]
```

```text
Stream the journal of one service on this node.

<service> is an alias or a unit name. A tenant service is a systemd template
instance, so name it in full:

  orama node logs orama-namespace-olric@anchat

--since takes a window rather than a line count, which is what a diagnostic
that greps for a periodic line needs:

  orama node logs node --since -30min | grep 'WireGuard peer sync completed'

Aliases: caddy, cluster, coredns, gateway, ipfs, ipfs-cluster, node, olric, rqlite, turn
```

| Flag | Default | Description |
|---|---|---|
| `--since` | — | Show entries newer than this, e.g. -30min or "2 hours ago" (overrides --lines) |
| `-f`, `--follow` | `false` | Stream new log lines as they arrive |
| `-n`, `--lines` | `50` | How many lines of history to show |


## orama node remove

Remove one node from the cluster, then erase it (replaced by orama remove)

```text
orama node remove [flags]
```

```text
The old path of 'orama remove', which is the command to use: this one runs it
(--force is --yes) and prints a notice. The removal is the same: the quorum
arithmetic for every raft cluster the node votes in, the tombstone, the
retirement and the wipe, plus the chain: a node in the validator set is refused
unless --drop-validator, and a node with the global layer needs --chain-node-id
or --no-chain. See 'orama remove --help'.

Examples:
  orama node remove --env testnet --node 1.2.3.4 --dry-run   # Show the plan only
  orama node remove --env testnet --node 1.2.3.4
  orama node remove --env testnet --node 1.2.3.4 --offline   # VPS already deleted
  orama node remove --env testnet --node 1.2.3.4 --force
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id you expect, for a network that is on no registry network (one from the registry already names it); the wallet signs for no other chain |
| `--chain-node-id` | — | The node's id in the chain's node registry: retire it there before removing it |
| `--drop-validator` | `false` | Remove the node although it signs for the validator set; its consensus key is erased with it |
| `--dry-run` | `false` | Print the quorum impact and the statements, change nothing |
| `--env` | — | Target environment (devnet, testnet) [required] |
| `--force` | `false` | Skip confirmation (DESTRUCTIVE) |
| `--no-chain` | `false` | Leave the node's chain registration alone (its bonds stay locked until you retire it) |
| `--node` | — | Public IP of the node to remove [required] |
| `--nuclear` | `false` | When wiping, also remove shared binaries, the Tor package and the system accounts Orama created |
| `--offline` | `false` | The node is already gone: retire it cluster-side only, do not try to wipe it |


## orama node report

Output comprehensive node health data as JSON

```text
orama node report [flags]
```

```text
Collect all system and service data from this node and output
as a single JSON blob. Designed to be called by 'orama status' over SSH.
Requires root privileges for full data collection.
```

| Flag | Default | Description |
|---|---|---|
| `--pretty` | `false` | Indent the JSON for reading, instead of one line |


## orama node restart

Restart all production services (requires sudo)

```text
orama node restart [flags]
```

```text
Restart all Orama services. Stops in dependency order then restarts.
Includes explicit namespace service restart.
The auto-update timer, and a run of the update agent already in progress, are
stopped for the restart, and the timer is started again once the node is serving,
so the update agent never runs against a node that is down. When the restart
fails or the node does not come to serve, the timer stays stopped (starting it
would fire the agent at once against a node that is down) and the output names
the command that starts it. An interrupt, a terminate or a hangup finishes the
restart, then starts the timer if the node serves; a timer that was stopped stays
stopped.
Use --force to bypass quorum safety check.
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Bypass quorum safety check |


## orama node setup

Set up a fresh VPS as an Orama node (use orama setup)

```text
orama node setup [flags]
```

```text
Use "orama setup": it does this for every machine you give it, and the rest of joining the
network as well. This command stays for now and installs the cluster node only.

Bootstrap a fresh VPS into a running Orama node in one command.

Creates an SSH key in rootwallet, installs it on the VPS, uploads the binary
archive, and runs the node install. For the first node, use --genesis to
create a new cluster.

Examples:
  # Genesis node (first node, creates new cluster).
  # Store the VPS login first: rw vault add 1.2.3.4 (username root).
  # --password is a switch; it reads that login. --archive is the path
  # "orama maint build" printed.
  orama node setup --ip 1.2.3.4 --password --env devnet \
    --base-domain devnet.orama.network --role nameserver --genesis \
    --archive /tmp/orama-<version>-linux-amd64.tar.gz

  # From a published release: no checkout, no Go or zig. The root is the
  # release signers' key set you decided to trust; the cluster adopts it.
  orama node setup --ip 1.2.3.4 --password --env mycluster \
    --base-domain cluster.example.com --role nameserver --genesis \
    --release 0.3.1 --release-repo https://releases.example.org/tuf --release-root ./root.json

  # Join existing cluster
  orama node setup --ip 5.6.7.8 --password --env devnet \
    --base-domain devnet.orama.network \
    --archive /tmp/orama-<version>-linux-amd64.tar.gz

  # Key-only VPS (no password login): install the RootWallet key once
  # with the key that opens it today. Do not pass --password as well.
  orama node setup --ip 5.6.7.8 --user ubuntu --bootstrap-key ~/.ssh/id_ed25519 \
    --env devnet --base-domain devnet.orama.network \
    --archive /tmp/orama-<version>-linux-amd64.tar.gz

  # Join as nameserver
  orama node setup --ip 9.10.11.12 --password --env devnet \
    --base-domain devnet.orama.network --role nameserver \
    --archive /tmp/orama-<version>-linux-amd64.tar.gz
```

| Flag | Default | Description |
|---|---|---|
| `--acme-ca` | — | ACME directory for the node's TLS certificates (passed to node install): letsencrypt, letsencrypt-staging or an https URL |
| `--archive` | — | Build archive to install — the path `orama maint build` printed [required]; a node already running this exact build is not re-uploaded |
| `--base-domain` | — | Base domain for the network |
| `--bootstrap-key` | — | SSH private key that opens the VPS today (key-only images, e.g. --user ubuntu); used once to install the RootWallet key, never stored |
| `--channel` | — | Release channel to read (default stable); with --release |
| `--env` | — | Target environment (default: active) |
| `--gateway` | — | Gateway URL of the cluster to join (default: the environment's): its domain, e.g. https://devnet.orama.network; the invite is minted through one of its nodes and pins that node's certificate |
| `--genesis` | `false` | Create a new cluster (first node) |
| `--host-key` | — | Expected SSH host-key fingerprint (SHA256:...) of the VPS; omit to confirm it interactively |
| `--ip` | — | Public IP address of the VPS (required) |
| `--join-via` | — | user@ip of a node already in the cluster; the invite is minted there over SSH (no 'orama auth login' needed) |
| `--password` | `false` | Bootstrap over password login; the password is read from your RootWallet vault login for the IP (rw vault add &lt;ip>), never from the command line |
| `--release-repo` | — | https URL of the release repository (TUF metadata and archives); with --release |
| `--release-root` | — | The TUF root.json of the release signers you trust, checked out of band; with --release. The cluster adopts it |
| `--release` | — | Install this published release version instead of an archive you built: it is fetched from --release-repo, verified against --release-root, then signed by your RootWallet |
| `--role` | `node` | Node role: node or nameserver |
| `--user` | `root` | SSH user on the VPS |


## orama node start

Start all production services (requires sudo)

```text
orama node start
```


## orama node status

Show the service status of the node on this machine

```text
orama node status
```

```text
Report the systemd units of the Orama node installed on this machine.

For the health of your whole fleet from your own machine, use 'orama status'.
```


## orama node stop

Stop all production services (requires sudo)

```text
orama node stop [flags]
```

```text
Stop all Orama services in dependency order and disable auto-start.
Includes namespace services, global services, and supporting services.
Use --force to bypass quorum safety check.
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Bypass quorum safety check |


## orama node tls

This node's TLS certificates

```text
orama node tls
```

Subcommands: `status`

## orama node tls status

Show whether this node holds its certificates, and why not (requires sudo)

```text
orama node tls status
```

```text
Report the certificate of every name this node's Caddy manages: the base
domain and its wildcard, and any site of its own.

For each name it says whether Caddy serves an issued certificate, who issued it
and when it expires. When it does not, it says what the certificate authority
last answered to the order, read from Caddy's log: the ACME problem type (for
example rateLimited), the CA's message and, when the CA gave one, the time it
will accept a new order. A rate limit shows here and nowhere a client can see:
the client's only symptom is "tls: internal error".

Reads the journal of orama-namespace-caddy@index, so it needs root. --json
prints the same as a document with a "names" list.

See orama.network/docs/operator/tls-certificates.
```


## orama node trust

Manage what this node accepts code from, besides its operator's wallet

```text
orama node trust
```

Subcommands: `add-root`

## orama node trust add-root

Adopt a TUF release root on this node (requires sudo)

```text
orama node trust add-root <root.json> [flags]
```

```text
Adopt a TUF release root as /etc/orama/release-root.json.

A node trusts its operator's wallet by default (/etc/orama/archive-signers). A
cluster may also trust a release root: a set of keys whose threshold signature
on release metadata makes an archive installable without the operator building
and signing it. The Orama release root is one; a cluster adopts it by choice
and can drop it by deleting the file.

The root is checked before it is written: well-formed, signed by its own keys at
its threshold, not expired. Adopting a root other than the one already adopted
needs --replace. This command changes this node only; 'orama maint build
--release-root' puts the root in a signed archive, and every node that installs
that archive adopts it.

--rotate adopts the root as the next version of the one adopted: it has to be
signed by the adopted root's keys at their threshold and by its own, exactly as a
client following the release repository checks a rotation. A push of a release
uses it, so a root the operator renewed or rotated reaches this node without
--replace and without anyone's word for it.

Examples:
  sudo orama node trust add-root ./root.json
  sudo orama node trust add-root --rotate ./2.root.json
```

| Flag | Default | Description |
|---|---|---|
| `--replace` | `false` | Replace a different release root that is already adopted |
| `--rotate` | `false` | Adopt the root as the next version of the adopted one, verified against it (a rotation) |


## orama node uninstall

Remove production services (requires sudo)

```text
orama node uninstall
```


## orama node upgrade

Upgrade existing installation (requires sudo)

```text
orama node upgrade [flags]
```

```text
Upgrade the Orama node binary and optionally restart services.
Uses rolling restart with quorum safety to ensure zero downtime.

Run on a node, with sudo, this upgrades that node. Run from your machine with
--env it rolls the nodes of an environment one at a time from the build already
staged on them: that remote mode is replaced by 'orama upgrade' (the newest signed
release of your network's channel) and 'orama maint rollout' (a build of your own),
and prints a notice.
```

| Flag | Default | Description |
|---|---|---|
| `--acme-ca` | — | ACME directory this node's TLS certificates come from, recorded in node.yaml: letsencrypt (production), letsencrypt-staging or an https URL (default: the recorded one) |
| `--delay` | `300` | Seconds a node has to rejoin the cluster after its upgrade before the rollout stops |
| `--env` | — | Target environment for remote rolling upgrade (devnet, testnet) |
| `--force` | `false` | Reconfigure all settings |
| `--nameserver` | `false` | Make this node a nameserver (uses saved preference if not specified) |
| `--node` | — | Upgrade a single node IP only |
| `--public-ip` | — | This node's public IP, recorded as node.public_ip (default: the recorded one, else the source address of the default route) |
| `--restart` | `false` | Automatically restart services after upgrade |
| `--skip-checks` | `false` | Skip minimum resource checks (disk, RAM, CPU) |
| `--yes` | `false` | Execute the rolling upgrade plan (without it the plan is printed and nothing is restarted) |


## orama node wipe

Erase Orama from remote nodes (target-side only)

```text
orama node wipe [flags]
```

```text
Remove all Orama data, services and configuration from remote nodes.
Tor is left installed (its config and state are removed); --nuclear purges it.
The wipe ends by listing what of Orama is still on the machine (LEFTOVER lines) and fails if
anything is; the system accounts are removed only with --nuclear.
Every wipe removes the IPv6 sysctl drop-in and puts IPv6 back to its value from before the install
(enabled when the install recorded none); --nuclear also removes the RAM hygiene drop-in and puts back
the values it recorded.
Every wipe removes the host files the install wrote for Orama alone: the log rotation policy
/etc/logrotate.d/orama (so logrotate.service never fails on the orama user once it is gone) and the
systemd-resolved drop-ins, and links /etc/resolv.conf back to resolved's stub while it is still the
file the install wrote; --nuclear also removes the coredump drop-in and the Node.js runtime.

Target-side only: this says nothing to the cluster. If the node is still a
member, use 'orama remove' instead — otherwise the survivors keep
counting it toward quorum and re-adding its WireGuard peer.

This is a DESTRUCTIVE operation. Use --force to skip confirmation.

Examples:
  orama node wipe --env testnet                      # Wipe every node
  orama node wipe --env testnet --node 1.2.3.4       # Wipe one node
  orama node wipe --env testnet --nuclear             # Also remove shared binaries and accounts
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Target environment (devnet, testnet) [required] |
| `--force` | `false` | Skip confirmation (DESTRUCTIVE) |
| `--node` | — | Public IP of the node to wipe; omit to wipe every node in the environment |
| `--nuclear` | `false` | Also remove shared binaries (rqlited, ipfs, caddy, ...), the Tor package and the system accounts Orama created (orama, orama-*, ntfy) |


## orama nodes

List your nodes across environments

```text
orama nodes [flags]
```

```text
List all nodes owned by your wallet. Queries the network API
with your stored credentials, falling back to nodes.conf.

Requires: orama auth login (for API-based resolution)
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Filter by environment (default: active environment) |


## orama remove

Remove one node from your network, then erase it

```text
orama remove [flags]
```

```text
Take one node out of every store the network keeps, then wipe it.

Before anything changes, remove prints what the removal costs every raft cluster
the node is a voter in (the platform cluster and each namespace it serves) and
refuses if any of them would lose quorum. It also refuses:

  a node in the validator set       erasing it destroys the validator's consensus key
                                    and jails the validator; move the key first, or
                                    pass --drop-validator
  a node with the global layer      it may be registered on the chain with a bond; say
                                    what happens to that: --chain-node-id <id> retires
                                    it, --no-chain leaves it
  a node that holds storage deals   the chain refuses to retire a node whose deals
                                    still reserve bytes

With --chain-node-id the node is retired on the chain first (MsgRetireNode, signed
by your RootWallet, sent through a surviving node's chain over SSH): its service keys
are revoked and its bonds start to unbond. If the chain or the RootWallet refuses,
nothing has been removed. Then the node leaves the raft configuration, an eviction
tombstone keeps anything from re-adding it, its mesh address, nameserver slot,
namespace memberships, port blocks and TURN and SFU allocations are released, its DNS
records are purged, and the machine is wiped.

Use --offline when the machine is already gone: the removal is done from the
survivors and nothing is attempted on the target. It cannot be asked whether it
was registered on the chain, so its registration is left alone and the plan says
so; --chain-node-id retires it through a surviving node. Every step is keyed on the node and
safe to repeat, so a removal that failed part way is finished by running it again.

--dry-run prints the quorum arithmetic and every step, changing nothing. This is
DESTRUCTIVE: it asks you to type 'yes' unless --yes is given.

Examples:
  orama remove --node 203.0.113.9 --dry-run
  orama remove --node 203.0.113.9
  orama remove --node 203.0.113.9 --chain-node-id node-9
  orama remove --node 203.0.113.9 --offline
```

| Flag | Default | Description |
|---|---|---|
| `--chain-id` | — | The chain id you expect, for a network that is on no registry network (one from the registry already names it); the wallet signs for no other chain |
| `--chain-node-id` | — | The node's id in the chain's node registry: retire it there before removing it |
| `--drop-validator` | `false` | Remove the node although it signs for the validator set; its consensus key is erased with it |
| `--dry-run` | `false` | Print the quorum impact and every step, change nothing |
| `--env` | — | Network the node belongs to (default: the active one) |
| `--no-chain` | `false` | Leave the node's chain registration alone (its bonds stay locked until you retire it) |
| `--node` | — | Public IP of the node to remove [required] |
| `--nuclear` | `false` | When wiping, also remove the shared binaries, the Tor package and the system accounts Orama created |
| `--offline` | `false` | The machine is already gone: retire it from the cluster only, do not wipe it |
| `--yes` | `false` | Do not ask for confirmation (DESTRUCTIVE) |


## orama setup

Join an Orama network: turn fresh VPSes into nodes

```text
orama setup [ip ...] [flags]
```

```text
Turn fresh VPSes into nodes of an Orama network, in one command.

For each machine setup gives your RootWallet an SSH key (and pins the machine's host key),
checks the hardware against what the machine will run, installs the signed release of the
network's channel (verified against the release root the network pins), installs the cluster
node and, beside it, the global layer: the chain (it joins by state sync from two seeds that
must agree), public storage and its provider, and a Tor relay (the network pins the Tor network file its
relays join; --tor-network gives another, --no-relay leaves the relay out). Then it registers your operator, each node, its bonds and its storage capacity on the
chain and creates your validator, signing every transaction with your RootWallet. Nodes are
restarted one at a time, each waiting until it carries its share of the cluster again.

Each machine downloads the release from the release repository itself, up to eight at a time, and checks the
file against the length and SHA-256 of the release metadata that setup verified here; the archive does not pass
through this computer. A machine that cannot reach the repository fails the run; --upload-release downloads the
archive here and uploads it to each machine over SSH, one after the other, for such machines.

The first machine creates the cluster; the others join it. Running setup again with more
addresses adds nodes to the same cluster, and a machine that already has a step does not get
it again, so a run that stopped can be run again as it was.

With no addresses and no --yes, on a terminal, setup asks for everything. With --yes it asks
nothing: give the addresses, --name, and a --host-key for each machine (the fingerprint your
provider's console shows; setup never trusts a host key it was not given).

The operator account needs ORAMA for the bonds and for the validator's 1,000 ORAMA self-bond.
On a network with a faucet and a node of it in your CLI configuration it is requested; otherwise
setup stops, says how much to send and to which address, and resumes when you run it again.

--create-network <name> makes a new network instead of joining one. Every machine is a bootstrap
validator of it: setup installs the cluster and the global layer on all of them, makes each
machine's chain keys, builds the genesis on the first machine from all the keys, gives it to
the others, starts the chains one after the other, waits for blocks, and then registers your
operator and the nodes as it does for a join. It writes networks/<name>/ (manifest, genesis,
release root) to --publish-dir and prints what to do to publish it. --chain-id is the chain's
id: a test network's is of the form orama-[<name>-]testnet-<id>, orama-[<name>-]devnet-<id> or orama-[<name>-]localnet-<id> (an id that only contains one of those words is a production id); any other is a production id, which
setup refuses: it creates test networks only, because it keeps each seat's key in an unencrypted test keyring
(a production network needs seat accounts held by the RootWallet, which is not built). --release-root is the release-root.json the network's
releases are verified against. A network that was announced in the registry before it was created (orama maint network announce)
supplies its chain id, release repository, channel, minimum version, seeds, faucet and release root, so those flags can be left out;
a flag overrides the announcement, and the genesis built here is published over it. Joining a network that is only announced is refused. Running it again with the same machines resumes: a machine that
has its keys keeps them, and a genesis the machines carry is kept (--force-new-genesis builds a
new one, and only while no chain has run).

A creation also makes the network its own Tor network, unless --tor-authorities 0: the first N machines
(default 3, when there are at least 3 machines and neither --tor-network nor --no-relay is given; fewer
than 3 is refused) each run a directory authority, whose keys are made on that machine and never leave it,
and the others run relays (exits with --exit). After the chains run and the nodes are registered (the
relay machines with the relay role and without the relay's key, which Tor makes on its first start), setup
installs and starts the authorities within minutes of each other, waits until the machines' own tor holds a
consensus signed by a majority of the authorities that lists them (an authority casts no Running vote for
30 minutes after it starts, so that takes about an hour), binds each relay's key to its node and registers
the relay with x/relay, then leaves the bootstrap mode: the authorities
are restarted one at a time, each only when its own machine allows it (30 minutes after the previous one
started), then the relays. The step takes two hours or more; a run that stops resumes where it stopped.
networks/<name>/ gets the tor-network.json the manifest pins, which every machine that joins installs.

--cluster-only installs the cluster node alone (2 vCPU, 2 GiB, 10 GiB free). The full profile
needs 4 vCPU, 8 GiB and 80 GiB free plus the storage you offer. Nothing is installed on any
machine until every machine passes.
```

| Flag | Default | Description |
|---|---|---|
| `--acme-ca` | — | ACME directory for the cluster's certificates: letsencrypt, letsencrypt-staging or an https URL |
| `--allow-quorum-loss` | `false` | Restart a cluster of fewer than three voters with --force when the global layer is installed: the cluster is unavailable while the node restarts (without it, a run with --yes stops there) |
| `--asn` | `0` | Autonomous system number to declare for the nodes (default: looked up from the address; 0 leaves it undeclared) |
| `--bootstrap-key` | — | A private key that opens the machines today (key-only images); used once to install the RootWallet key, never stored |
| `--chain-id` | — | With --create-network: the chain id (default: the announced network's). A test network's is of the form orama-[&lt;name>-]testnet-&lt;id>, orama-[&lt;name>-]devnet-&lt;id> or orama-[&lt;name>-]localnet-&lt;id> (an id that only contains one of those words is a production id); any other id is a production one, which setup refuses (it creates test networks only) |
| `--channel` | — | With --create-network: the release channel, nightly, main or dev/&lt;branch> (default nightly, main for a production chain id) |
| `--cluster-only` | `false` | Install the cluster node only, without the chain, storage or relay |
| `--contact` | — | Where an abuse complaint about the relay goes (default: your operator account) |
| `--create-network` | — | Create a network of this name instead of joining one: the machines are its bootstrap validators |
| `--domain` | — | Base domain of a cluster of your own: setup prints the NS and glue records to create, then waits until they resolve and the cluster has a certificate |
| `--env` | — | CLI environment to record the cluster under (default: the active one on this network, else &lt;network>-&lt;name>) |
| `--exit` | `false` | Make the relay an exit relay: other people's traffic leaves from your IP address. Needs the network's Tor network file and --yes |
| `--force-new-genesis` | `false` | With --create-network: build a new genesis although the machines carry one. Refused once a chain has run |
| `--host-key` | — | Expected SSH host-key fingerprint, SHA256:..., for a single machine or &lt;ip>=SHA256:... for each (repeatable) |
| `--ip` | — | Public IPv4 address of a machine, or &lt;user>@&lt;address> to log in to that machine as &lt;user> instead of --user (repeatable; the addresses can also be given as arguments) |
| `--min-version` | — | With --create-network: the oldest orama version that may join, X.Y.Z (default: this CLI's version) |
| `--name` | — | Node name, the node's id on the chain; several machines are named &lt;name>, &lt;name>-2, ... (required unless --cluster-only) |
| `--network` | — | Network to join: a name from `orama network list` (default: the active network, or the only one that runs a chain) |
| `--no-faucet` | `false` | With --create-network: leave the test-network faucet out of the genesis |
| `--no-relay` | `false` | Run no relay though the network pins a Tor network file |
| `--no-validator` | `false` | Do not create a validator (and do not bond the 1,000 ORAMA self-bond) |
| `--password` | `false` | Log in with the password in your RootWallet vault login for the address (rw vault add &lt;ip>), never from the command line |
| `--publish-dir` | — | With --create-network: where networks/&lt;name>/ is written (default ./networks) |
| `--release-repo` | — | With --create-network: the https base URL of the release repository (default https://releases.orama.network) |
| `--release-root` | — | With --create-network: the release-root.json the network's releases are verified against; the manifest pins its digest (default: the announced network's) |
| `--seed` | — | With --create-network: a seed DNS name (repeatable; default ns1.&lt;name>.orama.network ..., one per machine; with --domain, the cluster's nameservers ns&lt;N>.&lt;domain>, one per machine up to 13) |
| `--storage-gb` | `0` | Public storage each node offers, in GB (default 50); counts towards the disk floor |
| `--tor-authorities` | `0` | With --create-network: how many of the machines, the first ones, run a directory authority of the network's own Tor network, the others running relays; 0 makes no Tor network. Default 3 when there are at least 3 machines (and no --tor-network or --no-relay); fewer than 3 is refused. The step takes two hours or more |
| `--tor-network` | — | A tor-network.json to give the relays instead of the one the network pins (a network that pins none runs a relay only with it; a creation makes its own Tor network instead, see --tor-authorities) |
| `--upload-release` | `false` | Download the release on this computer and upload it to each machine over SSH, one after the other, instead of each machine downloading it from the release repository (for machines that cannot reach the repository) |
| `--user` | `root` | SSH login on the machines |
| `-y`, `--yes` | `false` | Ask nothing: use the answers given as flags (every machine needs a --host-key) |


## orama ssh

SSH into a node

```text
orama ssh <ip-or-hostname> [-- command] [flags]
```

```text
SSH into a node by IP address or hostname.
Resolves the SSH key from rootwallet automatically.

The node's host key must already be pinned in ~/.orama/known_hosts, where
'orama node setup' writes it. A host with no pinned key is refused, never
trusted on first use, and a key that differs from the pinned one is refused.

Pass a command after the IP to run it non-interactively:
  orama ssh 1.2.3.4 'sudo systemctl status orama-node'
```

| Flag | Default | Description |
|---|---|---|
| `--env` | — | Environment to search (default: active) |


## orama status

Show your nodes, the cluster, the chain and your account

```text
orama status [flags]
```

```text
Show everything about your nodes in one place: each node's cluster health and chain
(height, syncing, validator), the verdict with what to do, and your account on the chain (earnings,
spendable balance, bond, and each Tor relay registered on your nodes with the last payout it received):
the one 'orama setup' registered your nodes under, or the one --operator names.

In a terminal this is the live view (tab/1-0 switch tabs, ? help, q quit). Piped or with --once it
prints one table; --json prints a document whose "healthy" is true only when the verdict is
operational, every node is healthy and every node's chain answers and has caught up.

Each node's UPDATE column (and "update" in --json) is what its auto-update agent found: a newer
release, the channel, what the cluster's policy does with it (notify, upgrade, refuse) and why. A chain
upgrade the chain has scheduled is listed with its height, the blocks and time left and whether the
node has its binary staged ("chain.upgrade" in --json); neither makes "healthy" false.

The cluster data comes from the gateway's operator telemetry API (sign in with 'orama auth login');
--ssh reads every node over SSH instead, for when no gateway answers. The subcommands show one
aspect at a time.
```

| Flag | Default | Description |
|---|---|---|
| `--config` | — | With --ssh: read nodes from this file instead of resolving them |
| `--env` | — | Environment (default: active) |
| `--interval` | `5s` | How often the live view refreshes |
| `--node` | — | Show only this node (public IP or WireGuard IP) |
| `--once` | `false` | Print one table and exit, even in a terminal |
| `--operator` | — | Your operator account (orama1...), to show its earnings, balance, bond and relays |
| `--ssh` | `false` | Collect over SSH from every node instead of the gateway API (break-glass) |

Subcommands: `alerts`, `chain`, `cluster`, `dns`, `mesh`, `namespaces`, `node`, `report`, `service`, `traffic`

## orama status alerts

Alerts, most severe first, with what to do (one-shot)

```text
orama status alerts
```


## orama status chain

Orama L1 height, sync and validators (one-shot)

```text
orama status chain
```


## orama status cluster

Verdict, components and a row per node (one-shot)

```text
orama status cluster
```


## orama status dns

DNS and TLS health of the nameservers (one-shot)

```text
orama status dns
```


## orama status mesh

WireGuard mesh connectivity (one-shot)

```text
orama status mesh
```


## orama status namespaces

Namespace health across nodes (one-shot)

```text
orama status namespaces
```


## orama status node

Per-node health details (one-shot)

```text
orama status node
```


## orama status report

Full cluster report as JSON (one-shot)

```text
orama status report
```


## orama status service

Service status across the cluster (one-shot)

```text
orama status service
```


## orama status traffic

Gateway requests, errors and latency (one-shot)

```text
orama status traffic
```


## orama storage

Storage deals on the Orama chain

```text
orama storage
```

Subcommands: `accept`, `create`, `decline`, `extend`, `get`, `grant`, `open`, `prove`, `put`, `repair`, `revoke`, `rewrap`, `seal`

## orama storage accept

Accept an assigned storage slot

```text
orama storage accept [flags]
```

```text
Accept one slot of a deal. The signer is the node's hot key. Without --node the command prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--deal-id` | `0` | Deal id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Signing account (orama1...) [required] |
| `--slot` | `0` | Slot index |


## orama storage create

Open a private or public-pin storage deal

```text
orama storage create [flags]
```

```text
Open a PRIVATE or PUBLIC_PIN deal.

The command does not encrypt the bytes and does not upload them. Each --piece
is a 32-byte root and a byte count, written as <64 hex chars>:<bytes>. Leaf
counts follow the 1024-byte piece rule. The root is not checked against the
bytes. A private deal needs one piece per replica. A public-pin deal needs
exactly one piece. Archive deals are refused. Without --node the command
prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--class` | — | private or public-pin [required] |
| `--duration-epochs` | `0` | Deal length in epochs [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--granter` | — | Account whose deal allowance pays, when the signer is the grantee |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--nonce` | — | 32-byte deal nonce hex [required] |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--piece` | — | Piece as &lt;64-hex-root>:&lt;bytes> [required] |
| `--price` | — | Price per epoch per replica, in norama [required] |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--repair-delegate` | — | Repair delegate id |
| `--replicas` | `3` | Replica count |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Signing account (orama1...) [required] |


## orama storage decline

Decline an assigned storage slot

```text
orama storage decline [flags]
```

```text
Decline one slot of a deal. The signer is the node's hot key. Without --node the command prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--deal-id` | `0` | Deal id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--reason` | — | Why the slot is declined |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Signing account (orama1...) [required] |
| `--slot` | `0` | Slot index |


## orama storage extend

Add epochs to a storage deal

```text
orama storage extend [flags]
```

```text
Add epochs to a user deal. Without --node the command prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--deal-id` | `0` | Deal id [required] |
| `--extra-epochs` | `0` | Epochs to add [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Signing account (orama1...) [required] |


## orama storage get

Fetch and open a private file from its providers

```text
orama storage get [flags]
```

```text
Fetch the first slot of a deal that a provider serves with the on-chain
piece root, strip its slot layer, and decrypt it. A wrong storage key or repair seed
fails and writes nothing.
```

| Flag | Default | Description |
|---|---|---|
| `--deal-id` | `0` | Deal id |
| `--out` | — | Plaintext output file |
| `--repair-seed-file` | — | File holding the repair seed, hex, at least 32 bytes, mode 0600 |
| `--rpc` | — | oramad CometBFT RPC, for example http://127.0.0.1:31001 |
| `--storage-key-file` | — | File holding the orama-storage-v1 key from RootWallet (never the wallet seed), hex, exactly 32 bytes, mode 0600 |


## orama storage grant

Grant a cluster a capped deal allowance

```text
orama storage grant [flags]
```

```text
Grant a deal allowance to another account.

The grant is not SDK authz. It caps spend, piece size, duration, and replica
count. Without --node the command prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--expiry-epoch` | `0` | Epoch after which the grant is dead |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--grantee` | — | Grantee account (orama1...) [required] |
| `--max-duration-epochs` | `0` | Longest deal the grant allows [required] |
| `--max-piece-bytes` | `0` | Largest piece the grant allows [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--period-epochs` | `0` | Epochs in one spend period |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--replicas` | `3` | Exact replica count a deal must use |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Granter account (orama1...) [required] |
| `--spend-limit` | — | Spend limit in norama [required] |


## orama storage open

Open one sealed storage slot

```text
orama storage open [flags]
```

```text
Open one slot file written by seal.

A wrong storage key, repair seed, or slot fails and writes nothing.
```

| Flag | Default | Description |
|---|---|---|
| `--in` | — | Sealed slot file |
| `--nonce` | — | Deal nonce, 32 bytes hex |
| `--out` | — | Plaintext output file |
| `--repair-seed-file` | — | File holding the repair seed, hex, at least 32 bytes, mode 0600 |
| `--slot` | `0` | Slot index |
| `--storage-key-file` | — | File holding the orama-storage-v1 key from RootWallet (never the wallet seed), hex, exactly 32 bytes, mode 0600 |


## orama storage prove

Submit storage challenge proofs

```text
orama storage prove [flags]
```

```text
Submit one or more challenge proofs for a storage node.

--file is a JSON array. Each object has deal_id, slot, leaf_index, leaf,
and siblings. leaf and siblings are hex. The leaf is 1024 bytes and each
sibling is 32 bytes. The command does not choose the challenged leaf and
does not read the stored piece. Without --node it prints the sign document
and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--file` | — | JSON file of proofs [required] |
| `--gas` | `0` | Gas limit [required] |
| `--id` | — | Node id [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Hot key account (orama1...) [required] |


## orama storage put

Upload sealed slots to the providers a deal assigned

```text
orama storage put [flags]
```

```text
Upload the slot-N files written by seal to the providers the chain assigned.

The deal must already exist (orama storage create, with the roots seal printed).
Every file's piece root is checked against its slot on chain before any byte
is sent, so a wrong file or a wrong deal uploads nothing. The command waits
for each slot to be assigned and for its provider to accept the root. The
provider endpoint is the node's first http(s) endpoint in x/nodes.
```

| Flag | Default | Description |
|---|---|---|
| `--deal-id` | `0` | Deal id |
| `--dir` | — | Directory holding slot-N files from seal |
| `--rpc` | — | oramad CometBFT RPC, for example http://127.0.0.1:31001 |
| `--wait` | `5m0s` | How long to wait for assignment and acceptance |


## orama storage repair

Restore the replicas a deal lost, from the providers that still hold them

```text
orama storage repair [flags]
```

```text
Rebuild every slot of a deal that the chain assigned to a new provider but that
no provider has accepted yet, using the repair seed.

For each such slot the command fetches an accepted replica from another
provider, checks it against its on-chain piece root, strips that slot's outer
layer, applies the new slot's layer, checks the result against the new slot's
on-chain root, and uploads it to the new provider. The plaintext is never
recovered. A repair seed that is not the deal's makes the result miss the
root, and nothing is uploaded.

A deal that names a repair delegate is repaired by the delegate while you are
away. Without one, the deal runs with fewer replicas until you run this.
```

| Flag | Default | Description |
|---|---|---|
| `--deal-id` | `0` | Deal id |
| `--repair-seed-file` | — | File holding the repair seed, hex, at least 32 bytes, mode 0600 |
| `--rpc` | — | oramad CometBFT RPC, for example http://127.0.0.1:31001 |
| `--wait` | `5m0s` | How long to wait for each new provider to read its assignment |


## orama storage revoke

Revoke a deal allowance

```text
orama storage revoke [flags]
```

```text
Revoke a deal allowance. Without --node the command prints the sign document and does not submit it.
```

| Flag | Default | Description |
|---|---|---|
| `--account-number` | `0` | Account number, when not read from --node |
| `--chain-id` | — | Chain id [required] |
| `--fee` | — | Fee in norama [required] |
| `--gas` | `0` | Gas limit [required] |
| `--grantee` | — | Grantee account (orama1...) [required] |
| `--node` | — | Chain REST API, for example http://127.0.0.1:31003; the command returns once the transaction is in a block, and fails if the block refuses it |
| `--onion-network` | — | Start a Tor client for this Orama Tor network file (tor-network.json) and submit through it; without --onion a validator onion from the file is picked at random ($ORAMA_ONION_NETWORK) |
| `--onion-socks` | — | Tor SOCKS5 address for --onion, a loopback host:port (default 127.0.0.1:9050, $ORAMA_ONION_SOCKS) |
| `--onion-tor` | `tor` | The tor binary --onion-network starts |
| `--onion` | — | Submit through this validator onion service (addr.onion[:port]) over Tor instead of --node; never falls back to the clearnet ($ORAMA_CHAIN_ONION) |
| `--pubkey` | — | Compressed secp256k1 pubkey hex of the signing account |
| `--sequence` | `0` | Account sequence, when not read from --node |
| `--signer` | — | Granter account (orama1...) [required] |


## orama storage rewrap

Rebuild one storage slot from another slot's ciphertext

```text
orama storage rewrap [flags]
```

```text
Turn one sealed slot into another slot of the same deal.

The command uses the repair seed only. It does not recover the plaintext
and it does not upload the result.
```

| Flag | Default | Description |
|---|---|---|
| `--from` | `0` | Slot the input file belongs to |
| `--in` | — | Source slot file |
| `--nonce` | — | Deal nonce, 32 bytes hex |
| `--out` | — | Destination slot file |
| `--repair-seed-file` | — | File holding the repair seed, hex, at least 32 bytes, mode 0600 |
| `--to` | `0` | Slot to write |


## orama storage seal

Seal a file into one ciphertext per storage slot

```text
orama storage seal [flags]
```

```text
Seal a private file before a storage deal.

The file key is wrapped under the owner's orama-storage-v1 key from RootWallet. Each slot gets a different
ciphertext. The command writes slot-N files and prints each piece root.
It does not upload the bytes and it does not submit a deal.
```

| Flag | Default | Description |
|---|---|---|
| `--in` | — | Plaintext file |
| `--nonce` | — | Deal nonce, 32 bytes hex |
| `--out-dir` | — | Directory for slot-N files |
| `--repair-seed-file` | — | File holding the repair seed, hex, at least 32 bytes, mode 0600 |
| `--replicas` | `3` | Number of slots, 1 to 32 |
| `--storage-key-file` | — | File holding the orama-storage-v1 key from RootWallet (never the wallet seed), hex, exactly 32 bytes, mode 0600 |


## orama upgrade

Upgrade your nodes to the newest signed release of the network's channel

```text
orama upgrade [flags]
```

```text
Find the newest release of your network's channel, show what each node runs and
what it will go through, and after you confirm, upgrade the nodes one at a time.

The release comes from the release repository and channel the network publishes
(orama network list). Its signed metadata is verified here against the release
root built into this CLI; the archive is not downloaded here. Each node downloads
the archive from the repository itself, checks it against the signed length and
SHA-256, and verifies it again against the release root it adopted, refusing a
release that does not match, before it replaces a file. --upload-release
downloads and verifies the archive here instead and uploads it to each node over
SSH, for nodes that cannot reach the repository.

The plan lists each node with the release it runs now (from the cluster's
telemetry, as 'orama status' shows it), the release it will run, and its place in
the rollout: followers first, nameservers spread so the zone keeps answering, the
raft leader last. A node that already runs the release is left alone
(--reinstall puts it in place again); a node that runs a newer one is never
downgraded.

The release is staged on every node first, which restarts nothing. Then each node
in turn is upgraded and restarted, and the next one starts only when that node is
healthy and carrying its share of the cluster again: never two RQLite voters at once.
A node that has the global layer also refreshes it right after its own upgrade:
the global binaries are replaced, and the services that run them are restarted.
The chain binary (oramad) is staged for cosmovisor only when the release carries
another one and the chain has a governed upgrade scheduled; otherwise the running
oramad is kept and the output says so.

--node upgrades one node. --dry-run prints the plan and stops.

Examples:
  orama upgrade --dry-run          # What would change
  orama upgrade                    # Show the plan, ask, then roll
  orama upgrade --yes              # Roll without asking
  orama upgrade --node 203.0.113.7
  orama upgrade --upload-release   # Nodes that cannot reach the repository
```

| Flag | Default | Description |
|---|---|---|
| `--delay` | `300` | Seconds a node has to rejoin the cluster after its upgrade before the rollout stops |
| `--dry-run` | `false` | Print the plan and stop; nothing is staged or restarted |
| `--env` | — | Network to upgrade (default: the active one) |
| `--node` | — | Upgrade only the node with this public IP |
| `--reinstall` | `false` | Put the release in place again on nodes that already run it |
| `--ssh` | `false` | Read what the nodes run over SSH instead of the gateway's telemetry |
| `--upload-release` | `false` | Download the release on this computer and upload it to each node over SSH, instead of each node downloading it from the release repository (for nodes that cannot reach the repository) |
| `--yes` | `false` | Do not ask for confirmation |


## orama version

Show version information

```text
orama version
```

