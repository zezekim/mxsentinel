-- +goose Up
CREATE TABLE dkim_rotation_plans (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id      UUID        NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    selector_old   TEXT        NOT NULL DEFAULT '',
    selector_new   TEXT        NOT NULL,
    public_key_new TEXT        NOT NULL,
    private_key_new TEXT       NOT NULL,
    key_bits       INTEGER     NOT NULL DEFAULT 2048,
    stage          TEXT        NOT NULL DEFAULT 'pending',
    dns_verified   BOOLEAN     NOT NULL DEFAULT false,
    test_passed    BOOLEAN     NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at   TIMESTAMPTZ,
    retired_at     TIMESTAMPTZ,
    UNIQUE (tenant_id, domain_id, selector_new)
);

CREATE INDEX idx_dkim_rotation_domain ON dkim_rotation_plans (domain_id);

-- +goose Down
DROP INDEX IF EXISTS idx_dkim_rotation_domain;
DROP TABLE IF EXISTS dkim_rotation_plans;
