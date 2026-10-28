-- =============================================================================
-- 099_device_policy_function.sql
--
-- A namespace may admit its users' new devices itself (bugboard feat-973).
--
-- Under the `approval` policy a device beyond an account's first holds no
-- session until another of the account's devices approves the gateway's user
-- code. An application that runs its own admission (a signed join request
-- answered from an existing device, or a recovery check) cannot use that: the
-- waiting device has no session to run the application's protocol with, and an
-- account that lost every device can only be recovered by an operator.
--
-- The `function` device policy is `required`, and a device beyond the
-- account's first starts pending with a restricted session: it reaches only the
-- functions deployed with `open_to_pending_devices`, its own session and its
-- own attestation, until one of the namespace's functions calls approve_device
-- (or deny_device).
--
-- namespace_session_policy.device_policy is CHECK-constrained, and SQLite
-- cannot change a CHECK in place, so the table is rebuilt with every column and
-- row carried across. The copy is INSERT OR IGNORE into a table with the same
-- columns, so a re-run copies the table onto itself and changes nothing.
--
-- functions.open_to_pending_devices is the function.yaml field of the same
-- name. It defaults to false: a function deployed before this migration, or
-- without the field, is refused to a pending device. The runner treats a
-- repeated ADD COLUMN as applied.
-- =============================================================================

CREATE TABLE IF NOT EXISTS namespace_session_policy_new (
    namespace_id  INTEGER PRIMARY KEY,
    -- optional: a device is bound when the client sends one.
    -- required: every end-user session is bound to a device.
    -- approval: as required, and a device beyond an account's first needs an
    --           existing device to approve it, holding no session until then.
    -- function: as required, and a device beyond an account's first holds a
    --           pending session until one of the namespace's functions
    --           approves it (approve_device).
    device_policy TEXT NOT NULL CHECK (device_policy IN ('optional', 'required', 'approval', 'function')),
    updated_by    TEXT NOT NULL,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sign_in       TEXT NOT NULL DEFAULT 'members' CHECK (sign_in IN ('members','open')),
    end_user_topics TEXT NOT NULL DEFAULT '[]',
    per_ip_limits TEXT NOT NULL DEFAULT 'off' CHECK (per_ip_limits IN ('off','on')),
    new_wallets_per_ip_hour INTEGER NOT NULL DEFAULT 20,
    invokes_per_ip_minute INTEGER NOT NULL DEFAULT 1200,
    FOREIGN KEY(namespace_id) REFERENCES namespaces(id) ON DELETE CASCADE
);

INSERT OR IGNORE INTO namespace_session_policy_new (
    namespace_id, device_policy, updated_by, updated_at, sign_in, end_user_topics,
    per_ip_limits, new_wallets_per_ip_hour, invokes_per_ip_minute
)
SELECT
    namespace_id, device_policy, updated_by, updated_at, sign_in, end_user_topics,
    per_ip_limits, new_wallets_per_ip_hour, invokes_per_ip_minute
FROM namespace_session_policy;

DROP TABLE IF EXISTS namespace_session_policy;
ALTER TABLE namespace_session_policy_new RENAME TO namespace_session_policy;

ALTER TABLE functions ADD COLUMN open_to_pending_devices BOOLEAN NOT NULL DEFAULT FALSE;

INSERT OR IGNORE INTO schema_migrations(version) VALUES (99);
