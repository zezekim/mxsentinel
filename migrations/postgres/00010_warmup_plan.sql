-- +goose Up
-- IP warm-up plan tracking (cmd/warmupd). New sending IPs must ramp volume gradually to build
-- sender reputation with receiving MTAs and filtering services. warmup_plans holds the schedule
-- and current progress for each IP pool under a tenant. warmup_day_stats records the daily
-- delivery outcomes (sent, delivered, bounced, deferred) so the warm-up engine and dashboard
-- can evaluate whether the ramp is healthy and advance or pause accordingly.
--
-- Stages:
--   ramping     - actively incrementing daily volume toward target_daily_volume
--   established - target volume reached and acceptance rate sustained; plan complete
--   paused      - operator or automated rule has halted ramping (e.g. acceptance rate drop)

-- warmup_plans: one row per IP pool warm-up campaign under a tenant.
CREATE TABLE warmup_plans (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                TEXT        NOT NULL,
    ip_pool             TEXT        NOT NULL DEFAULT '',
    target_daily_volume INTEGER     NOT NULL DEFAULT 1000,
    current_day         INTEGER     NOT NULL DEFAULT 1,
    stage               TEXT        NOT NULL DEFAULT 'ramping', -- ramping, established, paused
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_warmup_plans_tenant ON warmup_plans (tenant_id, stage);

-- warmup_day_stats: one row per calendar day per plan, recording volume and acceptance metrics.
CREATE TABLE warmup_day_stats (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id         UUID        NOT NULL REFERENCES warmup_plans(id) ON DELETE CASCADE,
    day_number      INTEGER     NOT NULL,
    stat_date       DATE        NOT NULL,
    sent            INTEGER     NOT NULL DEFAULT 0,
    delivered       INTEGER     NOT NULL DEFAULT 0,
    bounced         INTEGER     NOT NULL DEFAULT 0,
    deferred        INTEGER     NOT NULL DEFAULT 0,
    acceptance_rate NUMERIC(5,4),
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id, day_number)
);
CREATE INDEX idx_warmup_day_stats_plan_date ON warmup_day_stats (plan_id, stat_date DESC);

-- +goose Down
DROP TABLE IF EXISTS warmup_day_stats;
DROP TABLE IF EXISTS warmup_plans;
