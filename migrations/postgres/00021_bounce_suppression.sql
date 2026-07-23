-- +goose Up
-- Bounce classification + suppression-list management (the platform's Remediate step;
-- cmd/bounced, internal/bounce, internal/api/handlers_bounce.go).
--
-- suppression_entries: the per-tenant suppression list. Each row suppresses one recipient,
-- identified ONLY by recipient_hash — the keyed HMAC-SHA256 produced at the telemetry parser
-- boundary (internal/telemetry). The plaintext address is never stored. Entries are
-- auto-populated by the bounced daemon from terminal bounces (hard / invalid-recipient /
-- spam-block) and complaints, and can be managed via the /v1/suppression API. A NULL
-- expires_at means the suppression is permanent (e.g. a non-existent mailbox); a non-NULL
-- expires_at (e.g. spam blocks) lets the recipient become re-eligible after the window.
--
-- reason:   stable machine reason, e.g. hard_bounce | invalid_recipient | spam_block | complaint
-- category: the internal/bounce.Category that produced the entry (hard, invalid_recipient, ...)
-- source:   where the entry came from — bounce | complaint | manual | import
--
-- bounce_rollup: per-day, per-sending-domain, per-category classified bounce counts. The
-- category is computed by the Go classifier (not expressible in SQL), so the daemon writes
-- these authoritatively by recomputing over a fixed lookback window each pass. Powers the
-- classified-category breakdown in GET /v1/bounces. (Cheap per-domain bounce *rates* come
-- from the ClickHouse bounce_daily rollup instead — see migrations/clickhouse/00005.)

CREATE TABLE suppression_entries (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    recipient_hash TEXT        NOT NULL,
    reason         TEXT        NOT NULL DEFAULT '',
    category       TEXT        NOT NULL DEFAULT '',
    source         TEXT        NOT NULL DEFAULT 'bounce', -- bounce | complaint | manual | import
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ,                            -- NULL = permanent
    UNIQUE (tenant_id, recipient_hash)
);
-- Listing endpoint filters by tenant and (optionally) active expiry, newest first.
CREATE INDEX idx_suppression_tenant_created ON suppression_entries (tenant_id, created_at DESC);
-- Export / active-membership scans filter out expired rows.
CREATE INDEX idx_suppression_tenant_expiry ON suppression_entries (tenant_id, expires_at);

CREATE TABLE bounce_rollup (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    day         DATE        NOT NULL,
    from_domain TEXT        NOT NULL DEFAULT '',
    category    TEXT        NOT NULL,
    count       INTEGER     NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, day, from_domain, category)
);
CREATE INDEX idx_bounce_rollup_tenant_day ON bounce_rollup (tenant_id, day DESC);

-- +goose Down
DROP TABLE IF EXISTS bounce_rollup;
DROP TABLE IF EXISTS suppression_entries;
