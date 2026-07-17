-- +goose Up
-- message_share_links: shareable, capability-based links to a single message's delivery
-- trace (like a courier tracking page for one email). A link resolves to the events in
-- ClickHouse smtp_events keyed on (tenant_id, queue_id) — the relay-local queue id is the
-- stable per-message handle (a message may have no Message-ID header when rejected early).
--
-- SECURITY: the URL token is the capability. The raw Postfix queue id is short and
-- guessable, so it is NEVER the secret — we mint a separate high-entropy token, store only
-- its SHA-256 hash plus a non-secret lookup prefix (same scheme as api_credentials), and
-- support per-link expiry + revocation. Anyone holding the link can read the trace; nobody
-- can enumerate other tenants' messages.

CREATE TABLE message_share_links (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    queue_id       TEXT        NOT NULL,
    message_id     TEXT        NOT NULL DEFAULT '',
    token_prefix   TEXT        NOT NULL UNIQUE,
    token_hash     TEXT        NOT NULL,
    label          TEXT        NOT NULL DEFAULT '',
    created_by     UUID        REFERENCES users(id) ON DELETE SET NULL,
    expires_at     TIMESTAMPTZ,
    revoked_at     TIMESTAMPTZ,
    last_viewed_at TIMESTAMPTZ,
    view_count     INTEGER     NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_message_share_links_tenant_queue
    ON message_share_links (tenant_id, queue_id);

-- +goose Down
DROP INDEX IF EXISTS idx_message_share_links_tenant_queue;
DROP TABLE IF EXISTS message_share_links;
