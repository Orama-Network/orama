-- =============================================================================
-- 094_device_attestation_signing.sql
--
-- An Android device's record keeps what its attestation proved that the
-- namespace's policy can later narrow (bugboard feat-965), so a narrowed
-- policy reaches a device that attested before it, as a raised minimum
-- version already does:
--
--   signing_cert_digests  JSON array of the lowercase-hex SHA-256 digests of
--                         the certificates the attested app is signed with
--   os_patch_level        the attested OS security patch level, YYYYMM; 0
--                         when the device attested none
--
-- The access token carries both in its `att` claim, and every request is
-- judged with them against the policy as it is now: a digest removed from
-- android_signing_digests, or a narrower android_max_patch_age_months, refuses
-- the device's tokens within the policy reload.
--
-- No backfill. An existing row's digests were never recorded and cannot be
-- recovered from it, so it keeps the default '[]', and a record with no digest
-- is judged ATTESTATION_REQUIRED: the device attests again, once, and the new
-- record carries them. Passing such a row would admit exactly the device this
-- change exists to refuse, one signed with a key since removed from the
-- policy. An iOS row keeps '[]' and 0; neither applies to it.
--
-- The runner treats a repeated ADD COLUMN as applied, so a re-run changes
-- nothing.
-- =============================================================================

ALTER TABLE device_attestations ADD COLUMN signing_cert_digests TEXT NOT NULL DEFAULT '[]';
ALTER TABLE device_attestations ADD COLUMN os_patch_level INTEGER NOT NULL DEFAULT 0;

INSERT OR IGNORE INTO schema_migrations(version) VALUES (94);
