-- =============================================================================
-- 100_device_revocation_proof.sql
--
-- A device's tombstone keeps who revoked it, and what proves it (bugboard
-- feat-976).
--
-- A client told DEVICE_REVOKED may erase itself: its data, its session and its
-- keys. On the gateway's word alone, a misbehaving gateway could erase a
-- healthy device by saying so. The refusal now carries the revocation, so the
-- client can check it with keys it already trusts:
--
-- revoked_by           device:   a device of the account, with its signed
--                                `revoke` proof over the revoked device's id;
--                      account:  a session of the account bound to no device
--                                (the wallet's own, under the optional policy);
--                      operator: a namespace operator (/v1/namespace/devices);
--                      function: one of the namespace's functions (deny_device).
--                      NULL on a device revoked before this migration.
-- revocation_proof     the revoking device's proof, as verified, with its
--                      public key and the namespace it signed (JSON), when
--                      revoked_by is device.
-- revocation_evidence  what the revoker attached for the application to check,
--                      such as an account-signed roster entry: opaque to the
--                      gateway, stored and returned verbatim.
--
-- The columns are written once, by the statement that tombstones the row, and
-- never again: a revocation repeated to finish one that stopped half way keeps
-- the first record. The runner treats a repeated ADD COLUMN as applied.
-- =============================================================================

ALTER TABLE session_devices ADD COLUMN revoked_by TEXT
    CHECK (revoked_by IS NULL OR revoked_by IN ('device', 'account', 'operator', 'function'));
ALTER TABLE session_devices ADD COLUMN revocation_proof TEXT;
ALTER TABLE session_devices ADD COLUMN revocation_evidence TEXT;

INSERT OR IGNORE INTO schema_migrations(version) VALUES (100);
