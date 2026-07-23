-- +goose Up
-- Historical inbox-placement analytics (internal/seedtest, cmd/seedd).
--
-- Postgres (seed_results) holds the authoritative, mutable state of the CURRENT run — it is
-- updated in place as probes are sent and placement is observed. ClickHouse holds one
-- IMMUTABLE row per finalized seed result, appended once the result reaches a terminal
-- placement (inbox/spam/missing). This is the read path for long-horizon trend analytics:
-- "inbox rate for provider=gmail from ip_pool=pool-A over the last 90 days", per-provider
-- placement time series, auth-pass rates by provider, etc. — the same rationale as
-- smtp_events vs. the operational Postgres tables (see docs/data-model.md).
--
-- ReplacingMergeTree keyed on (result_id) makes re-inserts idempotent: seedd may re-emit a
-- result row if a run is finalized more than once, and the newest ingested_at wins.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS seed_placement_results
(
    result_id   UUID,
    run_id      UUID,
    tenant_id   UUID,
    list_id     UUID,
    run_tag     String,
    address     String,
    provider    LowCardinality(String),
    ip_pool     LowCardinality(String),
    placement   Enum8('unknown'=0,'inbox'=1,'spam'=2,'missing'=3),
    spf_pass    UInt8,
    dkim_pass   UInt8,
    dmarc_pass  UInt8,
    sent_at     DateTime('UTC'),
    observed_at DateTime('UTC'),
    ingested_at DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(observed_at)
ORDER BY (tenant_id, provider, observed_at, run_id, result_id);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS seed_placement_results;
