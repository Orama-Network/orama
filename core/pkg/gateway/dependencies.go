package gateway

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/config"
	"github.com/Orama-Network/orama/core/pkg/constants"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	serverlesshandlers "github.com/Orama-Network/orama/core/pkg/gateway/handlers/serverless"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/ipfs"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"github.com/Orama-Network/orama/core/pkg/olric"
	"github.com/Orama-Network/orama/core/pkg/privhelper"
	"github.com/Orama-Network/orama/core/pkg/pubsub"
	"github.com/Orama-Network/orama/core/pkg/push"
	pushcreds "github.com/Orama-Network/orama/core/pkg/push/credentials"
	pushapns "github.com/Orama-Network/orama/core/pkg/push/providers/apns"
	pushexpo "github.com/Orama-Network/orama/core/pkg/push/providers/expo"
	pushntfy "github.com/Orama-Network/orama/core/pkg/push/providers/ntfy"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/secrets"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/serverless/hostfunctions"
	"github.com/Orama-Network/orama/core/pkg/serverless/persistent"
	"github.com/Orama-Network/orama/core/pkg/serverless/triggers"
	"github.com/Orama-Network/orama/core/pkg/serverless/wsbridge"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	_ "github.com/rqlite/gorqlite/stdlib"
)

const (
	olricInitMaxAttempts    = 5
	olricInitInitialBackoff = 500 * time.Millisecond
	olricInitMaxBackoff     = 5 * time.Second
)

// Dependencies holds all service clients and components required by the Gateway.
// This struct encapsulates external dependencies to support dependency injection and testability.
type Dependencies struct {
	// Client is the network client for P2P communication
	Client client.NetworkClient

	// RQLite database dependencies
	SQLDB     *sql.DB
	ORMClient rqlite.Client
	ORMHTTP   *rqlite.HTTPGateway

	// GlobalORMClient reads the MAIN cluster's RQLite. On a namespace gateway
	// ORMClient points at that namespace's own isolated RQLite, which does not
	// carry cluster-wide tables such as dns_nodes — those live only in the main
	// cluster (see pkg/node/dns_registration.go). Any handler that needs
	// cluster topology must use this handle, not ORMClient (bugboard #153),
	// and so must the auth service's compare-and-swap writes (bindAuthRegistry).
	// On the main gateway (no separate GlobalRQLiteDSN) it IS ORMClient.
	GlobalORMClient rqlite.Client
	globalSQLDB     *sql.DB

	// Olric distributed cache client
	OlricClient *olric.Client

	// OlricCurrent is the client connected right now. Long-lived consumers read
	// it per operation; the gateway updates it as the supervisor drops and
	// reconnects the client.
	OlricCurrent *olric.Current

	// OlricServers is the address list the client above was built from, after
	// discovery has resolved it. The supervisor needs it to reconnect: it used
	// to fall back to a hardcoded "localhost:10102" while the resolved list
	// sat here unused, so a namespace gateway that lost its cache tried to
	// reconnect to the wrong place for ever.
	OlricServers []string

	// IPFS storage client
	IPFSClient ipfs.IPFSClient

	// Serverless function engine components
	ServerlessEngine   *serverless.Engine
	ServerlessRegistry *serverless.Registry
	ServerlessInvoker  *serverless.Invoker
	// HostFuncs is what functions call the platform through; the gateway wires
	// into it what only it can build (the WebRTC controller).
	HostFuncs          *hostfunctions.HostFunctions
	ServerlessWSMgr    *serverless.WSManager
	ServerlessHandlers *serverlesshandlers.ServerlessHandlers

	// WSSessions is every token-authorized WebSocket open on this gateway. The
	// function and pubsub handlers register their sockets in it, and the
	// gateway's sweeper closes the ones whose token expires or is revoked.
	WSSessions *wssession.Registry

	// PubSub trigger dispatcher (used to wire into PubSubHandlers)
	PubSubDispatcher *triggers.PubSubDispatcher

	// Cron trigger store + scheduler. The scheduler is started by gateway
	// lifecycle code after Dependencies is constructed; Stop is called
	// during shutdown.
	CronTriggerStore *triggers.CronTriggerStore
	CronScheduler    *triggers.CronScheduler

	// PersistentWSManager tracks long-lived WS function instances.
	// Used by the WS handler when fn.WSPersistent=true; nil = disabled.
	PersistentWSManager *persistent.Manager

	// WSBridge wires PubSub topics directly to WS clients on this gateway.
	// Used by the ws_pubsub_bridge host function. Nil = disabled.
	WSBridge *wsbridge.Bridge

	// Push notification dispatcher (legacy single-tier; nil when push
	// isn't configured at all). When PushManager is also set, send paths
	// route through the manager instead so per-namespace config wins.
	PushDispatcher  *push.PushDispatcher
	PushDeviceStore push.PushDeviceStore

	// PushManager wraps the device store + per-namespace config store so
	// tenants self-serve their push provider config via PUT /v1/push/config.
	// Nil when push subsystem isn't initialized (cluster secret missing).
	// When set, this is the canonical send path; PushDispatcher is the
	// fallback used only if Manager is somehow missing.
	PushManager     *push.Manager
	PushConfigStore push.ConfigStore

	// PushCredentialsManager owns per-namespace, per-provider push
	// credentials (feature #72). Used by provider factories to look up
	// the right credential at send time, and by the HTTP credentials
	// handlers for tenant self-service PUT/GET/DELETE. Nil when the
	// cluster secret is unavailable.
	PushCredentialsManager *pushcreds.Manager

	// Authentication service
	AuthService *auth.Service

	// EncHolder is the process-wide encryption root for stored ciphertext.
	EncHolder  *secrets.Holder
	SecretsMgr *hostfunctions.DBSecretsManager
}

// NewDependencies creates and initializes all gateway dependencies based on the provided configuration.
// It establishes connections to RQLite, Olric, IPFS, initializes the serverless engine, and creates
// the authentication service.
func NewDependencies(logger *logging.ColoredLogger, cfg *Config) (*Dependencies, error) {
	deps := &Dependencies{WSSessions: wssession.NewRegistry(logger.Logger), OlricCurrent: &olric.Current{}}

	// Before anything connects: a gateway that cannot write its own state
	// cannot hold a signing key, and there is no point dialling the cluster to
	// find that out.
	if err := ensureStateDir(cfg.StateDir); err != nil {
		return nil, err
	}

	// Create and connect network client
	logger.ComponentInfo(logging.ComponentGeneral, "Building client config...")
	cliCfg, err := gatewayClientConfig(cfg)
	if err != nil {
		return nil, err
	}

	logger.ComponentInfo(logging.ComponentGeneral, "Creating network client...")
	c, err := client.NewClient(cliCfg)
	if err != nil {
		logger.ComponentError(logging.ComponentClient, "failed to create network client", zap.Error(err))
		return nil, err
	}

	logger.ComponentInfo(logging.ComponentGeneral, "Connecting network client...")
	if err := c.Connect(); err != nil {
		logger.ComponentError(logging.ComponentClient, "failed to connect network client", zap.Error(err))
		return nil, err
	}

	logger.ComponentInfo(logging.ComponentClient, "Network client connected",
		zap.String("namespace", cliCfg.AppName),
		zap.Int("peer_count", len(cliCfg.BootstrapPeers)),
	)

	deps.Client = c

	if err := initializeBackends(logger, cfg, deps, c); err != nil {
		return nil, err
	}
	return deps, nil
}

// The backend initialisers that dial live services. Variables only so a test
// can stand them in and exercise initializeBackends without an Olric or IPFS
// cluster; nothing else assigns them.
var (
	initOlricBackend = initializeOlric
	initIPFSBackend  = initializeIPFS
)

