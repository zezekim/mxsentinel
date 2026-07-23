-- +goose Up
-- High-volume per-MTA TLS-RPT detail rows (RFC 8460). The tlsrpt ingestor writes one
-- summary row per policy (result_type='successful', success_count set) plus one row per
-- failure detail (result_type=<failure>, failure_count set). Objects are unqualified so
-- they land in the database named in the ClickHouse DSN (created by docker-compose).

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tlsrpt_results
(
    report_id            String,
    org_name             LowCardinality(String),
    tenant_id            UUID,
    policy_domain        LowCardinality(String),
    policy_type          LowCardinality(String),
    date_begin           DateTime('UTC'),
    date_end             DateTime('UTC'),
    result_type          LowCardinality(String),
    sending_mta_ip       IPv6,
    receiving_mx_hostname String,
    receiving_ip         IPv6,
    success_count        UInt64,
    failure_count        UInt64,
    ingested_at          DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(date_begin)
ORDER BY (tenant_id, policy_domain, date_begin, result_type, report_id);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS tlsrpt_results;
