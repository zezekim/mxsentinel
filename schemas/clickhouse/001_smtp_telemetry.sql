-- MX Sentinel — ClickHouse schema (SMTP telemetry / time-series observability)
--
-- This is the high-ingest, append-only analytics store. One row per SMTP transaction
-- outcome, written in batches by `ingestd` consuming `mxs.smtp.>` from NATS JetStream.
--
-- Privacy (enforced upstream at the parser, mirrored here by column design):
--   * NO message bodies, NO attachments, NO subject lines.
--   * recipient_domain is stored; the full recipient address is stored only as a keyed
--     hash (recipient_hash). envelope_from is metadata and kept whole for correlation.
--   * remote MTA response text is truncated upstream (~512 chars).
--
-- See docs/data-model.md and docs/event-contracts.md (smtp_event.schema.json maps 1:1).

CREATE DATABASE IF NOT EXISTS mxsentinel;

-- ---------------------------------------------------------------------------
-- Raw SMTP events
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mxsentinel.smtp_events
(
    -- Envelope / identity
    event_id            UUID,
    event_time          DateTime64(3, 'UTC'),          -- occurred_at (relay time)
    ingested_at         DateTime64(3, 'UTC'),          -- when MX Sentinel observed it
    tenant_id           UUID,
    source              LowCardinality(String),        -- emitting component@node

    -- Outcome
    event_type          Enum8(
                            'received'  = 1,           -- accepted into our queue
                            'delivered' = 2,
                            'deferred'  = 3,           -- temp fail (4xx)
                            'bounced'   = 4,           -- perm fail after acceptance (5xx)
                            'rejected'  = 5            -- rejected at SMTP time
                        ),
    bounce_class        Enum8(
                            'none'      = 0,
                            'hard'      = 1,
                            'soft'      = 2,
                            'block'     = 3,           -- IP/domain blocked by provider
                            'spam'      = 4,           -- content/spam rejection
                            'policy'    = 5,           -- policy (e.g. DMARC) rejection
                            'auth'      = 6,           -- auth/alignment failure
                            'reputation'= 7,
                            'technical' = 8,           -- TLS/DNS/connectivity
                            'unknown'   = 9
                        ) DEFAULT 'none',

    -- Correlation keys (also carried in the event envelope's correlation block)
    message_id          String,
    queue_id            String,
    session_id          String,
    source_ip           IPv6,                          -- inbound client (cPanel/Exim smart host)
    relay_ip            IPv6,                          -- our outbound IP
    ip_pool             LowCardinality(String),
    dkim_selector       LowCardinality(String),
    dkim_domain         LowCardinality(String),
    envelope_from       String,
    from_domain         LowCardinality(String),

    -- Recipient (privacy: domain kept, address hashed)
    recipient_domain    LowCardinality(String),
    recipient_hash      String,                        -- keyed hash of full RCPT TO

    -- Destination provider classification
    provider            LowCardinality(String),        -- gmail | microsoft | yahoo | ...
    mx_host             String,

    -- Authentication results
    spf_result          Enum8('none'=0,'pass'=1,'fail'=2,'softfail'=3,'neutral'=4,'temperror'=5,'permerror'=6) DEFAULT 'none',
    dkim_result         Enum8('none'=0,'pass'=1,'fail'=2,'temperror'=3,'permerror'=4) DEFAULT 'none',
    dmarc_result        Enum8('none'=0,'pass'=1,'fail'=2) DEFAULT 'none',

    -- TLS metadata
    tls_used            UInt8 DEFAULT 0,
    tls_version         LowCardinality(String),
    tls_cipher          LowCardinality(String),
    tls_verified        UInt8 DEFAULT 0,

    -- SMTP response
    smtp_code           UInt16,                        -- e.g. 250, 421, 550
    enhanced_status     LowCardinality(String),        -- e.g. 5.7.1
    response_text       String,                        -- truncated upstream
    sasl_username       LowCardinality(String),        -- authenticated submission user (added in migration 00003)

    -- Sizes & timing
    size_bytes          UInt32,
    queue_time_ms       UInt32,                        -- time spent in our queue
    delivery_latency_ms UInt32,                        -- accept -> final outcome

    -- Free-form extracted attributes that don't warrant a column yet
    attrs               Map(String, String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(event_time)
ORDER BY (tenant_id, event_time, message_id)
-- Default retention; override per deployment. Raw events age out; rollups persist.
TTL toDateTime(event_time) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;

-- Helpful skip-indexes for common explorer filters.
ALTER TABLE mxsentinel.smtp_events ADD INDEX IF NOT EXISTS idx_provider provider TYPE set(64) GRANULARITY 4;
ALTER TABLE mxsentinel.smtp_events ADD INDEX IF NOT EXISTS idx_msgid message_id TYPE bloom_filter GRANULARITY 4;
ALTER TABLE mxsentinel.smtp_events ADD INDEX IF NOT EXISTS idx_relay_ip relay_ip TYPE bloom_filter GRANULARITY 4;

-- ---------------------------------------------------------------------------
-- Deliverability rollups (tenant x provider x 5-minute bucket) via materialized view.
-- The dashboard reads this, not the raw table, for time-series charts.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mxsentinel.deliverability_5m
(
    bucket              DateTime('UTC'),
    tenant_id           UUID,
    provider            LowCardinality(String),
    delivered           AggregateFunction(sum, UInt64),
    deferred            AggregateFunction(sum, UInt64),
    bounced             AggregateFunction(sum, UInt64),
    rejected            AggregateFunction(sum, UInt64),
    total               AggregateFunction(sum, UInt64),
    latency_p50_ms      AggregateFunction(quantile(0.50), UInt32),
    latency_p95_ms      AggregateFunction(quantile(0.95), UInt32)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id, provider, bucket)
TTL bucket + INTERVAL 400 DAY;       -- rollups retained far longer than raw events

CREATE MATERIALIZED VIEW IF NOT EXISTS mxsentinel.deliverability_5m_mv
TO mxsentinel.deliverability_5m
AS
SELECT
    toStartOfFiveMinute(event_time)                          AS bucket,
    tenant_id,
    provider,
    sumState(toUInt64(event_type = 'delivered'))             AS delivered,
    sumState(toUInt64(event_type = 'deferred'))              AS deferred,
    sumState(toUInt64(event_type = 'bounced'))               AS bounced,
    sumState(toUInt64(event_type = 'rejected'))              AS rejected,
    sumState(toUInt64(1))                                    AS total,
    quantileState(0.50)(delivery_latency_ms)                 AS latency_p50_ms,
    quantileState(0.95)(delivery_latency_ms)                 AS latency_p95_ms
FROM mxsentinel.smtp_events
GROUP BY bucket, tenant_id, provider;

-- Example read query (merge the aggregate states):
--   SELECT bucket, provider,
--          sumMerge(delivered) AS delivered,
--          sumMerge(rejected)  AS rejected,
--          quantileMerge(0.95)(latency_p95_ms) AS p95
--   FROM mxsentinel.deliverability_5m
--   WHERE tenant_id = {tenant:UUID} AND bucket >= now() - INTERVAL 6 HOUR
--   GROUP BY bucket, provider ORDER BY bucket;
