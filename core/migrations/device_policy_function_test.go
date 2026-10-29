package migrations_test

import (
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"go.uber.org/zap"
)

// devicePolicyFunctionMigration admits the `function` device policy and adds
// functions.open_to_pending_devices.
const devicePolicyFunctionMigration = "099_device_policy_function.sql"

// Migration 099 (feat-973): the session policy table is rebuilt with every row
// and column kept, accepts `function` and still refuses anything else; a
// function deployed before it is closed to pending devices. A replay changes
// nothing.
func TestDevicePolicyFunctionMigration_keepsRowsAndAdmitsFunction(t *testing.T) {
	db := openRoundtripDB(t)
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrationsBefore(t, devicePolicyFunctionMigration), zap.NewNop()); err != nil {
		t.Fatalf("apply migrations before 099: %v", err)
	}
	for _, seed := range []string{
		`INSERT INTO namespaces (id, name) VALUES (7, 'ns-a'), (8, 'ns-b')`,
		`INSERT INTO namespace_session_policy (namespace_id, device_policy, updated_by, sign_in, end_user_topics, per_ip_limits, new_wallets_per_ip_hour, invokes_per_ip_minute)
		 VALUES (7, 'approval', 'owner', 'open', '["chat.*"]', 'on', 5, 60)`,
		`INSERT INTO functions (id, name, namespace, version, wasm_cid, created_by) VALUES ('old', 'fn', 'ns-a', 1, 'cid', 'ns-a')`,
	} {
		if _, err := db.Exec(seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for run := 1; run <= 2; run++ {
		if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
			t.Fatalf("apply 099 (run %d): %v", run, err)
		}
	}

	var policy, by, signIn, topics, perIP string
	var wallets, invokes int
	if err := db.QueryRow(`SELECT device_policy, updated_by, sign_in, end_user_topics, per_ip_limits, new_wallets_per_ip_hour, invokes_per_ip_minute
		FROM namespace_session_policy WHERE namespace_id = 7`).Scan(&policy, &by, &signIn, &topics, &perIP, &wallets, &invokes); err != nil {
		t.Fatalf("the policy row did not survive the rebuild: %v", err)
	}
	if policy != "approval" || by != "owner" || signIn != "open" || topics != `["chat.*"]` || perIP != "on" || wallets != 5 || invokes != 60 {
		t.Errorf("the rebuilt row is %s %s %s %s %s %d %d", policy, by, signIn, topics, perIP, wallets, invokes)
	}
	if _, err := db.Exec(`INSERT INTO namespace_session_policy (namespace_id, device_policy, updated_by) VALUES (8, 'function', 'owner')`); err != nil {
		t.Errorf("the function policy was refused: %v", err)
	}
	if _, err := db.Exec(`UPDATE namespace_session_policy SET device_policy = 'sometimes' WHERE namespace_id = 8`); err == nil {
		t.Error("a device policy outside the four was stored")
	}

	var open bool
	if err := db.QueryRow(`SELECT open_to_pending_devices FROM functions WHERE id = 'old'`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open {
		t.Error("a function deployed before 099 is open to pending devices; want closed")
	}
}

// Migration 099 rebuilds a table (CREATE, copy, DROP, RENAME), which is safe
// only because the runner applies a migration file as one transaction: a
// failure anywhere in it, here its last statement, leaves the original table
// with its rows and its old CHECK, and no half-built copy, so the next run
// applies the whole file.
func TestDevicePolicyFunctionMigration_aFailurePartwayLeavesTheTableWhole(t *testing.T) {
	db := openRoundtripDB(t)
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrationsBefore(t, devicePolicyFunctionMigration), zap.NewNop()); err != nil {
		t.Fatalf("apply migrations before 099: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO namespaces (id, name) VALUES (7, 'ns-a')`,
		`INSERT INTO namespace_session_policy (namespace_id, device_policy, updated_by) VALUES (7, 'approval', 'owner')`,
		// The statement after the rebuild, ADD COLUMN on functions, fails.
		`ALTER TABLE functions RENAME TO functions_aside`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err == nil {
		t.Fatal("099 applied with no functions table")
	}
	var policy string
	if err := db.QueryRow(`SELECT device_policy FROM namespace_session_policy WHERE namespace_id = 7`).Scan(&policy); err != nil || policy != "approval" {
		t.Fatalf("after the failed run the policy row is %q, %v", policy, err)
	}
	var copies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'namespace_session_policy_new'`).Scan(&copies); err != nil || copies != 0 {
		t.Errorf("the failed run left %d half-built copies (%v)", copies, err)
	}
	if _, err := db.Exec(`UPDATE namespace_session_policy SET device_policy = 'function' WHERE namespace_id = 7`); err == nil {
		t.Error("the failed run changed the CHECK")
	}

	if _, err := db.Exec(`ALTER TABLE functions_aside RENAME TO functions`); err != nil {
		t.Fatal(err)
	}
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
		t.Fatalf("the next run: %v", err)
	}
	if _, err := db.Exec(`UPDATE namespace_session_policy SET device_policy = 'function' WHERE namespace_id = 7`); err != nil {
		t.Errorf("after the next run the function policy is refused: %v", err)
	}
}
