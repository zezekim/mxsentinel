-- MX Sentinel — PostgreSQL reference DDL: smtp_users (SMTP submission SASL credentials).
--
-- Runnable migration: migrations/postgres/00005_smtp_users.sql (keep in sync).
--
-- SASL credentials that smarthost clients use to authenticate to the Postfix relay on
-- the submission port (587). The relay's Dovecot SASL passdb reads this table directly
-- (see deploy/install.sh, docs/smarthost.md). Management is tenant-scoped, but `username`
-- is globally UNIQUE because SASL auth is not tenant-aware — Dovecot looks a login up
-- across the whole table. password_hash is a bcrypt hash verified by Dovecot as BLF-CRYPT.
--
-- password_enc is an AES-256-GCM sealed copy of the same password ("v1:" prefix), written
-- only when MXS_ENCRYPTION_KEY is configured. It exists solely for Roundcube autologin —
-- IMAP cannot authenticate against a bcrypt hash, so apid unseals it to mint a webmail
-- session. NULL means webmail autologin is unavailable for that user (no key configured, or
-- the row predates the feature and has not had a password reset since).
-- See docs/webmail-autologin.md and migrations/postgres/00028_smtp_user_webmail.sql.

CREATE TABLE smtp_users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    username      CITEXT      NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    password_enc  TEXT,
    domain        TEXT,
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_smtp_users_tenant ON smtp_users (tenant_id);