// initializeBackends opens the database handles, the cache and storage
// clients, and the serverless engine together with the auth service.
//
// The serverless step is fatal. It builds the auth service, and a gateway
// without one serves /health 200 while every /v1/auth/* route is a 404, no
// function runs and no WebSocket token is checked — which is how a gateway
// that could not write its signing key used to look healthy for days.
func initializeBackends(logger *logging.ColoredLogger, cfg *Config, deps *Dependencies, c client.NetworkClient) error {
	// Open the RQLite handles. This does not touch the database — sql.Open is
	// lazy — so it fails only on a malformed DSN. The work that needs a live
	// database (the leader wait, the migrations, the schema contract) runs in
	// the background afterwards and is reported as readiness; see readiness.go.
	if err := initializeRQLite(logger, cfg, deps); err != nil {
		logger.ComponentWarn(logging.ComponentGeneral,
			"RQLite handles could not be opened; the gateway will report itself as starting until this is fixed",
			zap.Error(err))
	}

	// Initialize Olric cache client (with retry and background reconnection)
	initOlricBackend(logger, cfg, deps, c)

	// Initialize IPFS Cluster client. A gateway that cannot derive the cluster
	// credentials would otherwise serve storage routes that fail closed only
	// after a request, and the log line was the only record of why.
	if err := initIPFSBackend(logger, cfg, deps); err != nil {
		return fmt.Errorf("gateway %s cannot start: %w", cfg.ClientNamespace, err)
	}

	// Initialize serverless function engine (requires RQLite and IPFS)
	if err := initializeServerless(logger, cfg, deps, c); err != nil {
		return fmt.Errorf("gateway %s cannot start: the serverless engine and auth service failed to initialize: %w",
			cfg.ClientNamespace, err)
	}
	// Every route the gateway wires after this dereferences the auth service
	// (the deployment workload-token minter, the audit trail); a nil one here
	// is a bug in initializeServerless, not a degraded mode.
	if deps.AuthService == nil {
		return fmt.Errorf("gateway %s cannot start: serverless initialized without an auth service", cfg.ClientNamespace)
	}
	return nil
}

// initializeRQLite sets up the RQLite database connection and ORM HTTP gateway
func initializeRQLite(logger *logging.ColoredLogger, cfg *Config, deps *Dependencies) error {
	logger.ComponentInfo(logging.ComponentGeneral, "Initializing RQLite ORM HTTP gateway...")
	// Inject basic auth credentials into DSN if available
	dsn, err := credentialedRQLiteDSN(cfg.RQLiteDSN, cfg.RQLiteUsername, cfg.RQLitePassword)
	if err != nil {
		return fmt.Errorf("rqlite_dsn: %w", err)
	}
	dsn = appendRQLiteQueryParams(dsn)
	db, err := sql.Open("rqlite", dsn)
	if err != nil {
		return fmt.Errorf("failed to open rqlite sql db at %s: %s", rqlite.RedactDSN(dsn), rqlite.RedactError(err, dsn))
	}

	// Configure connection pool with proper timeouts and limits
	db.SetMaxOpenConns(25)                 // Maximum number of open connections
	db.SetMaxIdleConns(5)                  // Maximum number of idle connections
	db.SetConnMaxLifetime(5 * time.Minute) // Maximum lifetime of a connection
	db.SetConnMaxIdleTime(2 * time.Minute) // Maximum idle time before closing

	deps.SQLDB = db
	// The ORM client needs its native *gorqlite.Connection: atomic batches
	// (push topic registration among them) have no other path. A gateway
	// without one served every route and answered 500 on each batch, so a
	// failed dial stops the boot instead (bugboard #435).
	orm, err := rqlite.NewClientWithDSN(db, dsn)
	if err != nil {
		return fmt.Errorf("rqlite native connection (atomic batches need it): %w", err)
	}
	deps.ORMClient = orm
	deps.ORMHTTP = rqlite.NewHTTPGateway(orm, "/v1/db")
	// Set a reasonable timeout for HTTP requests (30 seconds)
	deps.ORMHTTP.Timeout = 30 * time.Second

	logger.ComponentInfo(logging.ComponentGeneral, "RQLite ORM HTTP gateway ready",
		zap.String("dsn", rqlite.RedactDSN(dsn)),
		zap.String("base_path", "/v1/db"),
		zap.Duration("timeout", deps.ORMHTTP.Timeout),
	)

	// Open a SECOND handle on the MAIN cluster when this is a namespace gateway
	// (bugboard #153). Cluster-wide tables — dns_nodes above all — are written
	// only to the main RQLite by pkg/node/dns_registration.go; a namespace
	// RQLite has the table (core migrations run there) but never a single row.
	// Reading topology off ORMClient therefore returns an empty set that looks
	// exactly like "no nodes", which is how immediate storage eviction silently
	// fanned out to nobody on every call. Resolve the handle ONCE, explicitly,
	// so no caller has to know which RQLite it is talking to.
	if err := initializeGlobalRQLite(logger, cfg, deps); err != nil {
		return err
	}

	return nil
}

// errSchemaContract marks the one schema failure retrying cannot fix: a leader
// answered, and the applied version is below what this binary requires.
var errSchemaContract = errors.New("schema contract violation")

// isSchemaContractViolation reports whether err is that failure.
func isSchemaContractViolation(err error) bool {
	return errors.Is(err, errSchemaContract)
}

// prepareSchema waits for a leader, applies the embedded migrations, and
// asserts the schema contract.
//
// Every error it returns except errSchemaContract means "not yet": the caller
// retries. That distinction is the whole point — "the local rqlite has no
// leader" and "this database is behind the binary" used to be the same fatal
// error, so a slow cross-region follower during a rolling upgrade was
// indistinguishable from real schema drift.
func prepareSchema(ctx context.Context, logger *logging.ColoredLogger, cfg *Config, deps *Dependencies) error {
	db := deps.SQLDB
	if db == nil {
		// Only a malformed DSN gets here — sql.Open is lazy and fails on
		// nothing else — so retrying cannot help any more than it can for a
		// schema mismatch.
		return fmt.Errorf("%w: no database handle, check the configured rqlite DSN", errSchemaContract)
	}

	// Wait for the local RQLite to actually be able to serve a leader-routed
	// read before touching the schema.
	//
	// sql.Open above is lazy and rqlite accepts connections well before it has
	// elected a leader, so without this the migrations below fire into a
	// database that cannot answer them. systemd's After=/Requires= on the
	// rqlite unit does not help: for Type=simple it orders process start, not
	// readiness. Gating on the real signal turns "gateway dies at boot because
	// the database was 3 seconds behind it" into "gateway waits 3 seconds".
	readyTimeout := cfg.rqliteReadyTimeout()
	readyCtx, readyCancel := context.WithTimeout(ctx, readyTimeout)
	defer readyCancel()
	if err := rqlite.WaitForLeader(readyCtx, db, readyTimeout); err != nil {
		return fmt.Errorf("rqlite not ready for schema work: %w", err)
	}
	logger.ComponentInfo(logging.ComponentGeneral, "RQLite ready (leader reachable), applying schema")

	// Apply embedded migrations to ensure schema is up-to-date.
	// This is critical for namespace gateways whose RQLite instances
	// don't get migrations from the main cluster RQLiteManager.
	migCtx, migCancel := context.WithTimeout(ctx, cfg.schemaApplyTimeout())
	defer migCancel()

	// A NAMESPACE gateway's RQLite is ALSO the tenant app's own database (they
	// read/write it via /v1/rqlite and /v1/db/sqlite). Apply core's schema under
	// an ISOLATED tracker (orama_schema_migrations) and free the generic table
	// names the tenant needs to own — "schema_migrations" and the dead pubsub
	// "subscriptions" — so core and app schema never collide (bugboard #150).
	// Detection mirrors the global-auth-client check: a namespace gateway is the
	// one configured with a separate GlobalRQLiteDSN pointing at the main cluster.
	if isNamespaceGateway(cfg) {
		if err := rqlite.ApplyEmbeddedMigrationsNamespace(migCtx, db, migrations.FS, logger.Logger); err != nil {
			return fmt.Errorf("apply namespace embedded migrations failed: %w "+
				"(hint: this namespace gateway will not serve without its required schema; "+
				"check the namespace RQLite health — this is retried automatically)", err)
		}
		logger.ComponentInfo(logging.ComponentGeneral, "Namespace-isolated migrations applied to gateway RQLite")

		// Schema-version contract against the ISOLATED tracker (a namespace
		// RQLite records core's applied versions in orama_schema_migrations,
		// leaving schema_migrations for the tenant).
		applied, err := rqlite.AppliedVersionFromTracker(migCtx, db, rqlite.NamespaceMigrationsTracker())
		if err != nil {
			return fmt.Errorf("namespace schema contract read failed: %w", err)
		}
		if required := migrations.RequiredVersion(); applied < required {
			return fmt.Errorf("%w: namespace schema applied=%d, required=%d", errSchemaContract, applied, required)
		}
		logger.ComponentInfo(logging.ComponentGeneral, "Namespace schema contract satisfied",
			zap.Int("required_version", migrations.RequiredVersion()))
		return nil
	}

	// Main cluster: full core schema, tracked in the standard schema_migrations.
	//
	// An apply failure is retryable — it is almost always the leader going
	// away mid-apply. A gateway that cannot bring its schema up to the version
	// its binary expects must never serve, because it would silently corrupt
	// deploys later (INSERTing into missing columns, surfacing as a cryptic SQL
	// error to end users); that is what the readiness state below enforces, by
	// refusing every request until the schema is right.
	if err := rqlite.ApplyEmbeddedMigrations(migCtx, db, migrations.FS, logger.Logger); err != nil {
		return fmt.Errorf("apply embedded migrations failed: %w "+
			"(hint: this gateway can't safely run without its required schema; "+
			"check the underlying RQLite cluster health and re-run startup)", err)
	}
	logger.ComponentInfo(logging.ComponentGeneral, "Embedded migrations applied to gateway RQLite")

	// Schema-version contract: even if the apply call returned nil, verify
	// that the highest migration the binary embeds is recorded as applied.
	// Catches:
	//   - silent partial-apply states where the marker row was never written
	//   - clusters where the binary was upgraded but RQLite has stale schema
	//   - operator manually deleted rows from schema_migrations
	if err := migrations.AssertSchema(migCtx, db); err != nil {
		// AssertSchema returns two very different things: a real version
		// mismatch, and a failure to READ schema_migrations at all. Only the
		// first is permanent. Treating both as the contract violation would
		// let a lost leader or a context deadline between the wait above and
		// this read latch the gateway into blocked forever — a 200ms hiccup
		// taking a namespace down until someone restarts it.
		var mismatch *migrations.SchemaMismatchError
		if errors.As(err, &mismatch) {
			return fmt.Errorf("%w: %w", errSchemaContract, err)
		}
		return fmt.Errorf("schema contract read failed: %w", err)
	}
	logger.ComponentInfo(logging.ComponentGeneral, "Schema contract satisfied",
		zap.Int("required_version", migrations.RequiredVersion()))

	return nil
}

