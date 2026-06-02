-- +goose Up
-- Derived from schemas/clickhouse/002_dmarc_records.sql — keep in sync.
-- Assumes the target database (set in the ClickHouse DSN) already exists; the
-- docker-compose service creates it. Objects are unqualified so they land there.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS dmarc_records
(
    report_id     String,
    org_name      LowCardinality(String),
    tenant_id     UUID,
    domain        LowCardinality(String),
    date_begin    DateTime('UTC'),
    date_end      DateTime('UTC'),
    source_ip     IPv6,
    count         UInt32,
    disposition   Enum8('none'=0,'quarantine'=1,'reject'=2),
    dkim_aligned  UInt8,
    spf_aligned   UInt8,
    header_from   LowCardinality(String),
    envelope_from String,
    ingested_at   DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(date_begin)
ORDER BY (tenant_id, domain, date_begin, source_ip, report_id);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS dmarc_records;
