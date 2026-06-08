-- MX Sentinel — PostgreSQL reference DDL: fbl_complaints + domain_reputation
-- (feedback-loop complaints and Gmail Postmaster reputation -> per-sender reputation).
--
-- Runnable migration: migrations/postgres/00008_fbl_reputation.sql (keep in sync).
--
-- fbl_complaints holds one row per parsed ARF complaint email (RFC 5965 feedback-report);
-- the drop directory is fed by abuse@ mailboxes that providers send FBL reports to after
-- you enroll each sending IP/domain. domain_reputation holds the latest Gmail Postmaster
-- Tools grade + user-reported spam rate per domain (refreshed only when
-- GOOGLE_POSTMASTER_TOKEN is configured). Both feed /v1/reputation and the abuse rollup.

CREATE TABLE fbl_complaints (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    feedback_type TEXT,
    sender_domain TEXT,
    sasl_username TEXT,
    provider      TEXT,
    message_id    TEXT
);
CREATE INDEX idx_fbl_complaints_domain_time ON fbl_complaints (sender_domain, received_at DESC);

CREATE TABLE domain_reputation (
    domain     TEXT PRIMARY KEY,
    source     TEXT,
    reputation TEXT,
    spam_rate  DOUBLE PRECISION,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
