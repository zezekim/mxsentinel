-- +goose Up
-- Phase 3: AI diagnostics enrichment. aid polls incidents where ai_analyzed_at IS NULL,
-- generates a root-cause narrative + remediation via a local LLM, and writes them back.

ALTER TABLE incidents ADD COLUMN ai_summary     TEXT;
ALTER TABLE incidents ADD COLUMN ai_remediation JSONB;
ALTER TABLE incidents ADD COLUMN ai_model       TEXT;
ALTER TABLE incidents ADD COLUMN ai_analyzed_at TIMESTAMPTZ;

CREATE INDEX idx_incidents_needs_ai ON incidents (created_at) WHERE ai_analyzed_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_incidents_needs_ai;
ALTER TABLE incidents DROP COLUMN IF EXISTS ai_analyzed_at;
ALTER TABLE incidents DROP COLUMN IF EXISTS ai_model;
ALTER TABLE incidents DROP COLUMN IF EXISTS ai_remediation;
ALTER TABLE incidents DROP COLUMN IF EXISTS ai_summary;