// initializeGlobalRQLite resolves Dependencies.GlobalORMClient — the handle on
// the MAIN cluster's RQLite (bugboard #153).
//
// A namespace gateway runs against its own isolated RQLite (cfg.RQLiteDSN) and
// is additionally told where the main cluster lives (cfg.GlobalRQLiteDSN, used
// today for API-key validation). Cluster-wide tables live ONLY in the main
// RQLite: dns_nodes is written by pkg/node/dns_registration.go through the
// node's own adapter. The namespace RQLite has the table — core migrations run
// there under an isolated tracker — but it is permanently empty, and an empty
// result is indistinguishable from "this cluster has no active nodes".
//
// On the MAIN gateway the two DSNs are the same (or the global one is unset),
// so the global handle IS ORMClient and no second connection is opened.
//
// A namespace gateway that cannot reach the main RQLite is a hard failure, not
// a degraded mode: the caller would otherwise read an empty topology and act on
// it. Auth already depends on the same database, so there is no configuration
// in which proceeding without it is correct.
func initializeGlobalRQLite(logger *logging.ColoredLogger, cfg *Config, deps *Dependencies) error {
	globalDSN := strings.TrimSpace(cfg.GlobalRQLiteDSN)
	if globalDSN == "" || globalDSN == strings.TrimSpace(cfg.RQLiteDSN) {
		deps.GlobalORMClient = deps.ORMClient
		deps.globalSQLDB = deps.SQLDB
		return nil
	}

	credDSN, err := credentialedRQLiteDSN(globalDSN, cfg.RQLiteUsername, cfg.RQLitePassword)
	if err != nil {
		return fmt.Errorf("global_rqlite_dsn: %w", err)
	}
	dsn := appendRQLiteQueryParams(credDSN)
	db, err := sql.Open("rqlite", dsn)
	if err != nil {
		return fmt.Errorf("open global rqlite (%s): %s", rqlite.RedactDSN(globalDSN), rqlite.RedactError(err, dsn))
	}
	// It carries cluster-topology reads and the auth service's single-row
	// compare-and-swap writes (bindAuthRegistry), both short; a wide pool here
	// would only add idle connections against the main cluster from every
	// namespace gateway on the node.
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	deps.globalSQLDB = db
	deps.GlobalORMClient = rqlite.NewClient(db)

	logger.ComponentInfo(logging.ComponentGeneral, "Global RQLite handle ready (cluster topology and auth writes)",
		zap.String("global_dsn", rqlite.RedactDSN(globalDSN)))
	return nil
}

// initializeOlric sets up the Olric distributed cache client with retry and background reconnection
func initializeOlric(logger *logging.ColoredLogger, cfg *Config, deps *Dependencies, networkClient client.NetworkClient) {
	logger.ComponentInfo(logging.ComponentGeneral, "Initializing Olric cache client...")

	// Discover Olric servers dynamically from LibP2P peers if not explicitly configured
	olricServers := cfg.OlricServers
	if len(olricServers) == 0 {
		logger.ComponentInfo(logging.ComponentGeneral, "Olric servers not configured, discovering from LibP2P peers...")
		discovered := discoverOlricServers(networkClient, logger.Logger)
		if len(discovered) > 0 {
			olricServers = discovered
			logger.ComponentInfo(logging.ComponentGeneral, "Discovered Olric servers from LibP2P peers",
				zap.Strings("servers", olricServers))
		} else {
			// Fallback to the local index Olric, through the constant.
			olricServers = []string{constants.OlricAddrFor("localhost")}
			logger.ComponentInfo(logging.ComponentGeneral, "No Olric servers discovered, using localhost fallback",
				zap.Strings("servers", olricServers))
		}
	} else {
		logger.ComponentInfo(logging.ComponentGeneral, "Using explicitly configured Olric servers",
			zap.Strings("servers", olricServers))
	}

	olricCfg := olric.Config{
		Servers: olricServers,
		Timeout: cfg.OlricTimeout,
	}

	// Recorded whether or not the connection works, so the supervisor
	// reconnects to where Olric actually is rather than to a guess.
	deps.OlricServers = olricServers

	olricClient, err := initializeOlricClientWithRetry(olricCfg, logger)
	if err != nil {
		logger.ComponentWarn(logging.ComponentGeneral, "failed to initialize Olric cache client; cache endpoints disabled", zap.Error(err))
		// Note: Background reconnection will be handled by the Gateway itself
	} else {
		deps.OlricClient = olricClient
		deps.OlricCurrent.Set(olricClient)
		logger.ComponentInfo(logging.ComponentGeneral, "Olric cache client ready",
			zap.Strings("servers", olricCfg.Servers),
			zap.Duration("timeout", olricCfg.Timeout),
		)
	}
}

