-- =============================================================================
-- 095_webrtc_video_mute.sql
--
-- A forced mute of a user's video in a WebRTC room (bugboard feat-964).
--
-- webrtc_mute stops the forwarding of a user's audio and records it in the
-- muted column, so it holds when they rejoin. webrtc_mute_video does the same
-- for their video (every video track: camera and screen), separately from the
-- audio, and records it here. Like muted it is set on every admission of the
-- user in the room, and a row that carries it is kept for the mute retention
-- after its admission ends.
--
-- The runner treats a repeated ADD COLUMN as applied, so a re-run changes
-- nothing.
-- =============================================================================

ALTER TABLE webrtc_admissions ADD COLUMN video_muted INTEGER NOT NULL DEFAULT 0;

INSERT OR IGNORE INTO schema_migrations(version) VALUES (95);
