package sqlguard

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
)

// The escalation this guard exists to stop. A namespace database is both the
// tenant's application database and the database that authenticates the
// namespace, so a function's SQL could mint itself a key, make a wallet the
// namespace's owner, or read every other function's secrets.
func TestCheck_refusesTheTablesThatGrantAuthority(t *testing.T) {
	for _, query := range []string{
		"SELECT key, scopes FROM api_keys",
		"UPDATE api_keys SET scopes = 'admin' WHERE id = 1",
		"INSERT INTO grants(principal_id, namespace_id, role) VALUES (1, 1, 'owner')",
		"DELETE FROM grants",
		"UPDATE grants SET role = 'owner' WHERE id = 1",
		"INSERT INTO principals(type, identifier) VALUES ('wallet', '0xattacker')",
		"SELECT * FROM function_secrets",
		"SELECT value FROM function_env_vars",
		"SELECT token FROM refresh_tokens",
		"SELECT * FROM nonces",
		"INSERT INTO operators(wallet) VALUES ('0xattacker')",
		"SELECT agent_token FROM wireguard_peers",
		"SELECT * FROM invite_tokens",
		"UPDATE namespace_rate_limit_config SET requests_per_minute = 1000000",
		"UPDATE namespace_quotas SET storage_bytes = 0",
		"INSERT INTO dns_records(name, value) VALUES ('x', 'y')",
		"SELECT * FROM orama_schema_migrations",
		// bugboard #427: renaming a namespace re-points every API key of it.
		"UPDATE namespaces SET name = ? WHERE name = ?",
		"SELECT id, name FROM namespaces",
		// bugboard #431: an ownership row is a read grant on another tenant's CID.
		"INSERT INTO ipfs_content_ownership(cid, namespace, uploaded_by) VALUES (?, ?, ?)",
		"SELECT cid FROM ipfs_content_ownership",
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err == nil {
				t.Errorf("allowed: %s", query)
			}
		})
	}
}

// What runs where is platform state. A deployment row written by SQL skips the
// validation /v1/deployments applies to the content, entry point and port, so
// the tables that hold it are reserved with the rest, and so is the history a
// rollback reads its target CID from. Tables that only record what happened
// (events, health checks) stay reachable.
func TestCheck_refusesTheDeploymentTables(t *testing.T) {
	for _, query := range []string{
		"UPDATE deployments SET content_cid = ? WHERE name = ?",
		`INSERT INTO "deployments"(name) VALUES ('x')`,
		"DELETE FROM deployment_domains",
		"SELECT * FROM deployment_replicas",
		"UPDATE home_node_assignments SET home_node_id = 'x'",
		"INSERT INTO port_allocations(port) VALUES (1)",
		"INSERT INTO deployment_history(content_cid) VALUES ('QmForged')",
		"SELECT d.name FROM messages m JOIN [deployments] d ON 1=1",
		// The deployment family's record moved to the registry with it.
		"SELECT * FROM deployment_events",
		"SELECT * FROM deployment_health_checks",
	} {
		if err := Check(query); err == nil {
			t.Errorf("allowed: %s", query)
		}
	}
}

// Every way SQLite lets a name be written has to reach the same answer, or the
// guard is a spelling test.
func TestCheck_refusesEveryWayOfWritingTheName(t *testing.T) {
	for _, query := range []string{
		`SELECT * FROM api_keys`,
		`SELECT * FROM API_KEYS`,
		`SELECT * FROM Api_Keys`,
		`SELECT * FROM "api_keys"`,
		"SELECT * FROM `api_keys`",
		`SELECT * FROM [api_keys]`,
		`SELECT * FROM 'api_keys'`,
		`SELECT * FROM main.api_keys`,
		`SELECT * FROM "main"."api_keys"`,
		`SELECT * FROM main.'api_keys'`,
		`SELECT * FROM /* hide */ api_keys`,
		"SELECT * -- comment\nFROM api_keys",
		`SELECT * FROM   api_keys`,
		`WITH leak AS (SELECT * FROM api_keys) SELECT * FROM leak`,
		`SELECT (SELECT key FROM api_keys LIMIT 1)`,
		`SELECT * FROM messages WHERE id IN (SELECT id FROM api_keys)`,
		`CREATE VIEW leak AS SELECT * FROM api_keys`,
		`CREATE TRIGGER t AFTER INSERT ON api_keys BEGIN SELECT 1; END`,
		`INSERT INTO messages SELECT * FROM api_keys`,
		`SELECT * FROM messages JOIN api_keys ON 1=1`,
		`ALTER TABLE api_keys RENAME TO mine`,
		`DROP TABLE api_keys`,
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err == nil {
				t.Errorf("allowed: %s", query)
			}
		})
	}
}

