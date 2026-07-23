-- +goose Up
-- High-frequency synthetic SMTP probe results (cmd/probed). This is the time-series backing
-- for the endpoint latency and uptime history charts. Postgres holds the authoritative
-- current status; ClickHouse holds the dense history and is where uptime/latency rollups run.
--
-- Relay-wide infrastructure state (no tenant_id): the relay probing its own SMTP endpoints.
-- Objects are unqualified so they land in the database named in the ClickHouse DSN.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS smtp_probe_results
(
    probed_at              DateTime64(3, 'UTC'),
    endpoint               LowCardinality(String),          -- "host:port"
    host                   LowCardinality(String),
    port                   UInt16,
    mode                   LowCardinality(String),          -- plain | starttls | implicit_tls
    ok                     UInt8   DEFAULT 0,
    stage                  LowCardinality(String),          -- complete | connect | banner | ehlo | starttls | tls_handshake | response
    error                  String,
    latency_ms             UInt32  DEFAULT 0,               -- TCP connect latency
    tls_negotiated         UInt8   DEFAULT 0,
    tls_version            LowCardinality(String),
    tls_chain_valid        UInt8   DEFAULT 0,
    cert_days_until_expiry Int32   DEFAULT 0,
    cert_not_after         DateTime('UTC') DEFAULT toDateTime(0, 'UTC'),
    cert_expiring          UInt8   DEFAULT 0,
    greylisting            UInt8   DEFAULT 0,
    response_code          UInt16  DEFAULT 0
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(probed_at)
ORDER BY (endpoint, probed_at)
TTL toDateTime(probed_at) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
-- +goose StatementEnd

-- Per-endpoint hourly uptime/latency rollup so long-window dashboards don't scan raw rows.
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS smtp_probe_uptime_1h
(
    bucket         DateTime('UTC'),
    endpoint       LowCardinality(String),
    probes         AggregateFunction(sum, UInt64),
    ok_probes      AggregateFunction(sum, UInt64),
    latency_avg_ms AggregateFunction(avg, UInt32),
    latency_p95_ms AggregateFunction(quantile(0.95), UInt32)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(bucket)
ORDER BY (endpoint, bucket)
TTL bucket + INTERVAL 400 DAY;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW IF NOT EXISTS smtp_probe_uptime_1h_mv
TO smtp_probe_uptime_1h
AS
SELECT
    toStartOfHour(probed_at)          AS bucket,
    endpoint,
    sumState(toUInt64(1))             AS probes,
    sumState(toUInt64(ok = 1))        AS ok_probes,
    avgState(latency_ms)              AS latency_avg_ms,
    quantileState(0.95)(latency_ms)   AS latency_p95_ms
FROM smtp_probe_results
GROUP BY bucket, endpoint;
-- +goose StatementEnd

-- +goose Down
DROP VIEW IF EXISTS smtp_probe_uptime_1h_mv;
DROP TABLE IF EXISTS smtp_probe_uptime_1h;
DROP TABLE IF EXISTS smtp_probe_results;
