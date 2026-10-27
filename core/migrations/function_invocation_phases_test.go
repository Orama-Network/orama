package migrations_test

import (
	"database/sql"
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"go.uber.org/zap"
)

// invocationPhasesMigration adds where an invocation's duration went.
const invocationPhasesMigration = "096_function_invocation_phases.sql"

// Migration 096 (bug-963): a record written before it keeps NULL phases, a
// new record stores them, and a replay of the migrations changes nothing.
func TestInvocationPhasesMigration_nullForOldRecordsAndStoredForNew(t *testing.T) {
	db := openRoundtripDB(t)
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrationsBefore(t, invocationPhasesMigration), zap.NewNop()); err != nil {
		t.Fatalf("apply migrations before 096: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO functions (id, name, namespace, version, wasm_cid, created_by) VALUES ('fn', 'fn', 'ns', 1, 'cid', 'ns')`); err != nil {
		t.Fatalf("seed function: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO function_invocations (id, function_id, request_id, trigger_type, started_at, duration_ms, status) VALUES ('old', 'fn', 'r1', 'http', CURRENT_TIMESTAMP, 5, 'success')`); err != nil {
		t.Fatalf("seed invocation: %v", err)
	}
	for run := 1; run <= 2; run++ {
		if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
			t.Fatalf("apply 096 (run %d): %v", run, err)
		}
	}

	var node sql.NullString
	var queue, load, calls, dbMS sql.NullInt64
	if err := db.QueryRow(`SELECT node_id, queue_ms, load_ms, db_calls, db_ms FROM function_invocations WHERE id = 'old'`).Scan(&node, &queue, &load, &calls, &dbMS); err != nil {
		t.Fatal(err)
	}
	if node.Valid || queue.Valid || load.Valid || calls.Valid || dbMS.Valid {
		t.Errorf("a record written before 096 has phases: %v %v %v %v %v", node, queue, load, calls, dbMS)
	}

	if _, err := db.Exec(`INSERT INTO function_invocations (id, function_id, request_id, trigger_type, started_at, duration_ms, status, node_id, queue_ms, load_ms, db_calls, db_ms) VALUES ('new', 'fn', 'r2', 'http', CURRENT_TIMESTAMP, 240, 'success', 'peer-x', 3, 0, 2, 210)`); err != nil {
		t.Fatalf("a record with phases was refused: %v", err)
	}
	if err := db.QueryRow(`SELECT node_id, db_calls, db_ms FROM function_invocations WHERE id = 'new'`).Scan(&node, &calls, &dbMS); err != nil {
		t.Fatal(err)
	}
	if node.String != "peer-x" || calls.Int64 != 2 || dbMS.Int64 != 210 {
		t.Errorf("stored phases read back as %v %v %v", node, calls, dbMS)
	}
}
