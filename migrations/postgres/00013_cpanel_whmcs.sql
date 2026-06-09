-- +goose Up

CREATE TYPE cpanel_sync_status AS ENUM ('pending', 'syncing', 'ok', 'error');

CREATE TABLE cpanel_servers (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    label          TEXT NOT NULL,
    hostname       TEXT NOT NULL,
    port           INTEGER NOT NULL DEFAULT 2087,
    username       TEXT NOT NULL,
    api_token      TEXT NOT NULL,
    verify_ssl     BOOLEAN NOT NULL DEFAULT true,
    sync_interval  INTEGER NOT NULL DEFAULT 14400,
    last_synced_at TIMESTAMPTZ,
    sync_status    cpanel_sync_status NOT NULL DEFAULT 'pending',
    sync_error     TEXT,
    account_count  INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, hostname, username)
);
CREATE INDEX idx_cpanel_servers_tenant ON cpanel_servers(tenant_id);

CREATE TABLE cpanel_accounts (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id      UUID NOT NULL REFERENCES cpanel_servers(id) ON DELETE CASCADE,
    username       TEXT NOT NULL,
    primary_domain CITEXT NOT NULL,
    owner_email    TEXT NOT NULL DEFAULT '',
    plan           TEXT NOT NULL DEFAULT '',
    suspended      BOOLEAN NOT NULL DEFAULT false,
    disk_used_mb   INTEGER NOT NULL DEFAULT 0,
    synced_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, username)
);
CREATE INDEX idx_cpanel_accounts_server ON cpanel_accounts(server_id);
CREATE INDEX idx_cpanel_accounts_domain ON cpanel_accounts(primary_domain);

CREATE TABLE cpanel_domains (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES cpanel_accounts(id) ON DELETE CASCADE,
    domain      CITEXT NOT NULL,
    domain_type TEXT NOT NULL DEFAULT 'main',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, domain)
);
CREATE INDEX idx_cpanel_domains_domain ON cpanel_domains(domain);

CREATE TABLE whmcs_connections (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    label           TEXT NOT NULL,
    api_url         TEXT NOT NULL,
    api_identifier  TEXT NOT NULL,
    api_secret      TEXT NOT NULL,
    push_frequency  TEXT NOT NULL DEFAULT 'daily',
    push_metric_fields TEXT[] NOT NULL DEFAULT ARRAY['delivered','bounced','rejected','total'],
    enabled         BOOLEAN NOT NULL DEFAULT true,
    last_pushed_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, api_url)
);

CREATE TABLE whmcs_push_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id   UUID NOT NULL REFERENCES whmcs_connections(id) ON DELETE CASCADE,
    pushed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    accounts_pushed INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'ok',
    error_detail    TEXT,
    period_start    TIMESTAMPTZ NOT NULL,
    period_end      TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_whmcs_push_log_conn ON whmcs_push_log(connection_id, pushed_at DESC);

-- +goose Down
DROP TABLE IF EXISTS whmcs_push_log;
DROP TABLE IF EXISTS whmcs_connections;
DROP TABLE IF EXISTS cpanel_domains;
DROP TABLE IF EXISTS cpanel_accounts;
DROP TABLE IF EXISTS cpanel_servers;
DROP TYPE IF EXISTS cpanel_sync_status;