// initializeOlricClientWithRetry attempts to create an Olric client with exponential backoff
func initializeOlricClientWithRetry(cfg olric.Config, logger *logging.ColoredLogger) (*olric.Client, error) {
	backoff := olricInitInitialBackoff

	for attempt := 1; attempt <= olricInitMaxAttempts; attempt++ {
		client, err := olric.NewClient(cfg, logger.Logger)
		if err == nil {
			if attempt > 1 {
				logger.ComponentInfo(logging.ComponentGeneral, "Olric cache client initialized after retries",
					zap.Int("attempts", attempt))
			}
			return client, nil
		}

		logger.ComponentWarn(logging.ComponentGeneral, "Olric cache client init attempt failed",
			zap.Int("attempt", attempt),
			zap.Duration("retry_in", backoff),
			zap.Error(err))

		if attempt == olricInitMaxAttempts {
			return nil, fmt.Errorf("failed to initialize Olric cache client after %d attempts: %w", attempt, err)
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > olricInitMaxBackoff {
			backoff = olricInitMaxBackoff
		}
	}

	return nil, fmt.Errorf("failed to initialize Olric cache client")
}

// initializeIPFS sets up the IPFS Cluster client with automatic endpoint discovery
func initializeIPFS(logger *logging.ColoredLogger, cfg *Config, deps *Dependencies) error {
	logger.ComponentInfo(logging.ComponentGeneral, "Initializing IPFS Cluster client...")

	// Discover IPFS endpoints from node configs if not explicitly configured
	ipfsClusterURL := cfg.IPFSClusterAPIURL
	ipfsAPIURL := cfg.IPFSAPIURL
	ipfsTimeout := cfg.IPFSTimeout
	ipfsReplicationFactor := cfg.IPFSReplicationFactor

	if ipfsClusterURL == "" {
		logger.ComponentInfo(logging.ComponentGeneral, "IPFS Cluster URL not configured, discovering from node configs...")
		discovered := discoverIPFSFromNodeConfigs(logger.Logger)
		if discovered.clusterURL != "" {
			ipfsClusterURL = discovered.clusterURL
			ipfsAPIURL = discovered.apiURL
			if discovered.timeout > 0 {
				ipfsTimeout = discovered.timeout
			}
			if discovered.replicationFactor > 0 {
				ipfsReplicationFactor = discovered.replicationFactor
			}
			logger.ComponentInfo(logging.ComponentGeneral, "Discovered IPFS endpoints from node configs",
				zap.String("cluster_url", ipfsClusterURL),
				zap.String("api_url", ipfsAPIURL))
		} else {
			// Fallback to localhost defaults
			ipfsClusterURL = "http://localhost:10108"
			ipfsAPIURL = "http://localhost:10107"
			logger.ComponentInfo(logging.ComponentGeneral, "No IPFS config found in node configs, using localhost defaults")
		}
	}

	if ipfsAPIURL == "" {
		ipfsAPIURL = "http://localhost:10107"
	}
	if ipfsTimeout == 0 {
		ipfsTimeout = 60 * time.Second
	}
	if ipfsReplicationFactor == 0 {
		ipfsReplicationFactor = 3
	}

	ipfsCfg := ipfs.Config{
		ClusterAPIURL: ipfsClusterURL,
		IPFSAPIURL:    ipfsAPIURL,
		Timeout:       ipfsTimeout,
	}
	if cfg.ClusterSecret != "" {
		key, err := secrets.DeriveKey(cfg.ClusterSecret, ipfs.WrapPurpose)
		if err != nil {
			return fmt.Errorf("derive the IPFS wrap key: %w", err)
		}
		ipfsCfg.WrapKey = key
		// The cluster REST API requires the credentials install derived
		// from the same secret (ipfs.ClusterRESTPassword).
		password, err := ipfs.ClusterRESTPassword(cfg.ClusterSecret)
		if err != nil {
			return fmt.Errorf("derive the IPFS Cluster REST API password: %w", err)
		}
		ipfsCfg.ClusterAPIPassword = password
		token, err := ipfs.KuboAPIToken(cfg.ClusterSecret)
		if err != nil {
			return fmt.Errorf("derive the Kubo API token: %w", err)
		}
		ipfsCfg.KuboAPIToken = token
	}

	ipfsClient, err := ipfs.NewClient(ipfsCfg, logger.Logger)
	if err != nil {
		return fmt.Errorf("initialize the IPFS Cluster client: %w", err)
	}

	deps.IPFSClient = ipfsClient

	// Check peer count and warn if insufficient (use background context to avoid blocking)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if peerCount, err := ipfsClient.GetPeerCount(ctx); err == nil {
		if peerCount < ipfsReplicationFactor {
			logger.ComponentWarn(logging.ComponentGeneral, "insufficient cluster peers for replication factor",
				zap.Int("peer_count", peerCount),
				zap.Int("replication_factor", ipfsReplicationFactor),
				zap.String("message", "Some pin operations may fail until more peers join the cluster"))
		} else {
			logger.ComponentInfo(logging.ComponentGeneral, "IPFS Cluster peer count sufficient",
				zap.Int("peer_count", peerCount),
				zap.Int("replication_factor", ipfsReplicationFactor))
		}
	} else {
		logger.ComponentWarn(logging.ComponentGeneral, "failed to get cluster peer count", zap.Error(err))
	}

	logger.ComponentInfo(logging.ComponentGeneral, "IPFS Cluster client ready",
		zap.String("cluster_api_url", ipfsCfg.ClusterAPIURL),
		zap.String("ipfs_api_url", ipfsAPIURL),
		zap.Duration("timeout", ipfsCfg.Timeout),
		zap.Int("replication_factor", ipfsReplicationFactor),
	)

	// Store IPFS settings back in config for use by handlers
	cfg.IPFSAPIURL = ipfsAPIURL
	cfg.IPFSReplicationFactor = ipfsReplicationFactor
	return nil
}

