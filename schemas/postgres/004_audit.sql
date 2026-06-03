-- MX Sentinel — PostgreSQL reference DDL: audit_events (Phase 4 write audit log).
--
-- Runnable migration: migrations/postgres/00004_audit.sql (keep in sync).
--
-- The API records every mutating request (method/path/status + tenant + the API
-- credential that performed it) for tenant accountability. Surfaced at GET /v1/audit.

CREATE TABLE audit_events (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    credential_id UUID,                       -- api_credential used (retained even if the cred is deleted)
    method        TEXT        NOT NULL,
    path          TEXT        NOT NULL,
    status        INTEGER     NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_tenant_time ON audit_events (tenant_id, created_at DESC);