func TestCheck_allowsATenantsOwnSQL(t *testing.T) {
	for _, query := range []string{
		"SELECT * FROM messages WHERE conversation_id = ?",
		"INSERT INTO conversations(id, name) VALUES (?, ?)",
		"UPDATE users SET display_name = ? WHERE wallet = ?",
		"DELETE FROM attachments WHERE created_at < ?",
		"SELECT COUNT(*) AS n FROM friend_requests",
		"CREATE TABLE IF NOT EXISTS my_table (id TEXT PRIMARY KEY)",
		"SELECT * FROM messages ORDER BY created_at DESC LIMIT 50;",
		"WITH recent AS (SELECT * FROM messages LIMIT 10) SELECT * FROM recent",
		"SELECT * FROM apps",              // core owns the name, but a tenant may already be using it
		"SELECT * FROM schema_migrations", // the tenant's own migration tracker
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err != nil {
				t.Errorf("refused ordinary tenant SQL %q: %v", query, err)
			}
		})
	}
}

// A protected name inside the tenant's own data is data, not a table
// reference. Refusing it would make the guard unusable for a chat app.
func TestCheck_aProtectedNameAsDataIsAllowed(t *testing.T) {
	for _, query := range []string{
		"SELECT * FROM messages WHERE body = 'talk about api_keys'",
		"UPDATE users SET bio = 'I broke the nonces table once' WHERE id = ?",
		"INSERT INTO messages(body) VALUES ('api_keys2')", // contains a protected name, is not one
		"INSERT INTO messages(body) VALUES ('the grants table')",
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err != nil {
				t.Errorf("refused a protected name used as data: %q: %v", query, err)
			}
		})
	}
}

// bugboard #425: SQLite reads a string literal as a table name wherever its
// grammar takes a name, and every list of those positions missed one. A
// literal that is exactly a protected name is refused wherever it is; the
// refusal says to bind the value instead.
func TestCheck_aStringThatIsAProtectedNameIsRefusedAnywhere(t *testing.T) {
	for _, query := range []string{
		"SELECT * FROM messages, 'api_keys'",
		"SELECT * FROM messages,'api_keys'",
		"SELECT * FROM messages, main.'api_keys'",
		"SELECT * FROM ('api_keys')",
		"SELECT * FROM (SELECT 1) AS s, 'api_keys'",
		"WITH x AS (SELECT * FROM messages, 'api_keys') SELECT * FROM x",
		"SELECT * FROM messages WHERE id IN (SELECT 1 FROM messages, 'grants')",
		"INSERT INTO messages(id, body) SELECT 2, k FROM messages, 'api_keys'",
		"CREATE VIEW v AS SELECT * FROM messages, 'function_secrets'",
		"UPDATE OR REPLACE 'api_keys' SET scopes = 'admin'",
		"UPDATE OR IGNORE 'api_keys' SET scopes = 'admin'",
		"UPDATE OR ROLLBACK 'grants' SET role = 'owner'",
		"UPDATE OR ABORT 'grants' SET role = 'owner'",
		"UPDATE OR FAIL main.'api_keys' SET scopes = 'admin'",
		"SELECT * FROM pragma_table_info('api_keys')",
		"SELECT * FROM \v'api_keys'",
		"INSERT INTO messages(body) VALUES ('api_keys')",
	} {
		t.Run(query, func(t *testing.T) {
			var refused *ErrNotAllowed
			if err := Check(query); !errors.As(err, &refused) {
				t.Fatalf("Check(%q) = %v, want an ErrNotAllowed", query, err)
			}
		})
	}
	err := Check("INSERT INTO tags(name) VALUES ('grants')")
	if err == nil || !strings.Contains(err.Error(), "bound argument") {
		t.Errorf("refusal %v does not tell the caller to bind the value", err)
	}
}

