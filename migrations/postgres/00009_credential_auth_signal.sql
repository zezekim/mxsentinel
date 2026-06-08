-- +goose Up
-- SASL credential-compromise detection (cmd/authwatchd). authwatchd consumes SMTP
-- telemetry from the bus keyed by sasl_username and folds it into per-credential rolling
-- behavioral stats: a burst of distinct recipient domains (list-blasting), a spam/block
-- bounce-rate spike, and a volume far above the credential's recent norm. When the combined
-- score trips it records a signal here and opens a critical incident.
--
-- IMPORTANT (shared-relay topology): on a shared cPanel relay ALL submission arrives via ONE
-- SASL credential from ONE source IP, so per-credential keying is coarse and the classic
-- "login from a new geo/ASN" anomaly is degenerate (there's one IP, and the submitting
-- client's source IP isn't even on the event bus — SMTPPayload.RelayIP is the egress node).
-- These signals shine in DEDICATED-SUBMISSION deployments (one credential per end-user).
-- See docs and the authwatchd caveats.

-- credential_auth_signal: an append-only log of detections per credential.
CREATE TABLE credential_auth_signal (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    sasl_username TEXT        NOT NULL,
    signal        TEXT        NOT NULL,           -- e.g. recipient_burst, bounce_spike, volume_spike, off_hours, composite_trip
    detail        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    detected_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_cred_auth_signal_user ON credential_auth_signal (sasl_username, detected_at DESC);

-- credential_lock: the current lock state per credential. locked mirrors the operator's /
-- auto-lock decision; the smtp_users table is the source of truth for whether Dovecot
-- accepts the login (DisableSMTPUserByUsername flips smtp_users.enabled). This table records
-- WHY/WHEN authwatchd or an admin locked it, for the Auth Security UI.
CREATE TABLE credential_lock (
    sasl_username TEXT        PRIMARY KEY,
    locked        BOOLEAN     NOT NULL DEFAULT FALSE,
    reason        TEXT,
    locked_at     TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS credential_auth_signal;
DROP TABLE IF EXISTS credential_lock;
