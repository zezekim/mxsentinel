-- MX Sentinel — PostgreSQL reference DDL: incidents (Phase 2 intelligence output).
--
-- This is the human-readable spec; the runnable migration is
-- migrations/postgres/00002_incidents.sql (keep in sync).
--
-- Incidents are persisted, queryable correlations/anomalies. incidentd writes one per
-- source event (idempotent on (tenant_id, source_event_id)) from reputation.* and
-- dns.validation_failed bus events; the API surfaces them at /v1/incidents.

CREATE TYPE incident_kind   AS ENUM ('rejection_spike', 'blacklist', 'dns_validation', 'other');
CREATE TYPE incident_status AS ENUM ('open', 'acknowledged', 'resolved');

CREATE TABLE incidents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_event_id UUID            NOT NULL,                 -- the bus event that created it
    kind            incident_kind   NOT NULL,
    severity        dns_finding_sev NOT NULL,                 -- info | warning | critical
    domain          CITEXT,
    subject         TEXT,                                     -- ip / domain the incident concerns
    title           TEXT            NOT NULL,
    detail          JSONB           NOT NULL DEFAULT '{}'::jsonb,
    status          incident_status NOT NULL DEFAULT 'open',
    confidence      REAL,                                     -- correlation confidence, when applicable
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    UNIQUE (tenant_id, source_event_id)
);
CREATE INDEX idx_incidents_tenant_time ON incidents (tenant_id, created_at DESC);
CREATE INDEX idx_incidents_open ON incidents (tenant_id) WHERE status = 'open';
