-- +goose Up
-- TLS-RPT + MTA-STS monitoring (the transport-security layer).
--
-- mtasts_snapshots  : per-domain MTA-STS policy posture, captured by tlsrptd on each poll.
--                     A new row is written only when the state checksum changes (drift), so
--                     the table is a versioned timeline like dns_snapshots. `state` is the
--                     serialized mtasts.State; `findings` is the []DNSFinding (mta_sts
--                     category) that also rides the dns.validation_failed event.
-- tlsrpt_reports    : pointer/index rows for raw TLS-RPT JSON reports archived in object
--                     storage (per-MTA detail rows live in ClickHouse tlsrpt_results). One
--                     report per (tenant, report_id); re-ingesting a report is a no-op.
--
-- No message content is ever stored here — only policy/transport metadata and counts.

CREATE TABLE mtasts_snapshots (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id    UUID        REFERENCES domains(id) ON DELETE CASCADE,
    domain_name  TEXT        NOT NULL DEFAULT '',
    mode         TEXT        NOT NULL DEFAULT '',   -- none|testing|enforce|'' (no policy)
    max_age      INTEGER     NOT NULL DEFAULT 0,
    mx_hosts     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    checksum     TEXT        NOT NULL,
    cert_expiry  TIMESTAMPTZ,                        -- earliest MX cert NotAfter (nullable)
    is_healthy   BOOLEAN     NOT NULL DEFAULT true,
    state        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    findings     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    captured_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mtasts_snapshots_domain  ON mtasts_snapshots (domain_id, captured_at DESC);
CREATE INDEX idx_mtasts_snapshots_tenant  ON mtasts_snapshots (tenant_id, captured_at DESC);

CREATE TABLE tlsrpt_reports (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id     UUID        REFERENCES domains(id) ON DELETE SET NULL,
    domain_name   TEXT        NOT NULL DEFAULT '',
    org_name      TEXT        NOT NULL DEFAULT '',
    report_id     TEXT        NOT NULL,
    date_begin    TIMESTAMPTZ,
    date_end      TIMESTAMPTZ,
    object_key    TEXT        NOT NULL DEFAULT '',
    policy_count  INTEGER     NOT NULL DEFAULT 0,
    success_count BIGINT      NOT NULL DEFAULT 0,
    failure_count BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, report_id)
);

CREATE INDEX idx_tlsrpt_reports_tenant ON tlsrpt_reports (tenant_id, date_begin DESC);
CREATE INDEX idx_tlsrpt_reports_domain ON tlsrpt_reports (domain_id);

-- +goose Down
DROP TABLE IF EXISTS tlsrpt_reports;
DROP TABLE IF EXISTS mtasts_snapshots;
