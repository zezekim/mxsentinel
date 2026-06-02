-- +goose Up
-- Derived from schemas/postgres/001_init.sql — keep in sync.
-- See docs/data-model.md for the entity model.

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TYPE tenant_status   AS ENUM ('active', 'suspended', 'trial', 'closed');
CREATE TYPE tenant_kind     AS ENUM ('hosting_provider', 'msp', 'enterprise');
CREATE TYPE user_role       AS ENUM ('owner', 'admin', 'operator', 'viewer');
CREATE TYPE user_status     AS ENUM ('active', 'invited', 'disabled');
CREATE TYPE domain_status   AS ENUM ('pending_verification', 'monitored', 'paused', 'error');
CREATE TYPE dns_finding_sev AS ENUM ('info', 'warning', 'critical');
CREATE TYPE dns_category    AS ENUM (
    'spf', 'dkim', 'dmarc', 'mx', 'ptr', 'a_aaaa',
    'dnssec', 'mta_sts', 'tls_rpt', 'smtp_tls', 'bimi', 'arc'
);
CREATE TYPE ip_pool_purpose AS ENUM ('transactional', 'marketing', 'warmup', 'mixed');
CREATE TYPE channel_kind    AS ENUM ('email', 'slack', 'webhook', 'pagerduty');
CREATE TYPE alert_signal    AS ENUM (
    'dns_breakage', 'rejection_spike', 'blacklist_hit',
    'tls_failure', 'complaint_spike', 'bounce_rate'
);
CREATE TYPE alert_state     AS ENUM ('open', 'acknowledged', 'resolved', 'muted');

CREATE TABLE tenants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT          NOT NULL,
    slug       CITEXT        NOT NULL UNIQUE,
    kind       tenant_kind   NOT NULL DEFAULT 'enterprise',
    status     tenant_status NOT NULL DEFAULT 'trial',
    settings   JSONB         NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email         CITEXT      NOT NULL,
    display_name  TEXT,
    role          user_role   NOT NULL DEFAULT 'viewer',
    status        user_status NOT NULL DEFAULT 'invited',
    password_hash TEXT,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);

CREATE TABLE api_credentials (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    token_prefix TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL,
    scopes       TEXT[]      NOT NULL DEFAULT '{}',
    created_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_api_credentials_prefix ON api_credentials (token_prefix);

CREATE TABLE notification_channels (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    kind       channel_kind NOT NULL,
    name       TEXT         NOT NULL,
    config     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    enabled    BOOLEAN      NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE domains (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID          NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                CITEXT        NOT NULL,
    status              domain_status NOT NULL DEFAULT 'pending_verification',
    verification_token  TEXT,
    verified_at         TIMESTAMPTZ,
    check_interval_secs INTEGER       NOT NULL DEFAULT 300 CHECK (check_interval_secs >= 60),
    last_checked_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_domains_tenant ON domains (tenant_id);

CREATE TABLE dns_snapshots (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id   UUID        NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    state       JSONB       NOT NULL,
    checksum    TEXT        NOT NULL,
    is_healthy  BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_dns_snapshots_domain_time ON dns_snapshots (domain_id, captured_at DESC);

CREATE TABLE dns_findings (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    snapshot_id UUID            NOT NULL REFERENCES dns_snapshots(id) ON DELETE CASCADE,
    domain_id   UUID            NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    category    dns_category    NOT NULL,
    severity    dns_finding_sev NOT NULL,
    code        TEXT            NOT NULL,
    message     TEXT            NOT NULL,
    detail      JSONB           NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ     NOT NULL DEFAULT now()
);
CREATE INDEX idx_dns_findings_snapshot ON dns_findings (snapshot_id);
CREATE INDEX idx_dns_findings_domain_sev ON dns_findings (domain_id, severity);

CREATE TABLE ip_pools (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID            NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         TEXT            NOT NULL,
    purpose      ip_pool_purpose NOT NULL DEFAULT 'mixed',
    addresses    INET[]          NOT NULL DEFAULT '{}',
    warmup_stage INTEGER,
    created_at   TIMESTAMPTZ     NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE relay_nodes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        REFERENCES tenants(id) ON DELETE CASCADE,
    hostname     TEXT        NOT NULL UNIQUE,
    primary_ip   INET,
    ip_pool_id   UUID        REFERENCES ip_pools(id) ON DELETE SET NULL,
    software     TEXT,
    last_seen_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alert_rules (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT         NOT NULL,
    signal      alert_signal NOT NULL,
    condition   JSONB        NOT NULL,
    channel_ids UUID[]       NOT NULL DEFAULT '{}',
    enabled     BOOLEAN      NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_alert_rules_tenant ON alert_rules (tenant_id) WHERE enabled;

CREATE TABLE alert_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    rule_id      UUID        NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    state        alert_state NOT NULL DEFAULT 'open',
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at  TIMESTAMPTZ,
    payload      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_alert_events_tenant_time ON alert_events (tenant_id, triggered_at DESC);
CREATE INDEX idx_alert_events_open ON alert_events (tenant_id) WHERE state = 'open';

CREATE TABLE dmarc_reports (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id    UUID        REFERENCES domains(id) ON DELETE SET NULL,
    org_name     TEXT,
    report_id    TEXT,
    date_begin   TIMESTAMPTZ,
    date_end     TIMESTAMPTZ,
    object_key   TEXT        NOT NULL,
    record_count INTEGER     NOT NULL DEFAULT 0,
    ingested_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, org_name, report_id)
);
CREATE INDEX idx_dmarc_reports_domain_time ON dmarc_reports (domain_id, date_begin DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_tenants_updated    BEFORE UPDATE ON tenants     FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_users_updated      BEFORE UPDATE ON users       FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_domains_updated    BEFORE UPDATE ON domains     FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_alertrules_updated BEFORE UPDATE ON alert_rules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS dmarc_reports CASCADE;
DROP TABLE IF EXISTS alert_events CASCADE;
DROP TABLE IF EXISTS alert_rules CASCADE;
DROP TABLE IF EXISTS relay_nodes CASCADE;
DROP TABLE IF EXISTS ip_pools CASCADE;
DROP TABLE IF EXISTS dns_findings CASCADE;
DROP TABLE IF EXISTS dns_snapshots CASCADE;
DROP TABLE IF EXISTS domains CASCADE;
DROP TABLE IF EXISTS notification_channels CASCADE;
DROP TABLE IF EXISTS api_credentials CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS tenants CASCADE;

DROP FUNCTION IF EXISTS set_updated_at();

DROP TYPE IF EXISTS alert_state;
DROP TYPE IF EXISTS alert_signal;
DROP TYPE IF EXISTS channel_kind;
DROP TYPE IF EXISTS ip_pool_purpose;
DROP TYPE IF EXISTS dns_category;
DROP TYPE IF EXISTS dns_finding_sev;
DROP TYPE IF EXISTS domain_status;
DROP TYPE IF EXISTS user_status;
DROP TYPE IF EXISTS user_role;
DROP TYPE IF EXISTS tenant_kind;
DROP TYPE IF EXISTS tenant_status;