// sqlite_dbpage returns raw database pages — protected tables' rows included —
// without the statement naming any table.
func TestCheck_refusesRawStorageTables(t *testing.T) {
	for _, query := range []string{
		"SELECT * FROM sqlite_dbpage",
		"SELECT data FROM main.sqlite_dbpage WHERE pgno = 2",
		"SELECT * FROM dbstat",
		"SELECT * FROM 'sqlite_dbpage'",
	} {
		if err := Check(query); err == nil {
			t.Errorf("ran %q", query)
		}
	}
}

// Comments are removed before anything is looked at, so a protected name
// mentioned in one is not a reason to refuse a statement that never touches it.
// A guard that refused these would be unusable on a codebase whose SQL is
// commented.
func TestCheck_aProtectedNameInACommentIsNotAReference(t *testing.T) {
	for _, query := range []string{
		"SELECT * FROM messages -- api_keys lives elsewhere",
		"SELECT * FROM messages /* not api_keys */ WHERE id = ?",
		"/* function_secrets are read with get_secret */\nSELECT * FROM messages",
		"SELECT * FROM messages\n-- TODO: stop copying this into nonces\n",
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err != nil {
				t.Errorf("refused a statement that only mentions a protected name in a comment: %q: %v", query, err)
			}
		})
	}
}

// A bracketed identifier is one name however it is spelled inside, so a table
// whose name contains a space does not split into pieces that happen to match
// something else.
func TestCheck_bracketQuotingIsOneIdentifier(t *testing.T) {
	if err := Check("SELECT * FROM [my table]"); err != nil {
		t.Errorf("refused a bracketed name with a space: %v", err)
	}
	if err := Check(`SELECT * FROM "my table"`); err != nil {
		t.Errorf("refused a double-quoted name with a space: %v", err)
	}
	if err := Check("SELECT * FROM [api_keys]"); err == nil {
		t.Error("a bracketed protected name was allowed")
	}

	// A quoted name that merely contains a protected word is a different
	// table. Reading the brackets is what tells the two apart; without them
	// the words inside are looked at one by one and this is refused.
	for _, query := range []string{
		"SELECT * FROM [my api_keys backup]",
		`SELECT * FROM "archived api_keys 2024"`,
		"SELECT * FROM `nonces of my own`",
	} {
		if err := Check(query); err != nil {
			t.Errorf("refused a distinct table whose quoted name contains a protected word: %q: %v", query, err)
		}
	}
}

func TestCheck_refusesStatementsThatLeaveTheDatabaseItWasGiven(t *testing.T) {
	for _, query := range []string{
		"ATTACH DATABASE '/opt/orama/.orama/data/other.db' AS other",
		"attach database 'x' as y",
		"DETACH DATABASE other",
		"PRAGMA table_info(messages)",
		"pragma journal_mode = WAL",
		"VACUUM INTO '/tmp/copy.db'",
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err == nil {
				t.Errorf("allowed: %s", query)
			}
		})
	}
}

func TestCheck_refusesASecondStatement(t *testing.T) {
	for _, query := range []string{
		"SELECT 1; SELECT * FROM api_keys",
		"INSERT INTO messages(body) VALUES ('x'); DROP TABLE messages",
	} {
		t.Run(query, func(t *testing.T) {
			if err := Check(query); err == nil {
				t.Errorf("allowed two statements in one call: %s", query)
			}
		})
	}
	if err := Check("SELECT * FROM messages;"); err != nil {
		t.Errorf("a trailing semicolon was refused: %v", err)
	}
	if err := Check("SELECT * FROM messages;  \n "); err != nil {
		t.Errorf("a trailing semicolon with whitespace was refused: %v", err)
	}
}

func TestCheck_saysWhatIsWrong(t *testing.T) {
	err := Check("SELECT * FROM api_keys")
	if err == nil {
		t.Fatal("allowed")
	}
	if !strings.Contains(err.Error(), "api_keys") {
		t.Errorf("the refusal does not name the table: %v", err)
	}
	if !strings.Contains(err.Error(), "platform") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	err = Check("SELECT grants FROM roles")
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("a protected name used as a column: %v, want it refused as a reserved name", err)
	}

	err = Check("PRAGMA foreign_keys = ON")
	if err == nil {
		t.Fatal("allowed")
	}
	if !strings.Contains(err.Error(), "PRAGMA") {
		t.Errorf("the refusal does not name the statement: %v", err)
	}
}

