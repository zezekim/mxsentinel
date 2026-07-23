-- +goose Up
-- Long-horizon Microsoft SNDS per-IP-per-day history for trend analytics (cmd/sndsd streams a
-- copy here in addition to the authoritative Postgres row in 00020_snds_jmrp.sql).
--
-- Why both stores? SNDS itself is low-volume (a handful of egress IPs x Microsoft's ~30-day
-- retention window), so Postgres is the operational source of truth for the current filter
-- state and the incident rollup. ClickHouse is optional and exists purely to retain history
-- BEYOND Microsoft's 30-day window for long-range trend charts, matching how the rest of the
-- platform keeps high-retention time series in ClickHouse (smtp_events, dmarc_records). The API
-- reads trends from ClickHouse when it is deployed and falls back to the Postgres window
-- otherwise, so ClickHouse is never a hard dependency. ReplacingMergeTree dedupes re-polled days
-- on (tenant_id, ip, data_date), keeping the latest fetched_at.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS snds_ip_data
(
    tenant_id          UUID,
    ip                 String,
    data_date          Date,
    rcpt_count         UInt64,
    data_count         UInt64,
    message_recipients UInt64,
    filter_result      Enum8('' = 0, 'GREEN' = 1, 'YELLOW' = 2, 'RED' = 3),
    complaint_band     LowCardinality(String),
    trap_hits          UInt32,
    sample_helo        String,
    sample_from        String,
    fetched_at         DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(fetched_at)
PARTITION BY toYYYYMM(data_date)
ORDER BY (tenant_id, ip, data_date);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS snds_ip_data;
