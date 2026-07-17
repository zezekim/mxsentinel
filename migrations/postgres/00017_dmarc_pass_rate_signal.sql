-- +goose NO TRANSACTION
-- +goose Up
-- Add the dmarc_pass_rate alert signal (evaluated by cmd/alertd). It fires when a
-- monitored domain's DMARC pass rate over the recent window drops below the rule's
-- configured threshold (condition JSON e.g. {"threshold":0.9,"min_messages":50}).
-- ADD VALUE cannot run inside a transaction, hence NO TRANSACTION above.
ALTER TYPE alert_signal ADD VALUE IF NOT EXISTS 'dmarc_pass_rate';

-- +goose Down
-- Postgres does not support removing a value from an enum type, so this is a no-op.
-- Rolling back would require recreating the type and rewriting dependent columns.
SELECT 1;
