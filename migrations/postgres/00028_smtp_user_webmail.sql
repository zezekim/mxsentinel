-- +goose Up
-- Webmail autologin for SMTP submission users (docs/webmail-autologin.md).
--
-- Two additions:
--
-- 1) smtp_users.password_enc — an AES-256-GCM sealed copy of the password (same Encryptor
--    and MXS_ENCRYPTION_KEY used for cPanel/WHMCS credentials, values prefixed "v1:").
--    password_hash stays the authoritative bcrypt credential Dovecot verifies; password_enc
--    exists only so apid can hand the plaintext to Roundcube at autologin time, because
--    IMAP has no way to accept a bcrypt hash. It is written ONLY when an encryption key is
--    configured — apid refuses to store a reversible password in passthrough mode, so
--    password_enc stays NULL and webmail is simply unavailable for that user. Existing users
--    have NULL until their next password reset.
--
-- 2) smtp_user_webmail_tokens — short-lived, single-use handoff tokens. The dashboard mints
--    one (admin scope), the Roundcube mxs_autologin plugin redeems it exactly once against
--    POST /v1/webmail/redeem to obtain the IMAP credentials. Only the SHA-256 hash is stored
--    (token_prefix is the non-secret lookup key), the row records who minted it and from
--    where, and used_at makes redemption idempotently one-shot: the redeem UPDATE only
--    matches WHERE used_at IS NULL AND expires_at > now().

ALTER TABLE smtp_users ADD COLUMN password_enc TEXT;

CREATE TABLE smtp_user_webmail_tokens (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    smtp_user_id UUID        NOT NULL REFERENCES smtp_users(id) ON DELETE CASCADE,
    token_prefix TEXT        NOT NULL UNIQUE,
    token_hash   TEXT        NOT NULL,
    created_by   UUID        REFERENCES users(id) ON DELETE SET NULL,  -- NULL when minted with an API token
    created_ip   TEXT        NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webmail_tokens_tenant  ON smtp_user_webmail_tokens (tenant_id, created_at DESC);
CREATE INDEX idx_webmail_tokens_user    ON smtp_user_webmail_tokens (smtp_user_id, created_at DESC);
-- Supports the retention sweep that deletes spent/expired rows.
CREATE INDEX idx_webmail_tokens_expires ON smtp_user_webmail_tokens (expires_at);

-- +goose Down
DROP TABLE IF EXISTS smtp_user_webmail_tokens;
ALTER TABLE smtp_users DROP COLUMN IF EXISTS password_enc;
