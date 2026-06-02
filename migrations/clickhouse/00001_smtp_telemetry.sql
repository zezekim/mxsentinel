-- +goose Up
-- Derived from schemas/clickhouse/001_smtp_telemetry.sql — keep in sync.
-- Assumes the target database (set in the ClickHouse DSN) already exists; the
-- docker-compose service creates it. Objects are unqualified so they land there.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS smtp_events
(
    event_id            UUID,
    event_time          DateTime64(3, 'UTC'),
    ingested_at         DateTime64(3, 'UTC'),
    tenant_id           UUID,
    source              LowCardinality(String),

    event_type          Enum8('received'=1,'delivered'=2,'deferred'=3,'bounced'=4,'rejected'=5),
    bounce_class        Enum8('none'=0,'hard'=1,'soft'=2,'block'=3,'spam'=4,'policy'=5,'auth'=6,'reputation'=7,'technical'=8,'unknown'=9) DEFAULT 'none',

    message_id          String,
    queue_id            String,
    session_id          String,
    source_ip           IPv6,
    relay_ip            IPv6,
    ip_pool             LowCardinality(String),
    dkim_selector       LowCardinality(String),
    dkim_domain         LowCardinality(String),
    envelope_from       String,
    from_domain         LowCardinality(String),

    recipient_domain    LowCardinality(String),
    recipient_hash      String,

    provider            LowCardinality(String),
    mx_host             String,

    spf_result          Enum8('none'=0,'pass'=1,'fail'=2,'softfail'=3,'neutral'=4,'temperror'=5,'permerror'=6) DEFAULT 'none',
    dkim_result         Enum8('none'=0,'pass'=1,'fail'=2,'temperror'=3,'permerror'=4) DEFAULT 'none',
    dmarc_result        Enum8('none'=0,'pass'=1,'fail'=2) DEFAULT 'none',

    tls_used            UInt8 DEFAULT 0,
    tls_version         LowCardinality(String),
    tls_cipher          LowCardinality(String),
    tls_verified        UInt8 DEFAULT 0,

    smtp_code           UInt16,
    enhanced_status     LowCardinality(String),
    response_text       String,

    size_bytes          UInt32,
    queue_time_ms       UInt32,
    delivery_latency_ms UInt32,

    attrs               Map(String, String),

    INDEX idx_provider provider TYPE set(64) GRANULARITY 4,
    INDEX idx_msgid message_id TYPE bloom_filter GRANULARITY 4,
    INDEX idx_relay_ip relay_ip TYPE bloom_filter GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(event_time)
ORDER BY (tenant_id, event_time, message_id)
TTL toDateTime(event_time) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS deliverability_5m
(
    bucket         DateTime('UTC'),
    tenant_id      UUID,
    provider       LowCardinality(String),
    delivered      AggregateFunction(sum, UInt64),
    deferred       AggregateFunction(sum, UInt64),
    bounced        AggregateFunction(sum, UInt64),
    rejected       AggregateFunction(sum, UInt64),
    total          AggregateFunction(sum, UInt64),
    latency_p50_ms AggregateFunction(quantile(0.50), UInt32),
    latency_p95_ms AggregateFunction(quantile(0.95), UInt32)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id, provider, bucket)
TTL bucket + INTERVAL 400 DAY;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW IF NOT EXISTS deliverability_5m_mv
TO deliverability_5m
AS
SELECT
    toStartOfFiveMinute(event_time)              AS bucket,
    tenant_id,
    provider,
    sumState(toUInt64(event_type = 'delivered')) AS delivered,
    sumState(toUInt64(event_type = 'deferred'))  AS deferred,
    sumState(toUInt64(event_type = 'bounced'))   AS bounced,
    sumState(toUInt64(event_type = 'rejected'))  AS rejected,
    sumState(toUInt64(1))                        AS total,
    quantileState(0.50)(delivery_latency_ms)     AS latency_p50_ms,
    quantileState(0.95)(delivery_latency_ms)     AS latency_p95_ms
FROM smtp_events
GROUP BY bucket, tenant_id, provider;
-- +goose StatementEnd

-- +goose Down
DROP VIEW IF EXISTS deliverability_5m_mv;
DROP TABLE IF EXISTS deliverability_5m;
DROP TABLE IF EXISTS smtp_events;
