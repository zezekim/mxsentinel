-- +goose Up
-- Per-day, per-(tenant, sending-domain) delivery/bounce rollup over the existing
-- smtp_events table, used for cheap per-domain bounce-RATE trends (GET /v1/bounces
-- domain_rates). Objects are unqualified so they land in the DSN's database.
--
-- Why a rollup and not the raw table: bounce-rate trends only need coarse daily counts, and
-- scanning raw smtp_events per API call is wasteful. A SummingMergeTree fed by a materialized
-- view keeps running totals maintained automatically at ingest time.
--
-- Why NOT a fully-classified table here: the fine-grained bounce Category (hard, spam_block,
-- invalid_recipient, ...) is produced by the PURE Go classifier in internal/bounce, whose
-- text/enhanced-code rules would be brittle and drift-prone if duplicated in ClickHouse SQL.
-- The Go classifier stays authoritative: the classified feed is computed in Go from raw rows,
-- and per-category counts are rolled up into Postgres bounce_rollup by the bounced daemon.
-- This table deliberately stays at the "rate" granularity that SQL can maintain correctly.
--
-- Counts are stored as plain UInt64 in a SummingMergeTree; queries MUST still aggregate
-- (sum(...)) because part merges are eventual.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS bounce_daily
(
    day         Date,
    tenant_id   UUID,
    from_domain LowCardinality(String),
    sent        UInt64,   -- total delivery events (any event_type) — rate denominator
    delivered   UInt64,
    deferred    UInt64,
    bounced     UInt64,
    rejected    UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, from_domain, day)
TTL day + INTERVAL 400 DAY;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW IF NOT EXISTS bounce_daily_mv
TO bounce_daily
AS
SELECT
    toDate(event_time)                            AS day,
    tenant_id,
    from_domain,
    toUInt64(count())                             AS sent,
    toUInt64(countIf(event_type = 'delivered'))   AS delivered,
    toUInt64(countIf(event_type = 'deferred'))    AS deferred,
    toUInt64(countIf(event_type = 'bounced'))     AS bounced,
    toUInt64(countIf(event_type = 'rejected'))    AS rejected
FROM smtp_events
GROUP BY day, tenant_id, from_domain;
-- +goose StatementEnd

-- +goose Down
DROP VIEW IF EXISTS bounce_daily_mv;
DROP TABLE IF EXISTS bounce_daily;
