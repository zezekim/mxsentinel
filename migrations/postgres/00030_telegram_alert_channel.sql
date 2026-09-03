-- +goose Up
-- Add "telegram" to the alert_channels type whitelist. The Telegram driver posts to the
-- Bot API sendMessage endpoint; its config is {"bot_token", "chat_id"} (+ optional
-- "message_thread_id"), with bot_token encrypted at rest like every other channel secret.
--
-- The same migration is what makes login notifications reachable in practice: any channel
-- whose config_json carries "login_alerts": true is notified when a user signs in to the
-- dashboard. That flag is plain (non-secret) JSON on the existing config column, so it
-- needs no schema change of its own.

ALTER TABLE alert_channels DROP CONSTRAINT IF EXISTS alert_channels_type_chk;
ALTER TABLE alert_channels ADD CONSTRAINT alert_channels_type_chk
    CHECK (type IN ('slack', 'webhook', 'pagerduty', 'email', 'telegram'));

-- +goose Down
-- Drop any telegram channels first; they would violate the restored constraint.
DELETE FROM alert_channels WHERE type = 'telegram';
ALTER TABLE alert_channels DROP CONSTRAINT IF EXISTS alert_channels_type_chk;
ALTER TABLE alert_channels ADD CONSTRAINT alert_channels_type_chk
    CHECK (type IN ('slack', 'webhook', 'pagerduty', 'email'));
