package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Webmail autologin handoff (docs/webmail-autologin.md).
//
// The dashboard mints a short-lived, single-use token for one SMTP user; the Roundcube
// mxs_autologin plugin redeems it for that user's IMAP credentials. Only the SHA-256 hash
// of the token is stored — token_prefix is the non-secret lookup key, exactly as with API
// tokens and message share links.

// WebmailCredential is what a redeemed token resolves to: the SMTP user's login and the
// sealed copy of its password. The ciphertext is unsealed by the caller (apid holds the
// Encryptor); the store never sees the plaintext.
type WebmailCredential struct {
	SMTPUserID  string
	TenantID    string
	Username    string
	PasswordEnc string
}

// CreateWebmailToken records a minted autologin token for an SMTP user the tenant owns.
// createdBy may be "" (stored as NULL) when the caller authenticated with an API token
// rather than a user session. Returns the new row's id.
func (s *Store) CreateWebmailToken(ctx context.Context, tenantID, smtpUserID, tokenPrefix, tokenHash, createdBy, createdIP string, expiresAt time.Time) (string, error) {
	const q = `INSERT INTO smtp_user_webmail_tokens
	           (tenant_id, smtp_user_id, token_prefix, token_hash, created_by, created_ip, expires_at)
	           VALUES ($1, $2, $3, $4, $5, $6, $7)
	           RETURNING id`
	var createdByArg any
	if createdBy != "" {
		createdByArg = createdBy
	}
	var id string
	err := s.Pool.QueryRow(ctx, q, tenantID, smtpUserID, tokenPrefix, tokenHash, createdByArg, createdIP, expiresAt).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create webmail token: %w", err)
	}
	return id, nil
}

// RedeemWebmailToken atomically consumes an unexpired, unused token and returns the SMTP
// user's credential. The UPDATE ... WHERE used_at IS NULL AND expires_at > now() is what
// makes redemption strictly one-shot: a replay of the same token matches no row and
// returns found=false, as does an expired or unknown one. tokenHash is compared inside the
// statement, so a token whose prefix exists but whose secret is wrong is likewise rejected.
//
// Disabled users are refused here too — a suspended credential must not open webmail.
func (s *Store) RedeemWebmailToken(ctx context.Context, tokenPrefix, tokenHash string) (cred WebmailCredential, found bool, err error) {
	const q = `UPDATE smtp_user_webmail_tokens t
	           SET used_at = now()
	           FROM smtp_users u
	           WHERE t.token_prefix = $1
	             AND t.token_hash   = $2
	             AND t.used_at IS NULL
	             AND t.expires_at > now()
	             AND u.id = t.smtp_user_id
	             AND u.enabled = TRUE
	             AND u.password_enc IS NOT NULL
	           RETURNING u.id, u.tenant_id, u.username::text, u.password_enc`
	err = s.Pool.QueryRow(ctx, q, tokenPrefix, tokenHash).
		Scan(&cred.SMTPUserID, &cred.TenantID, &cred.Username, &cred.PasswordEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebmailCredential{}, false, nil
	}
	if err != nil {
		return WebmailCredential{}, false, fmt.Errorf("redeem webmail token: %w", err)
	}
	return cred, true, nil
}

// GetSMTPUserForWebmail loads the fields the mint path needs to decide whether a webmail
// session can be issued: the user must belong to the tenant, be enabled, and have a sealed
// password copy. found=false (nil error) when no such row belongs to the tenant.
func (s *Store) GetSMTPUserForWebmail(ctx context.Context, tenantID, id string) (username string, enabled, hasWebmail bool, found bool, err error) {
	const q = `SELECT username::text, enabled, password_enc IS NOT NULL
	           FROM smtp_users WHERE id = $2 AND tenant_id = $1`
	err = s.Pool.QueryRow(ctx, q, tenantID, id).Scan(&username, &enabled, &hasWebmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, false, nil
	}
	if err != nil {
		return "", false, false, false, fmt.Errorf("get smtp user for webmail: %w", err)
	}
	return username, enabled, hasWebmail, true, nil
}

// PurgeWebmailTokens deletes tokens that expired more than olderThan ago. Spent and expired
// rows have no further use — they are kept briefly only so the audit trail of a handoff
// survives long enough to investigate one. Returns the number of rows removed.
func (s *Store) PurgeWebmailTokens(ctx context.Context, olderThan time.Duration) (int64, error) {
	const q = `DELETE FROM smtp_user_webmail_tokens WHERE expires_at < now() - $1::interval`
	tag, err := s.Pool.Exec(ctx, q, olderThan.String())
	if err != nil {
		return 0, fmt.Errorf("purge webmail tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}
