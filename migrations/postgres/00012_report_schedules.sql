-- +goose Up
-- report_schedules: configuration for scheduled DNS health / deliverability digest emails.
-- Each row defines what to include and how often to send a summary report to a list of
-- recipients. The scheduler (apid or a dedicated daemon) queries rows WHERE enabled AND
-- next_run_at <= now(), sends the report, then advances next_run_at and sets last_sent_at.

CREATE TABLE report_schedules (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                TEXT        NOT NULL,
    frequency           TEXT        NOT NULL DEFAULT 'weekly',   -- daily, weekly, monthly
    recipients          TEXT[]      NOT NULL DEFAULT '{}',
    include_dns         BOOLEAN     NOT NULL DEFAULT true,
    include_dmarc       BOOLEAN     NOT NULL DEFAULT true,
    include_incidents   BOOLEAN     NOT NULL DEFAULT true,
    include_reputation  BOOLEAN     NOT NULL DEFAULT true,
    enabled             BOOLEAN     NOT NULL DEFAULT true,
    last_sent_at        TIMESTAMPTZ,
    next_run_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX idx_report_schedules_next_run
    ON report_schedules (next_run_at)
    WHERE enabled AND next_run_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_report_schedules_next_run;
DROP TABLE IF EXISTS report_schedules;
