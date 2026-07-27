-- +goose Up
-- Per-message CONTENT + spam verdict for the message drill-down page (the "Spam Tests" and
-- "Headers" tabs of the operator's per-email view). This is deliberately SEPARATE from
-- smtp_events: smtp_events is metadata-only (the parser drops bodies and hashes recipients),
-- whereas this table intentionally stores message content — subject line and the full raw
-- header block — captured by an rspamd Lua plugin (deploy/rspamd/mxs_trace.lua) that
-- fire-and-forgets to POST /v1/ingest/message-content.
--
-- PRIVACY CARVE-OUT: this is the one place MX Sentinel stores message content. It exists only
-- to reproduce a MagicSpam/mail.baby-style per-email report for the operator's OWN outbound.
--   * The AI layer (cmd/aid) MUST NEVER read this table — it only ever sees smtp_events /
--     incident metadata. Keeping content in its own table makes that boundary structural.
--   * Content auto-expires via a 30-day TTL (metadata in smtp_events keeps its own 90-day TTL).
--   * The API only returns subject/raw_headers to callers holding the admin scope.
--
-- Keyed on (tenant_id, queue_id): queue_id is the stable per-message handle shared with
-- smtp_events (rspamd exposes it via task:get_queue_id()), so the drill-down joins cleanly even
-- when a message carries no Message-ID. ReplacingMergeTree(received_at) makes re-ingest
-- idempotent (a later POST for the same queue_id replaces the earlier row).

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS message_content
(
    received_at   DateTime64(3, 'UTC'),
    tenant_id     UUID,
    queue_id      String,
    message_id    String,
    mail_from     String,
    subject       String,
    raw_headers   String,                       -- full verbatim header block (content)
    spam_score    Float32 DEFAULT 0,
    spam_action   LowCardinality(String),       -- rspamd action: no action | greylist | add header | reject | ...
    is_spam       UInt8   DEFAULT 0,
    symbol_names  Array(String),                -- rspamd symbol names ("Spam Tests")
    symbol_scores Array(Float32),               -- per-symbol weights, index-aligned with symbol_names

    INDEX idx_msgid message_id TYPE bloom_filter GRANULARITY 4
)
ENGINE = ReplacingMergeTree(received_at)
PARTITION BY toYYYYMM(received_at)
ORDER BY (tenant_id, queue_id)
TTL toDateTime(received_at) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS message_content;
