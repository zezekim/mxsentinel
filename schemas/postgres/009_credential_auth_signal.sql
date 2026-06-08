-- MX Sentinel — PostgreSQL reference DDL: SASL credential-compromise detection.
--
-- Runnable migration: migrations/postgres/00009_credential_auth_signal.sql (keep in sync).
--
-- authwatchd (cmd/authwatchd) consumes SMTP telemetry keyed by sasl_username and maintains
-- per-credential rolling behavioral stats: a burst of distinct recipient domains
-- (list-blasting), a spam/block bounce-rate spike, and volume far above the credential's
-- recent norm. When the combined score trips it appends a row to credential_auth_signal and
-- opens a critical incident.
--
-- Shared-relay caveat: on a shared cPanel relay every client's mail authenticates with ONE
-- SASL credential from ONE source IP, so per-credential keying is coarse and source-IP
-- geo/ASN anomaly is degenerate (and the submitting client IP isn't on the bus — RelayIP is
-- the egress node). These signals are strongest in dedicated-submission deployments.

CREATE TABLE credential_auth_signal (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    sasl_username TEXT        NOT NULL,
    signal        TEXT        NOT NULL,
    detail        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    detected_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_cred_auth_signal_user ON credential_auth_signal (sasl_username, detected_at DESC);

CREATE TABLE credential_lock (
    sasl_username TEXT        PRIMARY KEY,
    locked        BOOLEAN     NOT NULL DEFAULT FALSE,
    reason        TEXT,
    locked_at     TIMESTAMPTZ
);