func TestCheck_emptyAndTrivial(t *testing.T) {
	for _, query := range []string{"", "   ", "\n", ";"} {
		if err := Check(query); err != nil {
			t.Errorf("Check(%q) = %v, want nil", query, err)
		}
	}
}

// Tables tenant SQL may name. Each is here because a tenant may already
// be using the name as their own — a namespace database has exactly one
// table called `apps`, and it belongs to whoever wrote to it first — or
// because reading it tells a tenant only about their own namespace.
//
// `audit_events` used to be on this list for the second reason. It moved to the
// registry, where it holds every namespace's trail, so the reason stopped being
// true; so did `deployment_events` and `deployment_health_checks`, with the
// deployments they record.
var reachableTables = map[string]bool{
	"apps":                 true,
	"function_invocations": true, "function_logs": true,
	"function_jobs": true, "function_timers": true, "function_rate_limits": true,
	"function_db_change_tracking": true,
	"namespace_publish_seq":       true,
	"request_logs":                true,
	"push_devices":                true,
	"schema_migrations":           true, "subscriptions": true,
}

// The protected list is a decision about each core table. A migration that adds
// one has to be triaged rather than defaulting to reachable, so this fails until
// somebody says which side the new table is on.
func TestProtectedTables_everyCoreTableIsTriaged(t *testing.T) {
	for table := range coreTables(t) {
		_, protected := protectedTables[table]
		if !protected && !reachableTables[table] {
			t.Errorf("the core migrations create %q and nothing here says whether a function may name it. "+
				"Add it to protectedTables if it grants authority, holds a credential or configures the "+
				"platform; add it to the reachable list here, with a reason, if it does not.", table)
		}
		if protected && reachableTables[table] {
			t.Errorf("%q is on both lists", table)
		}
	}
}

// runtimeCreatedTables are protected tables the gateway creates with CREATE
// TABLE IF NOT EXISTS when it starts, which no migration file mentions.
var runtimeCreatedTables = map[string]bool{"_pubsub_mesh_peers": true, "_namespace_libp2p_peers": true}

// A protected name that no migration creates is a name that has been renamed or
// removed, and a stale entry reads as protection that is not there.
func TestProtectedTables_everyEntryStillExists(t *testing.T) {
	tables := coreTables(t)

	for table := range protectedTables {
		// orama_schema_migrations is created by the namespace migration
		// runner, not by a migration file.
		if table == "orama_schema_migrations" || runtimeCreatedTables[table] {
			continue
		}
		if !tables[table] {
			t.Errorf("%q is protected but the migrations leave no such table; a stale entry reads as protection that is not there", table)
		}
	}
}

// The reachable list is the other half of the triage, and nothing was checking
// it against the schema. deployment_env_vars sat on it after migration 046
// dropped the table, which reads as a decision that a function may name it
// when there is nothing to name.
func TestReachableTables_allStillExist(t *testing.T) {
	tables := coreTables(t)

	for table := range reachableTables {
		if !tables[table] {
			t.Errorf("%q is listed as reachable but the migrations leave no such table; "+
				"a stale entry reads as a decision about something that is not there", table)
		}
	}
}

// coreTables returns the tables the core migrations leave behind: every table
// they create, less every table they later drop.
//
// Reading the CREATEs alone is not the same thing. Migration 046 dropped
// deployment_env_vars and 049 dropped phantom_auth_sessions, and a set built
// from CREATEs still contains both — so a dropped table has to be declared
// either protected or reachable, and both readings are false. A name that is
// not there is neither.
func coreTables(t *testing.T) map[string]bool {
	t.Helper()

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	// ReadDir sorts by name, and migrations are numbered, so the statements
	// are visited in the order the runner applies them. A rename counts as
	// both: 009 builds dns_records_new, drops dns_records and renames the new
	// one into its place.
	ident := `["'` + "`" + `\[]?([a-z_][a-z0-9_]*)`
	stmt := regexp.MustCompile(`(?i)(create|drop)\s+table\s+(?:if\s+(?:not\s+)?exists\s+)?` + ident)
	rename := regexp.MustCompile(`(?i)alter\s+table\s+` + ident + `\s+rename\s+to\s+` + ident)

	tables := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrations.FS.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		text := string(body)
		for _, m := range stmt.FindAllStringSubmatch(text, -1) {
			name := strings.ToLower(m[2])
			if strings.EqualFold(m[1], "create") {
				tables[name] = true
			} else {
				delete(tables, name)
			}
		}
		for _, m := range rename.FindAllStringSubmatch(text, -1) {
			delete(tables, strings.ToLower(m[1]))
			tables[strings.ToLower(m[2])] = true
		}
	}
	if len(tables) < 30 {
		t.Fatalf("found only %d core tables; this test is not reading the migrations", len(tables))
	}
	return tables
}

