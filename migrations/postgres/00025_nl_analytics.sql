-- +goose Up
-- Natural-language analytics audit log ("ask your mail logs" — internal/nlquery, POST /v1/ask).
-- MX Sentinel lets an operator ask deliverability questions in plain English; a local LLM plans
-- which WHITELISTED aggregate query to run (never free-form SQL), we execute it, and the model
-- composes an answer. This table records each question, the whitelisted tools the planner chose,
-- and the composed answer, purely for auditability and product analytics.
--
-- PRIVACY: this log stores ONLY the operator's own question text, the names+args of the
-- whitelisted aggregate queries that ran, and the natural-language answer. It NEVER stores raw
-- mail content (message bodies or subject lines are never available to this subsystem at all).

CREATE TABLE nl_query_log (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    question     TEXT        NOT NULL,                     -- the operator's plain-English question
    chosen_tools JSONB       NOT NULL DEFAULT '[]'::jsonb, -- [{"tool":"...","args":{...}}] whitelisted queries run
    answer       TEXT        NOT NULL DEFAULT '',          -- composed natural-language answer
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_nl_query_log_tenant_created ON nl_query_log (tenant_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS nl_query_log;