// initializeServerless sets up the serverless function engine and related components
func initializeServerless(logger *logging.ColoredLogger, cfg *Config, deps *Dependencies, networkClient client.NetworkClient) error {
	logger.ComponentInfo(logging.ComponentGeneral, "Initializing serverless function engine...")

	if deps.ORMClient == nil || deps.IPFSClient == nil {
		return fmt.Errorf("serverless engine requires RQLite and IPFS; functions disabled")
	}

	// Create serverless registry (stores functions in RQLite + IPFS)
	registryCfg := serverless.RegistryConfig{
		IPFSAPIURL: cfg.IPFSAPIURL,
	}
	registry := serverless.NewRegistry(deps.ORMClient, deps.IPFSClient, registryCfg, logger.Logger)
	deps.ServerlessRegistry = registry

	// Backfill: re-pin every active function's WASM cluster-wide so functions
	// deployed before the pin-everywhere fix are durable + GC-safe. Incident
	// 2026-06-24: scheduled IPFS GC deleted unpinned function WASM. New deploys
	// are covered by uploadWASM's pin-everywhere + hard-fail; this protects
	// existing ones. Best-effort + idempotent (the cluster dedups pins), delayed
	// so it never races gateway startup. The rolling-restart cadence naturally
	// staggers it across nodes; the first node to run it pins everything
	// cluster-wide and the rest are no-ops.
	go func() {
		time.Sleep(2 * time.Minute) // let the namespace + IPFS cluster settle
		bctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		n, berr := registry.RepinAllWASM(bctx)
		if berr != nil {
			logger.Logger.Warn("serverless: WASM re-pin backfill failed", zap.Error(berr))
			return
		}
		logger.Logger.Info("serverless: WASM re-pin backfill complete", zap.Int("pinned", n))
	}()

	// Create WebSocket manager for function streaming
	deps.ServerlessWSMgr = serverless.NewWSManager(logger.Logger)

	// Get underlying Olric client if available

	// Get pubsub adapter from client for serverless functions
	var pubsubAdapter pubsub.Bus
	if networkClient != nil {
		if concreteClient, ok := networkClient.(*client.Client); ok {
			pubsubAdapter = concreteClient.PubSubAdapter()
			if pubsubAdapter != nil {
				logger.ComponentInfo(logging.ComponentGeneral, "pubsub adapter available for serverless functions")
			} else {
				logger.ComponentWarn(logging.ComponentGeneral, "pubsub adapter is nil - serverless pubsub will be unavailable")
			}
		}
	}

	// Create WASM engine configuration (needed before secrets manager)
	engineCfg := serverless.DefaultConfig()
	engineCfg.DefaultMemoryLimitMB = 128
	engineCfg.MaxMemoryLimitMB = 256
	engineCfg.DefaultTimeoutSeconds = 30
	engineCfg.MaxTimeoutSeconds = 60
	engineCfg.ModuleCacheSize = 100
	// Surface the per-phase slow-invoke diagnostic (instantiate_ms / run_ms)
	// above 1s instead of the 5s default — a >1s serverless invocation is
	// genuinely slow (well-built handlers are <300ms), and this makes the
	// cold-start floor (bugboard #27: async-dispatched stateless handlers pay a
	// fresh instantiate + TinyGo _start per call) visible for correlation
	// against client-side request_ids.
	engineCfg.SlowInvokeThresholdMs = 1000

	// Create secrets manager for serverless functions (AES-256-GCM encrypted).
	//
	// The encryption key is derived from the encryption root via HKDF, so every
	// gateway computes the identical key. The root starts as a copy of the
	// cluster secret so existing rows stay readable; an operator rotate
	// replaces the IKM without touching IPFS-Cluster or the mesh bearer.
	if err := waitForRegistryLeader(cfg, deps); err != nil {
		return err
	}
	encHolder, err := bootstrapEncryptionRoot(cfg, deps)
	if err != nil {
		return err
	}
	deps.EncHolder = encHolder
	// Never empty: LoadOrMaterialize fails rather than return a root without
	// a current IKM, and a rotate refuses one.
	ikm := deps.EncHolder.Get().CurrentIKM

	var secretsMgr serverless.SecretsManager
	if secretsKeyHex, keyErr := resolveSecretsEncryptionKeyHex(ikm, cfg.SecretsEncryptionKey); keyErr != nil {
		logger.ComponentWarn(logging.ComponentGeneral, "Failed to derive secrets encryption key; get_secret will be unavailable",
			zap.Error(keyErr))
	} else if smImpl, secretsErr := hostfunctions.NewDBSecretsManager(deps.ORMClient, secretsKeyHex, false, logger.Logger); secretsErr != nil {
		logger.ComponentWarn(logging.ComponentGeneral, "Failed to initialize secrets manager; get_secret will be unavailable",
			zap.Error(secretsErr))
	} else {
		smImpl.SetHolder(deps.EncHolder)
		deps.SecretsMgr = smImpl
		secretsMgr = smImpl
	}

	// Initialize push notification subsystem.
	//
	// Bug #220 follow-up: the subsystem ALWAYS initializes (so tenants can
	// register devices and PUT their per-namespace push config), regardless
	// of whether the gateway YAML has a default provider configured. The Manager wraps
	// the device store + per-namespace ConfigStore; Send paths route
	// through Manager so per-namespace config takes effect.
	//
	// PushDispatcher (legacy) is set only when YAML defaults exist —
	// kept for back-compat with code that hasn't migrated to Manager.
	pushDispatcher, pushStore, pushManager, pushCfgStore, pushCredManager, err := buildPushDispatcher(cfg, deps.ORMClient, deps.GlobalORMClient, logger, ikm, deps.EncHolder)
	if err != nil {
		// Non-fatal: log and continue. Functions calling push_send will get nil
		// (silent no-op) and HTTP /v1/push/* endpoints return 503.
		logger.ComponentWarn(logging.ComponentGeneral,
			"push notifications disabled (init failed)", zap.Error(err))
	}
	deps.PushDispatcher = pushDispatcher
	deps.PushDeviceStore = pushStore
	deps.PushManager = pushManager
	deps.PushConfigStore = pushCfgStore
	deps.PushCredentialsManager = pushCredManager

	// Create host functions provider (allows functions to call Orama services)
	hostFuncsCfg := hostfunctions.HostFunctionsConfig{
		IPFSAPIURL:  cfg.IPFSAPIURL,
		HTTPTimeout: 30 * time.Second,
		// feat-9 — TURN config for the turn_credentials host fn.
		// Empty TURNSecret → host fn returns {configured:false} envelope
		// (same shape as the HTTP endpoint's 503 semantically).
		TURNDomain:       cfg.TURNDomain,
		TURNSecret:       cfg.TURNSecret,
		StealthCDNDomain: cfg.StealthCDNDomain,
		// deps.ORMClient is this gateway's own database: the tenant's rqlite
		// on a namespace gateway, which only that namespace's functions may
		// reach; the cluster registry on the cluster gateway, which no
		// function may (bugboard #427).
		DatabaseNamespace: functionDatabaseNamespace(cfg),
	}
	// WS-PubSub bridge: wire PubSub topics directly to WS clients without
	// per-event WASM invocation. The bridge is a thin layer over the
	// pubsub adapter + WSManager.
	deps.WSBridge = wsbridge.New(pubsubAdapter, deps.ServerlessWSMgr, logger.Logger)

	hostFuncs := hostfunctions.NewHostFunctions(
		deps.ORMClient,
		deps.OlricCurrent.Underlying,
		deps.IPFSClient,
		pubsubAdapter, // pubsub adapter for serverless functions
		deps.ServerlessWSMgr,
		secretsMgr,
		pushDispatcher, // legacy — fallback when manager isn't wired
		pushManager,    // bug #220 follow-up — per-namespace config
		deps.WSBridge,  // may be nil; WSPubSubBridge returns explicit error
		hostFuncsCfg,
		logger.Logger,
	)

	deps.HostFuncs = hostFuncs

	// Create WASM engine with multi-tier rate limiter (per-(ns, fn, wallet, ip),
	// per-(ns, wallet), per-(ns)). The legacy global limit is honored as
	// the per-namespace ceiling so no behavior regresses for existing deployments.
	rlCfg := serverless.DefaultLimiterConfig()
	if engineCfg.GlobalRateLimitPerMinute > 0 {
		rlCfg.PerNamespacePerMinute = engineCfg.GlobalRateLimitPerMinute
	}
	rateLimiter := serverless.NewMultiTierLimiter(rlCfg)
	engine, err := serverless.NewEngine(engineCfg, registry, hostFuncs, logger.Logger,
		serverless.WithInvocationLogger(registry),
		serverless.WithRateLimiter(rateLimiter),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize serverless engine: %w", err)
	}
	deps.ServerlessEngine = engine

	// Create invoker
	deps.ServerlessInvoker = serverless.NewInvoker(engine, registry, hostFuncs, ownNamespace(cfg), logger.Logger)

	// Wire the invoker back into hostFuncs so the function_invoke host
	// function can dispatch sub-invocations from inside a WASM function
	// (e.g. rpc-router routing client RPCs to per-op handlers).
	hostFuncs.SetInvoker(deps.ServerlessInvoker)

	// Create PubSub trigger store and dispatcher
	triggerStore := triggers.NewPubSubTriggerStore(deps.ORMClient, logger.Logger)

	// Pass the pubsub adapter so the dispatcher can subscribe to libp2p
	// for every literal trigger pattern (bugboard #282 fix). nil-safe:
	// dispatcher's Start/Refresh become no-ops when adapter is unavailable,
	// preserving the legacy HTTP-only Dispatch hook.
	deps.PubSubDispatcher = triggers.NewPubSubDispatcher(
		triggerStore,
		deps.ServerlessInvoker,
		deps.OlricCurrent.Underlying,
		pubsubAdapter,
		logger.Logger,
	)

	// Wire the dispatcher into hostFuncs so PubSubPublish /
	// PubSubPublishBatch fire local wildcard triggers immediately on
	// publish — closes the bugboard #93 gap where WASM publishes to e.g.
	// "presence:user-1" never reached wildcard handlers like "presence:*"
	// because libp2p has no wildcard subscribe.
	hostFuncs.SetTriggerDispatcher(deps.PubSubDispatcher)

	// Cron trigger store + scheduler. The scheduler polls
	// function_cron_triggers and invokes due rows via the same
	// ServerlessInvoker used for PubSub triggers; the ↓ Start call wires
	// the goroutine up — Stop is invoked from gateway lifecycle shutdown.
	cronStore := triggers.NewCronTriggerStore(deps.ORMClient, logger.Logger)
	deps.CronTriggerStore = cronStore
	deps.CronScheduler = triggers.NewCronScheduler(
		cronStore,
		deps.ServerlessInvoker,
		logger.Logger,
		30*time.Second,
	)

	// Persistent WS instance manager. Cap from gateway config (TODO: surface
	// the knob); 5000 is a sensible default per plan 06.
	deps.PersistentWSManager = persistent.NewManager(5000, logger.Logger)

	// Initialize auth service with persistent signing keys (RSA + EdDSA)
	var keyPEM []byte
	if servesNamedNamespace(cfg.ClientNamespace) {
		keyPEM, err = loadOrCreateSigningKey(cfg.StateDir, logger)
	} else {
		keyPEM, err = loadIndexSigningKey(os.Getenv("CREDENTIALS_DIRECTORY"), cfg.StateDir)
	}
	if err != nil {
		return fmt.Errorf("failed to load or create JWT signing key: %w", err)
	}
	authService, err := auth.NewService(logger, networkClient, string(keyPEM), ownNamespace(cfg), cfg.APIKeyHMACSecret)
	if err != nil {
		return fmt.Errorf("failed to initialize auth service: %w", err)
	}

	bindAuthRegistry(authService, deps)

	// Wire the namespace claims-provider hook (bugboard #548): at JWT mint time
	// the auth service invokes the namespace's reserved `auth-claims-provider`
	// function (if deployed) and merges its additive claims (e.g. account_id)
	// into the token. Fail-open — a missing/slow provider never breaks auth.
	if deps.ServerlessInvoker != nil {
		authService.SetClaimsResolver(newJWTClaimsProvider(deps.ServerlessInvoker, logger.Logger))
	}

	// The key this gateway signs with.
	//
	// It used to be HKDF-derived from the cluster secret, so every node in the
	// cluster held the same private key and could mint a token for any
	// namespace and any subject. Each gateway generates its own now; the
	// public halves are published so the others verify, and a namespace
	// gateway's key is bound to its namespace so it can sign only for its own
	// tenant.
	var edKey ed25519.PrivateKey
	var migrated bool
	if servesNamedNamespace(cfg.ClientNamespace) {
		edKey, migrated, err = loadOrCreateEdSigningKey(cfg.StateDir, cfg.ClusterSecret, logger)
	} else {
		edKey, migrated, err = loadIndexEdSigningKey(os.Getenv("CREDENTIALS_DIRECTORY"), cfg.StateDir, cfg.ClusterSecret, privhelper.PutGatewayKey, logger)
	}
	if err != nil {
		return fmt.Errorf("this gateway has no signing key and cannot mint a token: %w", err)
	}
	authService.SetEdDSAKey(edKey, signingKeyNamespace(cfg.ClientNamespace))
	// Where a rotation writes the replacement: what the next boot reads.
	if servesNamedNamespace(cfg.ClientNamespace) {
		authService.SetKeyStore(auth.FileKeyStore(cfg.StateDir))
	} else {
		authService.SetKeyStore(indexKeyStore(privhelper.PutGatewayKey))
	}

	// Tokens minted before this change carry the old cluster-derived kid, and
	// have to keep verifying across the upgrade. Only when this boot actually
	// replaced that key, and only for one access-token lifetime after that —
	// recorded in the state directory, so a restart does not re-arm it. A
	// gateway that never held the cluster-derived key records it retired.
	if err := armLegacyClusterKey(authService.SigningKeys(), cfg.StateDir, cfg.ClusterSecret, time.Now(), migrated); err != nil {
		return err
	}

	// The key is published once the schema is up (post_schema.go): the
	// signing_keys table may not exist yet, and a namespace gateway learns
	// where its registry is only after this (SetAPIKeyRegistry in New).

	deps.AuthService = authService

	// A namespace's per-IP limits (bugboard #749): the invoke ceiling is read
	// by the limiter through the auth service's cache, and new-wallet sign-ins
	// are counted in this gateway's Olric, shared by every gateway on it.
	rateLimiter.SetNamespaceLimits(authService)
	authService.SetNewWalletCounter(olricWindowCounter{client: deps.OlricCurrent.Underlying})

	// Create HTTP handlers. Built after the auth service because the deploy,
	// delete and secret endpoints write to the audit trail.
	deps.ServerlessHandlers = serverlesshandlers.NewServerlessHandlers(
		deps.ServerlessInvoker,
		deps.ServerlessEngine,
		registry,
		deps.ServerlessWSMgr,
		deps.WSSessions,
		triggerStore,
		cronStore,
		deps.PubSubDispatcher,
		deps.PersistentWSManager,
		deps.WSBridge,
		secretsMgr,
		authService.Audit(),
		logger.Logger,
	)
	deps.ServerlessHandlers.SetFunctionLimits(engineCfg.MaxMemoryLimitMB, engineCfg.MaxTimeoutSeconds, engineCfg.MaxRetryCount)
	if isNamespaceGateway(cfg) {
		deps.ServerlessHandlers.SetServedNamespace(cfg.ClientNamespace)
	}
	if err := wireCapabilities(cfg.ClusterSecret, authService, deps.ServerlessHandlers, hostFuncs, logger.Logger); err != nil {
		return err
	}
	// A function asks for the verdict on its caller's attestation under the
	// policy requests are judged by (feat-965).
	hostFuncs.SetAttestationJudge(serverlesshandlers.NewAttestationJudge(authService))

	logger.ComponentInfo(logging.ComponentGeneral, "Serverless function engine ready",
		zap.Int("default_memory_mb", engineCfg.DefaultMemoryLimitMB),
		zap.Int("default_timeout_sec", engineCfg.DefaultTimeoutSeconds),
		zap.Int("module_cache_size", engineCfg.ModuleCacheSize),
	)

	return nil
}

