package migrations_test

import (
	"database/sql"
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"go.uber.org/zap"
)

// deviceRevocationProofMigration keeps who revoked a device, and its proof.
const deviceRevocationProofMigration = "100_device_revocation_proof.sql"

// Migration 100 (feat-976): a device revoked before it keeps its tombstone with
// no record of who revoked it; a new revocation stores one of the four kinds
// and nothing else. A replay changes nothing.
func TestDeviceRevocationProofMigration_keepsTombstonesAndChecksTheKind(t *testing.T) {
	db := openRoundtripDB(t)
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrationsBefore(t, deviceRevocationProofMigration), zap.NewNop()); err != nil {
		t.Fatalf("apply migrations before 100: %v", err)
	}
	for _, seed := range []string{
		`INSERT INTO namespaces (id, name) VALUES (7, 'ns-a')`,
		`INSERT INTO session_devices (id, namespace_id, subject, public_key, state, revoked_at)
		 VALUES ('old', 7, '0xa11ce', '{}', 'revoked', datetime('now')), ('live', 7, '0xa11ce', '{}', 'active', NULL)`,
	} {
		if _, err := db.Exec(seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for run := 1; run <= 2; run++ {
		if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
			t.Fatalf("apply 100 (run %d): %v", run, err)
		}
	}

	var by, proof, evidence sql.NullString
	var state string
	if err := db.QueryRow(`SELECT state, revoked_by, revocation_proof, revocation_evidence FROM session_devices WHERE id = 'old'`).
		Scan(&state, &by, &proof, &evidence); err != nil {
		t.Fatalf("the tombstone did not survive: %v", err)
	}
	if state != "revoked" || by.Valid || proof.Valid || evidence.Valid {
		t.Errorf("an old tombstone reads %s by %v proof %v evidence %v, want revoked with nothing recorded", state, by, proof, evidence)
	}
	for _, kind := range []string{"device", "account", "operator", "function"} {
		if _, err := db.Exec(`UPDATE session_devices SET revoked_by = ? WHERE id = 'live'`, kind); err != nil {
			t.Errorf("revoked_by %q was refused: %v", kind, err)
		}
	}
	if _, err := db.Exec(`UPDATE session_devices SET revoked_by = 'gateway' WHERE id = 'live'`); err == nil {
		t.Error("a revoked_by outside the four was stored")
	}
}
