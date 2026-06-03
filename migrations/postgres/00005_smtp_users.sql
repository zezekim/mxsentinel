-- +goose Up
-- SMTP submission users: SASL credentials that smarthost clients (cPanel/Exim/apps)
-- use to authenticate to the Postfix relay on the submission port (587). The relay's
-- Dovecot SASL passdb reads this table directly (see deploy/install.sh, docs/smarthost.md).
--
-- Management is tenant-scoped (each tenant CRUDs its own rows via the dashboard/API),
-- but `username` is globally UNIQUE because SASL authentication is not tenant-aware:
-- Dovecot looks a login up across the whole table. Usernames are typically full
-- addresses (e.g. mailer@send.example.com), which are unique anyway.
--
-- password_hash holds a bcrypt hash ($2a$…). Dovecot is configured with
-- default_pass_scheme = BLF-CRYPT so it verifies the hash without a scheme prefix.

CREATE TABLE smtp_users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    username      CITEXT      NOT NULL UNIQUE,   -- SASL login (Dovecot %u lookup key)
    password_hash TEXT        NOT NULL,          -- bcrypt; verified by Dovecot as BLF-CRYPT
    domain        TEXT,                          -- optional: associated sending domain (informational)
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_smtp_users_tenant ON smtp_users (tenant_id);

-- +goose Down
DROP TABLE IF EXISTS smtp_users;