// The denylist and the table placement have to agree in one direction: a table
// that exists only in the cluster registry is platform state by definition, and
// a function must not name it.
//
// It is a cross-check rather than a derivation because the two lists answer
// different questions. Placement says which database a table lives in; this list
// says which names guest SQL may not use, and it also covers tables that do live
// in a namespace database and still hold the platform's secrets —
// `function_secrets` is the clearest.
func TestProtectedTables_coverEveryClusterOnlyTable(t *testing.T) {
	for _, table := range rqlite.ClusterOnlyTables() {
		if _, protected := protectedTables[table]; !protected {
			t.Errorf("%q lives only in the cluster registry and guest SQL may still name it. "+
				"Stripping it from a namespace database makes the query fail anyway, but the "+
				"refusal should say what it is rather than 'no such table'.", table)
		}
	}
}

// The platform-trust tables that were placed in a namespace database and
// reachable by tenant SQL (the platform-state design's finding F2), and the
// control-plane tables placement now says a namespace gateway never touches.
// Each is refused for a write, a read, and a quoted name.
func TestCheck_refusesEveryTableThatWasOnceReachable(t *testing.T) {
	for _, table := range []string{
		"functions", "function_cron_triggers", "function_pubsub_triggers", "function_db_triggers",
		"namespace_push_config", "namespace_webrtc_config", "deployment_history",
		"namespace_sqlite_databases", "namespace_sqlite_backups", "webrtc_rooms",
		"webrtc_port_allocations", "namespace_cluster_events", "namespace_pending_cleanup",
		"node_health_events", "rqlite_backups",
	} {
		for _, query := range []string{
			"INSERT INTO " + table + " (id) VALUES (1)",
			"SELECT * FROM " + table,
			`DELETE FROM "` + table + `"`,
			"UPDATE main." + strings.ToUpper(table) + " SET id = 2",
		} {
			var refused *ErrNotAllowed
			if err := Check(query); !errors.As(err, &refused) {
				t.Errorf("Check(%q) = %v, want a refusal", query, err)
			}
		}
	}
}

// The class of hole this closes: a table the platform acts on, placed in a
// namespace database, that nobody added to the denylist. Placement carries a
// trust class per table, and every platform-trust table has to be reserved here,
// whichever database it is placed in. A table added to the placement without a
// trust class is platform-trust by default, so forgetting fails this test.
func TestProtectedTables_coverEveryPlatformTrustTable(t *testing.T) {
	platform := rqlite.TablesOfTrust(rqlite.TrustPlatform)
	if len(platform) < 30 {
		t.Fatalf("only %d platform-trust tables; the placement is not being read", len(platform))
	}
	for _, table := range platform {
		if _, protected := protectedTables[table]; !protected {
			t.Errorf("%q is platform-trust in rqlite's tablePlacement, so the platform acts on its rows, "+
				"and tenant SQL may still name it. Add it to protectedTables with a reason; if a tenant's "+
				"rows in it are worth nothing to the platform, classify it TrustTenantData or TrustTelemetry instead.", table)
		}
	}
}

// The other direction, for the tables a tenant is allowed to name: one that is
// classified as tenant data or telemetry and is also reserved is a decision
// somebody should have to state, because the reserved name breaks any tenant
// application that owns a table of that name. push_topics is the one reason
// that is allowed today (FEAT-265): it is tenant data whose rows must not be
// joined against an invocation.
func TestProtectedTables_nonPlatformEntriesAreDeliberate(t *testing.T) {
	deliberate := map[string]bool{"push_topics": true}
	for _, trust := range []rqlite.Trust{rqlite.TrustTenantData, rqlite.TrustTelemetry} {
		for _, table := range rqlite.TablesOfTrust(trust) {
			if _, protected := protectedTables[table]; protected && !deliberate[table] {
				t.Errorf("%q is reserved but placement says the platform does not act on its rows", table)
			}
		}
	}
}

