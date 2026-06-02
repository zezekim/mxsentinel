-- +goose Up
-- Incidents: persisted, queryable correlations/anomalies produced by the intelligence
-- layer (Phase 2). incidentd writes these from reputation.* and dns.validation_failed
-- events; the API surfaces them. See docs/phase-1-plan.md / ARCHITECTURE.md §4-5.

CREATE TYPE incident_kind   AS ENUM ('rejection_spike', 'blacklist', 'dns_validation', 'other');
CREATE TYPE incident_status AS ENUM ('open', 'acknowledged', 'resolved');

CREATE TABLE incidents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_event_id UUID            NOT NULL,
    kind            incident_kind   NOT NULL,
    severity        dns_finding_sev NOT NULL,   -- reuse info/warning/critical
    domain          CITEXT,
    subject         TEXT,
    title           TEXT            NOT NULL,
    detail          JSONB           NOT NULL DEFAULT '{}'::jsonb,
    status          incident_status NOT NULL DEFAULT 'open',
    confidence      REAL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    -- one incident per source event keeps the at-least-once consumer idempotent
    UNIQUE (tenant_id, source_event_id)
);
CREATE INDEX idx_incidents_tenant_time ON incidents (tenant_id, created_at DESC);
CREATE INDEX idx_incidents_open ON incidents (tenant_id) WHERE status = 'open';

CREATE TRIGGER trg_incidents_updated BEFORE UPDATE ON incidents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS incidents CASCADE;
DROP TYPE IF EXISTS incident_status;
DROP TYPE IF EXISTS incident_kind;
