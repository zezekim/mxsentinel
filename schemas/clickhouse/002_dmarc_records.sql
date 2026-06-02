-- MX Sentinel — ClickHouse schema (DMARC aggregate records)
--
-- Stores parsed DMARC aggregate report rows, one row per <record> element
-- from an RUA report. Written in batches by `dmarcparser` consuming inbound
-- DMARC XML reports from NATS JetStream.
--
-- Each row captures a single source IP + policy result combination as
-- reported by a receiving domain (org_name). The ReplacingMergeTree engine
-- deduplicates on (tenant_id, domain, date_begin, source_ip, report_id) so
-- reprocessing a report is idempotent.
--
-- See docs/data-model.md for the DMARC parsing pipeline.

CREATE DATABASE IF NOT EXISTS mxsentinel;

-- ---------------------------------------------------------------------------
-- DMARC aggregate report records
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mxsentinel.dmarc_records
(
    report_id     String,                                -- unique report ID from <report_metadata>
    org_name      LowCardinality(String),               -- reporting org (e.g. google.com)
    tenant_id     UUID,                                  -- MX Sentinel tenant
    domain        LowCardinality(String),               -- policy domain (header From domain)
    date_begin    DateTime('UTC'),                       -- report interval start
    date_end      DateTime('UTC'),                       -- report interval end
    source_ip     IPv6,                                  -- sending IP (IPv4-mapped as needed)
    count         UInt32,                                -- message count for this row
    disposition   Enum8('none'=0,'quarantine'=1,'reject'=2),  -- applied policy disposition
    dkim_aligned  UInt8,                                 -- 1 = DKIM aligned/pass, 0 = fail
    spf_aligned   UInt8,                                 -- 1 = SPF aligned/pass, 0 = fail
    header_from   LowCardinality(String),               -- RFC5322 From domain
    envelope_from String,                                -- MAIL FROM domain
    ingested_at   DateTime64(3, 'UTC')                  -- when MX Sentinel processed the report
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(date_begin)
ORDER BY (tenant_id, domain, date_begin, source_ip, report_id);
