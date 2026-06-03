-- +goose Up
-- Phase 4: write audit log. The API records every mutating request (who/what/outcome)
-- for tenant accountability. credential_id is the API token that performed the action.

CREATE TABLE audit_events (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    credential_id UUID,                       -- the api_credential used (kept even if the cred is later deleted)
    method        TEXT        NOT NULL,
    path          TEXT        NOT NULL,
    status        INTEGER     NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_tenant_time ON audit_events (tenant_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_events;