// website/src/docs/developer/functions.mdx lists the reserved names so a tenant can avoid them. The
// list is copied by hand, so this fails when it and protectedTables differ.
func TestProtectedTables_matchTheDocumentedList(t *testing.T) {
	doc, err := os.ReadFile("../../testdata/repo/website/src/docs/developer/functions.mdx")
	if err != nil {
		t.Fatalf("read website/src/docs/developer/functions.mdx: %v", err)
	}
	text := string(doc)
	start := strings.Index(text, "The reserved names are ")
	if start < 0 {
		t.Fatal("website/src/docs/developer/functions.mdx no longer lists the reserved names")
	}
	end := strings.Index(text[start:], "(the list in")
	if end < 0 {
		t.Fatal("reserved-name list in website/src/docs/developer/functions.mdx has no end marker")
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z0-9_]+)`").FindAllStringSubmatch(text[start:start+end], -1) {
		documented[m[1]] = true
	}
	for name := range protectedTables {
		if !documented[name] {
			t.Errorf("%s is protected but not in website/src/docs/developer/functions.mdx's reserved list", name)
		}
	}
	for name := range documented {
		if _, ok := protectedTables[name]; !ok {
			t.Errorf("website/src/docs/developer/functions.mdx lists %s as reserved, but it is not protected", name)
		}
	}
}

func TestCheckTenantSQLite_refusesWhatReachesOutsideTheFile(t *testing.T) {
	for _, q := range []string{
		"ATTACH DATABASE '/x.db' AS x", "  attach '/x.db' as x", "/**/ATTACH/**/'/x.db'/**/AS x",
		"DETACH DATABASE main", "SELECT 1; SELECT 2", "SELECT 1; ATTACH '/x.db' AS x",
		"VACUUM INTO '/x.db'", "vacuum main into '/x.db'", "VACUUM/**/INTO '/x.db'",
		"PRAGMA temp_store_directory = '/tmp/x'", "pragma main.data_store_directory='/x'",
		"PRAGMA writable_schema = ON", `PRAGMA "writable_schema" = 1`, "PRAGMA 'temp_store_directory' = '/x'",
		"PRAGMA [data_store_directory]", "PRAGMA WRITABLE_SCHEMA",
		// Process-wide state and disclosure (security review 2026-09-30).
		"PRAGMA hard_heap_limit=4096", "PRAGMA soft_heap_limit = 1", "PRAGMA database_list",
		"PRAGMA main.hard_heap_limit=1", "PRAGMA temp.table_info(t)", "PRAGMA", "PRAGMA ;",
		"PRAGMA mmap_size=1", "PRAGMA threads=8", "PRAGMA trusted_schema=1",
		"SELECT * FROM pragma_database_list", `SELECT * FROM "pragma_database_list"`,
		"SELECT * FROM pragma_hard_heap_limit(1)",
		// EXPLAIN compiles the statement, and a setting pragma takes effect at
		// compile time (security review 2026-09-30).
		"EXPLAIN PRAGMA hard_heap_limit=1", "EXPLAIN QUERY PLAN PRAGMA writable_schema=1",
		"/*x*/ explain pragma soft_heap_limit=1", "EXPLAIN ATTACH '/x.db' AS x", "EXPLAIN VACUUM INTO '/x.db'",
		// A string literal is a table name in FROM.
		"SELECT * FROM 'pragma_database_list'", "SELECT * FROM 'PRAGMA_COMPILE_OPTIONS'",
		"SELECT 1\x00; VACUUM INTO '/x.db'", "SELECT 1\x01",
	} {
		var na *ErrNotAllowed
		if err := CheckTenantSQLite(q); !errors.As(err, &na) {
			t.Errorf("CheckTenantSQLite(%q) = %v, want ErrNotAllowed", q, err)
		}
	}
}

func TestCheckTenantSQLite_keywordsAsDataAreAllowed(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO w VALUES ('please ATTACH this; now')", "SELECT 'VACUUM INTO x'",
		"SELECT 1 -- ATTACH x", "SELECT 1 /* DETACH */", "SELECT \"attach\", [detach] FROM t",
		"SELECT * FROM t;", "VACUUM", "SELECT into_x, vacuum FROM t", "SELECT 1;;",
		"SELECT * FROM functions", "",
		"PRAGMA table_info(t)", "PRAGMA foreign_keys = ON", "SELECT 'writable_schema'",
		"PRAGMA main.table_info(t)", `PRAGMA "user_version" = 3`, "PRAGMA integrity_check",
		"SELECT * FROM pragma_table_info('t')", "SELECT 'pragma_notes are data'",
		"EXPLAIN PRAGMA table_info(t)", "EXPLAIN QUERY PLAN SELECT * FROM t", "EXPLAIN SELECT 1",
		"SELECT * FROM 'pragma_table_info'",
		"SELECT\t1\r\nFROM t", "SELECT 'hard_heap_limit'",
		"INSERT INTO t VALUES ('temp_store_directory')",
	} {
		if err := CheckTenantSQLite(q); err != nil {
			t.Errorf("CheckTenantSQLite(%q) = %v, want nil", q, err)
		}
	}
}

func TestCheck_vacuumIntoAndAttachLiteral(t *testing.T) {
	if err := Check("VACUUM INTO '/x.db'"); err == nil {
		t.Error("VACUUM INTO must be refused")
	}
	if err := Check("INSERT INTO w VALUES ('please ATTACH this')"); err != nil {
		t.Errorf("a literal mentioning ATTACH was refused: %v", err)
	}
}

// The gateway creates these two at runtime, so no migration-driven test sees
// them, and each holds addresses a node dials: a tenant that could write one
// would point the platform at a host of its choosing.
func TestCheck_refusesTheRuntimeCreatedPeerTables(t *testing.T) {
	for _, table := range []string{"_pubsub_mesh_peers", "_namespace_libp2p_peers"} {
		for _, query := range []string{
			"INSERT INTO " + table + " (peer_id) VALUES ('x')",
			"SELECT * FROM " + table,
			`DELETE FROM "` + table + `"`,
			"UPDATE main." + strings.ToUpper(table) + " SET peer_id = 'y'",
			"SELECT 1 FROM t JOIN " + table + " ON 1",
		} {
			var refused *ErrNotAllowed
			if err := Check(query); !errors.As(err, &refused) {
				t.Errorf("Check(%q) = %v, want a refusal", query, err)
			}
		}
	}
}

// Check holds the table-valued pragma functions to the allowlist CheckTenantSQLite
// does: pragma_database_list returned the RQLite node's file path on the ORM
// routes and to function host calls, while /v1/db/sqlite/query refused it.
func TestCheck_holdsPragmaFunctionsToTheAllowlist(t *testing.T) {
	for _, tc := range []struct {
		query   string
		allowed bool
	}{
		{"SELECT * FROM pragma_database_list", false},
		{`SELECT * FROM "pragma_database_list"`, false},
		{"SELECT * FROM [PRAGMA_DATABASE_LIST]", false},
		{"SELECT * FROM 'pragma_database_list'", false},
		{"SELECT * FROM pragma_compile_options", false},
		{"SELECT * FROM pragma_hard_heap_limit(1)", false},
		{"SELECT name FROM messages WHERE name IN (SELECT name FROM pragma_function_list)", false},
		{"SELECT * FROM main.pragma_module_list", false},
		{"SELECT * FROM pragma_table_info('messages')", true},
		{"SELECT name, pk FROM pragma_table_info('t') WHERE pk > 0", true},
		{"SELECT * FROM pragma_index_list('messages')", true},
		{"SELECT page_count FROM pragma_page_count()", true},
		{"SELECT 'pragma_notes are data'", true},
		{"SELECT * FROM 'pragma_table_info'", true},
	} {
		err := Check(tc.query)
		if tc.allowed {
			if err != nil {
				t.Errorf("Check(%q) = %v, want nil", tc.query, err)
			}
			continue
		}
		var refused *ErrNotAllowed
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "which is not available to tenant SQL") {
			t.Errorf("Check(%q) = %v, want a pragma refusal", tc.query, err)
		}
	}
}