// discoverOlricServers discovers Olric server addresses from LibP2P peers.
// Returns a list of IP:port addresses where index Olric servers are expected to run.
func discoverOlricServers(networkClient client.NetworkClient, logger *zap.Logger) []string {
	// Get network info to access peer information
	networkInfo := networkClient.Network()
	if networkInfo == nil {
		logger.Debug("Network info not available for Olric discovery")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	peers, err := networkInfo.GetPeers(ctx)
	if err != nil {
		logger.Debug("Failed to get peers for Olric discovery", zap.Error(err))
		return nil
	}

	olricServers := make([]string, 0)
	seen := make(map[string]bool)

	for _, peer := range peers {
		for _, addrStr := range peer.Addresses {
			// Parse multiaddr
			ma, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				continue
			}

			// Extract IP address
			var ip string
			if ipv4, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil && ipv4 != "" {
				ip = ipv4
			} else if ipv6, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil && ipv6 != "" {
				ip = ipv6
			} else {
				continue
			}

			// Skip localhost loopback addresses (the local Olric is the fallback)
			if ip == "localhost" || ip == "::1" {
				continue
			}

			olricAddr := constants.OlricAddrFor(ip)
			if !seen[olricAddr] {
				olricServers = append(olricServers, olricAddr)
				seen[olricAddr] = true
			}
		}
	}

	// Also check peers from config
	if cfg := networkClient.Config(); cfg != nil {
		for _, peerAddr := range cfg.BootstrapPeers {
			ma, err := multiaddr.NewMultiaddr(peerAddr)
			if err != nil {
				continue
			}

			var ip string
			if ipv4, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil && ipv4 != "" {
				ip = ipv4
			} else if ipv6, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil && ipv6 != "" {
				ip = ipv6
			} else {
				continue
			}

			// Skip localhost
			if ip == "localhost" || ip == "::1" {
				continue
			}

			olricAddr := constants.OlricAddrFor(ip)
			if !seen[olricAddr] {
				olricServers = append(olricServers, olricAddr)
				seen[olricAddr] = true
			}
		}
	}

	// If we found servers, log them
	if len(olricServers) > 0 {
		logger.Info("Discovered Olric servers from LibP2P network",
			zap.Strings("servers", olricServers))
	}

	return olricServers
}

// ipfsDiscoveryResult holds discovered IPFS configuration
type ipfsDiscoveryResult struct {
	clusterURL        string
	apiURL            string
	timeout           time.Duration
	replicationFactor int
}

// discoverIPFSFromNodeConfigs discovers IPFS configuration from node.yaml files.
// Checks node-1.yaml through node-5.yaml for IPFS configuration.
func discoverIPFSFromNodeConfigs(logger *zap.Logger) ipfsDiscoveryResult {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		logger.Debug("Failed to get home directory for IPFS discovery", zap.Error(err))
		return ipfsDiscoveryResult{}
	}

	configDir := filepath.Join(homeDir, ".orama")

	// Try all node config files for IPFS settings
	configFiles := []string{"node-1.yaml", "node-2.yaml", "node-3.yaml", "node-4.yaml", "node-5.yaml"}

	for _, filename := range configFiles {
		configPath := filepath.Join(configDir, filename)
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		var nodeCfg config.Config
		if err := config.DecodeStrict(strings.NewReader(string(data)), &nodeCfg); err != nil {
			logger.Debug("Failed to parse node config for IPFS discovery",
				zap.String("file", filename), zap.Error(err))
			continue
		}

		// Check if IPFS is configured
		if nodeCfg.Database.IPFS.ClusterAPIURL != "" {
			result := ipfsDiscoveryResult{
				clusterURL:        nodeCfg.Database.IPFS.ClusterAPIURL,
				apiURL:            nodeCfg.Database.IPFS.APIURL,
				timeout:           nodeCfg.Database.IPFS.Timeout,
				replicationFactor: nodeCfg.Database.IPFS.ReplicationFactor,
			}

			if result.apiURL == "" {
				result.apiURL = "http://localhost:10107"
			}
			if result.timeout == 0 {
				result.timeout = 60 * time.Second
			}
			if result.replicationFactor == 0 {
				result.replicationFactor = 3
			}

			logger.Info("Discovered IPFS config from node config",
				zap.String("file", filename),
				zap.String("cluster_url", result.clusterURL),
				zap.String("api_url", result.apiURL))

			return result
		}
	}

	return ipfsDiscoveryResult{}
}

