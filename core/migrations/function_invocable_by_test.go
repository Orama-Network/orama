package migrations_test

import (
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"go.uber.org/zap"
)

// invocableByMigration adds functions.invocable_by.
const invocableByMigration = "097_function_invocable_by.sql"

// Migration 097 (feat-971): a row deployed before it names no parent, and so
// does a row a gateway that predates the column inserts during a rolling
// upgrade. A replay of the migrations changes nothing.
func TestInvocableByMigration_oldRowsNameNoParent(t *testing.T) {
	db := openRoundtripDB(t)
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrationsBefore(t, invocableByMigration), zap.NewNop()); err != nil {
		t.Fatalf("apply migrations before 097: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO functions (id, name, namespace, version, wasm_cid, created_by) VALUES ('old', 'fn', 'ns', 1, 'cid', 'ns')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for run := 1; run <= 2; run++ {
		if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
			t.Fatalf("apply 097 (run %d): %v", run, err)
		}
	}
	// An insert that does not name the column: what a gateway older than 097 sends.
	if _, err := db.Exec(`INSERT INTO functions (id, name, namespace, version, wasm_cid, created_by) VALUES ('older-gateway', 'fn', 'ns', 2, 'cid', 'ns')`); err != nil {
		t.Fatalf("an insert without invocable_by was refused: %v", err)
	}
	for _, id := range []string{"old", "older-gateway"} {
		var v string
		if err := db.QueryRow(`SELECT invocable_by FROM functions WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != "" {
			t.Errorf("row %s: invocable_by %q, want '' (no parent)", id, v)
		}
	}
}
