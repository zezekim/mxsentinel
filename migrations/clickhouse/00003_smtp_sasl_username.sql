-- +goose Up
-- Add the authenticated submission user to smtp_events so outbound mail can be attributed
-- to an SMTP account (per-user abuse accounting / auto-suspension; see cmd/abused).
-- +goose StatementBegin
ALTER TABLE smtp_events
    ADD COLUMN IF NOT EXISTS sasl_username LowCardinality(String) AFTER response_text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE smtp_events DROP COLUMN IF EXISTS sasl_username;
-- +goose StatementEnd