// gatewayClientConfig is the configuration of the gateway's main network
// client, which is also the handle an index gateway's auth service reads its
// grants, nonces and API keys through.
func gatewayClientConfig(cfg *Config) (*client.ClientConfig, error) {
	cliCfg := client.DefaultClientConfig(cfg.ClientNamespace)
	if len(cfg.BootstrapPeers) > 0 {
		cliCfg.BootstrapPeers = cfg.BootstrapPeers
	}
	// Explicit rqlite_dsn always wins (bugboard #162). A gateway dials RQLite
	// itself, so it always has endpoints: the client would otherwise reach the
	// database through a gateway, this one.
	endpoints, err := resolveDatabaseEndpoints(cfg, client.DefaultDatabaseEndpoints())
	if err != nil {
		return nil, err
	}
	cliCfg.DatabaseEndpoints = endpoints
	// Reads go to the leader, as the tenant handle's do: an auth decision (a
	// grant `orama members add` just wrote, a nonce just spent) must see every
	// write the leader acknowledged, and a follower's local read does not.
	// `weak` is rqlite's own default; this client used to force `none`. The
	// cost is that while the cluster has no leader (an election, a few seconds
	// with the platform's Raft timing) every read on this client fails rather
	// than answering from the local follower: a control plane that answers
	// late is preferred to one that answers with what it has not caught up on.
	cliCfg.DatabaseReadLevel = client.ReadLevelWeak
	// A namespace gateway's peers dial it (peer discovery); it listens on
	// this node's WireGuard IP only. Every other gateway has no listener.
	listenAddrs, err := libp2pListenAddrs(cfg)
	if err != nil {
		return nil, err
	}
	cliCfg.ListenAddrs = listenAddrs
	// The network status reads this node's IPFS Cluster REST API, which
	// requires the password derived from the cluster secret.
	if cfg.ClusterSecret != "" {
		password, err := ipfs.ClusterRESTPassword(cfg.ClusterSecret)
		if err != nil {
			return nil, err
		}
		cliCfg.IPFSClusterAPIPassword = password
	}
	return cliCfg, nil
}

// resolveDatabaseEndpoints picks the gorqlite endpoint list (bugboard #162).
// A non-empty cfg.RQLiteDSN always wins over DefaultClientConfig's
// peer-derived list. ValidateConfig requires rqlite_dsn, so the defaults are
// only reached by callers that build a Config without validating it.
func resolveDatabaseEndpoints(cfg *Config, defaultEndpoints []string) ([]string, error) {
	if dsn := strings.TrimSpace(cfg.RQLiteDSN); dsn != "" {
		cred, err := credentialedRQLiteDSN(dsn, cfg.RQLiteUsername, cfg.RQLitePassword)
		if err != nil {
			return nil, fmt.Errorf("rqlite_dsn: %w", err)
		}
		return []string{cred}, nil
	}
	return defaultEndpoints, nil
}

// credentialedRQLiteDSN returns dsn carrying exactly one set of credentials:
// its own when it has them, otherwise username/password.
//
// This replaced injectRQLiteAuth, which prefixed user:pass@ to whatever it was
// given. The spawner already writes gateway DSNs with credentials in them, so
// the gateway sent http://orama:<pw>@orama:<pw>@host — a URL whose userinfo is
// everything up to the last '@' — and rqlited answered 401 to every query.
func credentialedRQLiteDSN(dsn, username, password string) (string, error) {
	ep, err := rqlite.EndpointFromDSN(dsn, username, password)
	if err != nil {
		return "", err
	}
	return ep.CredentialedURL(), nil
}

// appendRQLiteQueryParams adds the standard query parameters to a RQLite DSN:
//
//   - `disableClusterDiscovery=true` — gorqlite's discovery /nodes call is
//     unreliable when peers are unreachable; we manage topology ourselves.
//   - `level=weak` — Bug #235. Reads route to the leader (the only node
//     guaranteed to have all committed writes), so a SELECT after an UPDATE
//     in the same serverless invocation sees the new state. Previously
//     `level=none`, which read from the local follower's possibly-stale
//     snapshot. gorqlite's upstream default is `weak`; we were overriding
//     to `none` and that hid this bug.
//
// The cost of `weak` over `none` is one HTTP hop to the leader (~1-2ms over
// the WireGuard mesh) and applies only to reads. Writes are unaffected
// because rqlite always redirects them to the leader regardless of `level`.
func appendRQLiteQueryParams(dsn string) string {
	const params = "disableClusterDiscovery=true&level=weak"
	if strings.Contains(dsn, "?") {
		return dsn + "&" + params
	}
	return dsn + "?" + params
}

