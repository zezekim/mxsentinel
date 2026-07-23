-- +goose Up
-- alert_channels: per-tenant outbound notification destinations for firing alerts and
-- incidents. Each row is one delivery target (Slack incoming webhook, generic webhook,
-- PagerDuty Events API v2, or email). Sensitive fields inside config_json (Slack webhook
-- URL, webhook signing secret, PagerDuty routing key) are encrypted at rest with the same
-- AES-256-GCM Encryptor used for cPanel/WHMCS credentials (values prefixed "v1:"); the API
-- and notifyd seal on write and open on read. Non-secret config (webhook url, email
-- recipients, custom header name) is stored in the clear.
--
-- alert_deliveries is an append-only log of every dispatch attempt. It doubles as the
-- source of truth for the dispatcher's dedup (never notify the same channel twice for the
-- same alert_ref) and per-channel throttling (suppress a flapping alert that would
-- otherwise spam a channel). Rows are keyed to a channel and an alert_ref (the incident id,
-- or "test:<uuid>" for the manual test action).

CREATE TABLE alert_channels (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    type        TEXT        NOT NULL,            -- slack | webhook | pagerduty | email
    name        TEXT        NOT NULL,
    config_json JSONB       NOT NULL DEFAULT '{}'::jsonb,  -- secrets within are encrypted
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT alert_channels_type_chk
        CHECK (type IN ('slack', 'webhook', 'pagerduty', 'email')),
    UNIQUE (tenant_id, name)
);

CREATE INDEX idx_alert_channels_tenant  ON alert_channels (tenant_id);
CREATE INDEX idx_alert_channels_enabled ON alert_channels (tenant_id) WHERE enabled;

CREATE TABLE alert_deliveries (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel_id  UUID        NOT NULL REFERENCES alert_channels(id) ON DELETE CASCADE,
    alert_ref   TEXT        NOT NULL,            -- incident id, or "test:<uuid>"
    status      TEXT        NOT NULL,            -- sent | failed | skipped_throttle | skipped_dedup
    error       TEXT        NOT NULL DEFAULT '',
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_alert_deliveries_channel_ref
    ON alert_deliveries (channel_id, alert_ref);
CREATE INDEX idx_alert_deliveries_channel_time
    ON alert_deliveries (channel_id, sent_at DESC);
CREATE INDEX idx_alert_deliveries_tenant_time
    ON alert_deliveries (tenant_id, sent_at DESC);

-- +goose Down
DROP TABLE IF EXISTS alert_deliveries;
DROP TABLE IF EXISTS alert_channels;
