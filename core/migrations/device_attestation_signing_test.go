package migrations_test

import (
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"go.uber.org/zap"
)

// attestationSigningMigration adds the attested signing digests and patch
// level to device_attestations.
const attestationSigningMigration = "094_device_attestation_signing.sql"

// Migration 094 (feat-965): a row recorded before it keeps no signing digest
// ('[]') and no patch level (0), which the gateway judges "attest again"; a
// replay of the migrations changes nothing.
func TestAttestationSigningMigration_oldRowsRecordNoDigest(t *testing.T) {
	db := openRoundtripDB(t)
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrationsBefore(t, attestationSigningMigration), zap.NewNop()); err != nil {
		t.Fatalf("apply migrations before 094: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO namespaces(id, name) VALUES (10, 'anchat')`); err != nil {
		t.Fatalf("seed the namespace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO device_attestations(namespace_id, device_id, platform, app_id, app_version, attested_at)
		VALUES (10, 'device', 'android', 'chat.anchat', '412', datetime('now'))`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for run := 1; run <= 2; run++ {
		if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
			t.Fatalf("apply 094 (run %d): %v", run, err)
		}
	}

	var digests string
	var patch int
	if err := db.QueryRow(`SELECT signing_cert_digests, os_patch_level FROM device_attestations WHERE device_id = 'device'`).Scan(&digests, &patch); err != nil {
		t.Fatal(err)
	}
	if digests != "[]" || patch != 0 {
		t.Errorf("a row recorded before 094 has digests %q and patch level %d; want [] and 0", digests, patch)
	}
}