// buildPushDispatcher constructs the push subsystem, keyed from ikm (the
// encryption root's current IKM, never empty).
//
// As of bug #220 follow-up, push always initializes, regardless of whether
// any YAML provider config is set:
//
//   - Device store + ConfigStore always build (tenants need to register
//     devices and set per-namespace push config even on gateways with no
//     YAML defaults).
//   - Manager wraps the stores + a YAML-derived Defaults fallback. Each
//     namespace can override any default via PUT /v1/push/config.
//   - The legacy single-tier dispatcher is built only when YAML defaults
//     are non-empty — kept for back-compat with code paths that haven't
//     migrated to Manager.
//
// Returns an error only on store-init failure. The token_fp backfill needs
// migration 033's column, so it runs after the schema is up (post_schema.go),
// not here.
func buildPushDispatcher(
	cfg *Config,
	db rqlite.Client,
	registry rqlite.Client,
	logger *logging.ColoredLogger,
	ikm string,
	holder *secrets.Holder,
) (*push.PushDispatcher, push.PushDeviceStore, *push.Manager, push.ConfigStore, *pushcreds.Manager, error) {
	// The token fingerprint is keyed from the cluster secret, which a secrets
	// rotate does not change, so fingerprints keep matching across a rotate.
	store, err := push.NewRqliteDeviceStore(db, ikm, cfg.ClusterSecret, logger.Logger)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("init push device store: %w", err)
	}
	if h, ok := any(store).(interface{ SetHolder(*secrets.Holder) }); ok {
		h.SetHolder(holder)
	}

	cfgStore, err := push.NewRqliteConfigStore(db, ikm, logger.Logger)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("init push config store: %w", err)
	}
	if h, ok := any(cfgStore).(interface{ SetHolder(*secrets.Holder) }); ok {
		h.SetHolder(holder)
	}

	// Per-namespace, per-provider credentials (feature #72). Generic
	// store — used by APNs, ntfy (post-migration), FCM-direct (future).
	// Provider packages register their Validator at gateway startup
	// (see pushcreds.Register calls below).
	credStore, err := pushcreds.NewRqliteStore(db, ikm, logger.Logger)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("init push credentials store: %w", err)
	}
	if h, ok := any(credStore).(interface{ SetHolder(*secrets.Holder) }); ok {
		h.SetHolder(holder)
	}
	credManager := pushcreds.NewManager(credStore, logger.Logger)

	// Register the Validators that this gateway accepts. Each provider
	// package owns its own JSON schema + redactor; we tell the
	// credentials package which ones to allow at PUT/GET time. Adding a
	// new provider (FCM-direct, SMS, etc.) means a single new Register
	// call here — no other code needs to know.
	pushcreds.Register(pushapns.NewValidator())
	pushcreds.Register(pushntfy.NewValidator())

	// ntfy cluster fan-out (bugboard #858): the default push infra runs an
	// independent ntfy per node with no shared store, so a publish must reach
	// EVERY active node for the subscriber's instance (picked by round-robin
	// DNS) to receive it. The fan-out travels the WireGuard overlay to each
	// node's internal gateway, signed with a coordination MAC (never the public
	// push host). The factory attaches it only to providers using the shared
	// default base URL (a namespace pointing ntfy at its own server is never
	// fanned across our cluster). nil registry or no default base URL → no
	// fan-out (provider publishes to the single base URL).
	var ntfyFanout *ntfyFanoutResolver
	if registry != nil && strings.TrimSpace(cfg.NtfyBaseURL) != "" {
		ntfyFanout = newNtfyFanoutResolver(registry, defaultNtfyFanoutTTL)
	}

	// ProviderFactory turns a resolved Config into the right set of
	// provider instances. Lives here in dependencies.go because this is
	// the only place that imports both the manager package and the
	// concrete provider sub-packages — keeps push core dep-cycle-free.
	//
	// Per-namespace credentialed providers (APNs — feature #72) are
	// constructed here by consulting the credentials manager. If a
	// namespace has stored credentials for a provider, that provider is
	// instantiated with those credentials and registered in the
	// dispatcher; otherwise it's omitted.
	factory := func(ctx context.Context, c push.Config) []push.PushProvider {
		var ps []push.PushProvider

		// ntfy provider — sourced from EITHER the new credentials store
		// (#72, preferred) OR the legacy 026 push_config row. New table
		// wins field-by-field; legacy fills any gap. ntfy is registered
		// only if a BaseURL ends up set; auth_token alone is useless
		// without a server to point at.
		ntfyCfg := pushntfy.Config{
			BaseURL:   c.NtfyBaseURL,
			AuthToken: c.NtfyAuthToken,
			// A tenant-supplied server is reached through a guarded client; the operator's own
			// default (loopback ntfy) is not. Provenance decides, not the URL.
			GuardTarget: c.NtfyBaseURLTenant,
		}
		if c.Namespace != "" && credManager != nil {
			if cred, err := credManager.Get(ctx, c.Namespace, "ntfy"); err == nil && cred != nil {
				if ov, perr := pushntfy.ParseCredentials(cred.JSON); perr == nil {
					if ov.BaseURL != "" {
						ntfyCfg.BaseURL = ov.BaseURL
						ntfyCfg.GuardTarget = true
					}
					if ov.AuthToken != "" {
						ntfyCfg.AuthToken = ov.AuthToken
					}
				} else {
					logger.ComponentWarn(logging.ComponentGeneral,
						"ntfy credentials parse failed",
						zap.String("namespace", c.Namespace),
						zap.Error(perr))
				}
			}
		}
		if ntfyCfg.BaseURL != "" {
			// Fan out across all push nodes ONLY for the shared default infra.
			// A namespace that overrode BaseURL with its own ntfy server keeps
			// single-host delivery (its server, not our cluster).
			if ntfyFanout != nil && !ntfyCfg.GuardTarget && ntfyCfg.BaseURL == cfg.NtfyBaseURL {
				ntfyCfg.FanoutResolver = ntfyFanout.Targets
				ntfyCfg.FanoutSigner = newNtfyFanoutSigner(cfg.ClusterSecret, time.Now)
			}
			ps = append(ps, pushntfy.New(ntfyCfg, logger.Logger))
		}
		if c.ExpoAccessToken != "" {
			ps = append(ps, pushexpo.New(pushexpo.Config{
				AccessToken: c.ExpoAccessToken,
			}, logger.Logger))
		}
		// APNs is fully credentialed — no YAML fallback. The presence of
		// per-namespace credentials is the trigger. Bugboard #408: a
		// single set of APNs credentials spawns BOTH an alert-kind
		// provider (registered as "apns") AND a VoIP/PushKit provider
		// (registered as "apns_voip"). Both share the same JWT signer +
		// HTTP/2 client pool — VoIP only differs in the per-Send wire
		// format (topic suffix, apns-push-type header, empty-payload
		// acceptance). Tenants register PushKit voipPushTokens against
		// provider="apns_voip" and the dispatcher routes accordingly.
		if c.Namespace != "" && credManager != nil {
			if cred, err := credManager.Get(ctx, c.Namespace, "apns"); err == nil && cred != nil {
				if apnsCfg, perr := pushapns.ParseCredentials(cred.JSON); perr == nil {
					if provider, nerr := pushapns.New(apnsCfg, logger.Logger); nerr == nil {
						ps = append(ps, provider)
					} else {
						logger.ComponentWarn(logging.ComponentGeneral,
							"apns provider construction failed",
							zap.String("namespace", c.Namespace),
							zap.Error(nerr))
					}
					if voipProvider, nerr := pushapns.NewVoIP(apnsCfg, logger.Logger); nerr == nil {
						ps = append(ps, voipProvider)
					} else {
						logger.ComponentWarn(logging.ComponentGeneral,
							"apns_voip provider construction failed",
							zap.String("namespace", c.Namespace),
							zap.Error(nerr))
					}
				} else {
					logger.ComponentWarn(logging.ComponentGeneral,
						"apns credentials parse failed",
						zap.String("namespace", c.Namespace),
						zap.Error(perr))
				}
			}
		}
		return ps
	}

	defaults := push.Defaults{
		NtfyBaseURL:     cfg.NtfyBaseURL,
		NtfyAuthToken:   cfg.NtfyAuthToken,
		ExpoAccessToken: cfg.ExpoAccessToken,
	}
	manager := push.NewManager(store, cfgStore, defaults, factory, logger.Logger)

	// Registrations addressed by a rotating topic rather than an account
	// (FEAT-265). Same namespace database and encryption root as the devices;
	// the token fingerprint is keyed from the cluster secret, which a secrets
	// rotate does not change, so fingerprints keep matching across a rotate.
	topicStore, err := push.NewRqliteTopicStore(db, ikm, cfg.ClusterSecret)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("init push topic store: %w", err)
	}
	topicStore.SetHolder(holder)
	manager.SetTopicStore(topicStore)

	// Legacy single-tier dispatcher kept ONLY when YAML defaults exist —
	// some non-Manager code paths (notably the WASM push_send hostfunc
	// before its migration to Manager) still expect a populated
	// PushDispatcher. New code routes via Manager.
	var legacy *push.PushDispatcher
	if !defaults.IsEmpty() {
		legacy = push.New(store, logger.Logger)
		// Boot-time construction: no request context yet. Use Background
		// — the credential lookups here are fast (in-memory cache miss
		// reads rqlite once) and cancellation is irrelevant during boot.
		for _, p := range factory(context.Background(), push.Config{
			NtfyBaseURL:     defaults.NtfyBaseURL,
			NtfyAuthToken:   defaults.NtfyAuthToken,
			ExpoAccessToken: defaults.ExpoAccessToken,
		}) {
			legacy.Register(p)
		}
	}

	if defaults.NtfyBaseURL != "" {
		logger.ComponentInfo(logging.ComponentGeneral, "push default provider: ntfy",
			zap.String("base_url", defaults.NtfyBaseURL))
	}
	if defaults.ExpoAccessToken != "" {
		logger.ComponentInfo(logging.ComponentGeneral, "push default provider: expo configured")
	}
	logger.ComponentInfo(logging.ComponentGeneral,
		"push subsystem initialized; tenants can self-serve via PUT /v1/push/config")

	return legacy, store, manager, cfgStore, credManager, nil
}

// waitForRegistryLeader waits until the rqlite the encryption root is read
// from can answer a leader-routed query. On a node whose rqlite had just
// started, the read failed with "leader not found", the gateway exited, and
// systemd's restart five seconds later happened to find a leader: a crash
// standing in for a readiness wait.
func waitForRegistryLeader(cfg *Config, deps *Dependencies) error {
	db := deps.globalSQLDB
	if db == nil {
		db = deps.SQLDB
	}
	timeout := cfg.rqliteReadyTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := rqlite.WaitForLeader(ctx, db, timeout); err != nil {
		return fmt.Errorf("the registry rqlite has no leader after %s, so the encryption root cannot be read: %w", timeout, err)
	}
	return nil
}

// bindAuthRegistry gives the auth service the rqlite client its
// compare-and-swap writes need the affected-row count from: the single-use
// nonce claim, refresh-token rotation, grant and ownership changes, device
// and session state. Every one of those tables is in the cluster registry,
// which the service reads through registryDatabase.
//
// On a namespace gateway ORMClient is the tenant's own RQLite, which has none
// of them, so every wallet sign-in there failed with "no such table: nonces"
// (stagenet, 2026-09-29). GlobalORMClient is the registry on both gateway
// kinds; on the cluster gateway it is ORMClient.
func bindAuthRegistry(authService *auth.Service, deps *Dependencies) {
	if deps.GlobalORMClient != nil {
		authService.SetRqliteClient(deps.GlobalORMClient)
	}
}
