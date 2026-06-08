-- +goose Up
-- Feedback-loop (ARF) complaints + Gmail Postmaster reputation -> per-sender complaint
-- reputation. Two tables feed the /v1/reputation view and the abuse-incident rollup
-- (cmd/fbld):
--
--   fbl_complaints     one row per parsed ARF complaint email (RFC 5965 multipart/report;
--                      report-type=feedback-report). The drop directory is fed by abuse@
--                      mailboxes that providers (Google/Microsoft/Yahoo) send FBL reports
--                      to AFTER you enroll each sending IP/domain with their feedback loop.
--                      sasl_username/message_id are best-effort: present only when the
--                      original message's headers survive into the message/rfc822 part.
--
--   domain_reputation  latest Gmail Postmaster Tools grade + spam rate per domain, refreshed
--                      by internal/fbl/postmaster.go when GOOGLE_POSTMASTER_TOKEN is set.
--                      source distinguishes "postmaster" from any future providers.

CREATE TABLE fbl_complaints (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    feedback_type TEXT,                 -- ARF Feedback-Type: abuse, fraud, virus, ...
    sender_domain TEXT,                 -- From: domain of the complained-about message
    sasl_username TEXT,                 -- submission login, if recoverable from headers
    provider      TEXT,                 -- reporting mailbox provider (google, yahoo, ...)
    message_id    TEXT                  -- original Message-ID, if present
);
-- The 24h rollup and the API both group by sender_domain over received_at.
CREATE INDEX idx_fbl_complaints_domain_time ON fbl_complaints (sender_domain, received_at DESC);

CREATE TABLE domain_reputation (
    domain     TEXT PRIMARY KEY,
    source     TEXT,                    -- "postmaster"
    reputation TEXT,                    -- HIGH | GOOD | MEDIUM | LOW | BAD (Postmaster grade)
    spam_rate  DOUBLE PRECISION,        -- user-reported spam rate (0..1)
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS domain_reputation;
DROP TABLE IF EXISTS fbl_complaints;
