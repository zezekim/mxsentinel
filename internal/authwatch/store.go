// Package authwatch implements SASL credential-compromise detection (behavioral) for
// MX Sentinel's outbound relay. The detector (Detector) folds SMTP telemetry — keyed by
// sasl_username — into per-credential rolling windows and scores compromise-shaped changes:
// a burst of DISTINCT recipient domains (list-blasting), a spam/block bounce-rate spike, a
// volume far above the credential's recent norm, and (optionally) off-hours concentration.
// When the combined score crosses an env-tunable threshold it records a signal and opens a
// critical incident; auto-lock of the credential is opt-in (it's drastic on a shared relay).
//
// SHARED-RELAY CAVEAT: on a shared cPanel relay ALL submission arrives via ONE SASL
// credential from ONE source IP, so per-credential keying is coarse and the classic "new
// geo/ASN login" anomaly is degenerate (one IP — and the submitting client's source IP is
// not even on the event bus; SMTPPayload.RelayIP is the EGRESS node). The IP-geo path is
// gated behind GeoLookup (no-op by default) and documented as a follow-up needing a
// telemetry extension. These behavioral signals are strongest in dedicated-submission
// deployments (one credential per end-user).
package authwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists credential signals and lock state. It runs its own SQL against the shared
// pgxpool handle exposed by the postgres store ((*postgres.Store).Pool); it deliberately
// does not live under internal/store/postgres so the feature stays self-contained.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgx pool (pass (*postgres.Store).Pool).
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// InsertSignal appends a detection to credential_auth_signal.
func (s *Store) InsertSignal(ctx context.Context, username, signal string, detail json.RawMessage) error {
	var d interface{} = []byte("{}")
	if len(detail) > 0 {
		d = []byte(detail)
	}
	const q = `INSERT INTO credential_auth_signal (sasl_username, signal, detail)
	           VALUES ($1, $2, COALESCE($3, '{}')::jsonb)`
	if _, err := s.pool.Exec(ctx, q, username, signal, d); err != nil {
		return fmt.Errorf("insert credential auth signal: %w", err)
	}
	return nil
}

// SetLock upserts the lock state for a credential. reason and locked_at are recorded for the
// Auth Security UI; the smtp_users table remains the source of truth for whether Dovecot
// accepts the login.
func (s *Store) SetLock(ctx context.Context, username string, locked bool, reason string) error {
	const q = `INSERT INTO credential_lock (sasl_username, locked, reason, locked_at)
	           VALUES ($1, $2, NULLIF($3,''), CASE WHEN $2 THEN now() ELSE NULL END)
	           ON CONFLICT (sasl_username) DO UPDATE
	             SET locked = EXCLUDED.locked,
	                 reason = EXCLUDED.reason,
	                 locked_at = EXCLUDED.locked_at`
	if _, err := s.pool.Exec(ctx, q, username, locked, reason); err != nil {
		return fmt.Errorf("set credential lock: %w", err)
	}
	return nil
}

// Signal is one recorded detection.
type Signal struct {
	Signal     string          `json:"signal"`
	Detail     json.RawMessage `json:"detail"`
	DetectedAt time.Time       `json:"detected_at"`
}

// CredentialView aggregates a credential's recent signals and current lock state for the API.
type CredentialView struct {
	SASLUsername  string   `json:"sasl_username"`
	RecentSignals []Signal `json:"recent_signals"`
	Locked        bool     `json:"locked"`
	Reason        string   `json:"reason,omitempty"`
	LockedAt      *time.Time
}

// ListCredentials returns every credential that has either a recorded signal or a lock row,
// with up to signalsPerCred most-recent signals each, newest activity first. Read-only —
// suitable for the GET /v1/auth-security dashboard.
func (s *Store) ListCredentials(ctx context.Context, signalsPerCred int) ([]CredentialView, error) {
	if signalsPerCred <= 0 {
		signalsPerCred = 10
	}
	if signalsPerCred > 100 {
		signalsPerCred = 100
	}

	// Union of usernames seen in either table, with lock state attached.
	const usersQ = `
SELECT u.sasl_username,
       COALESCE(l.locked, FALSE),
       COALESCE(l.reason, ''),
       l.locked_at,
       u.last_signal
FROM (
    SELECT sasl_username, MAX(detected_at) AS last_signal
    FROM credential_auth_signal
    GROUP BY sasl_username
    UNION
    SELECT sasl_username, NULL::timestamptz FROM credential_lock
) u
LEFT JOIN credential_lock l ON l.sasl_username = u.sasl_username
GROUP BY u.sasl_username, l.locked, l.reason, l.locked_at, u.last_signal
ORDER BY COALESCE(MAX(u.last_signal), l.locked_at) DESC NULLS LAST, u.sasl_username`

	rows, err := s.pool.Query(ctx, usersQ)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	type cred struct {
		username string
		locked   bool
		reason   string
		lockedAt *time.Time
	}
	var creds []cred
	for rows.Next() {
		var c cred
		var lastSignal *time.Time
		if err := rows.Scan(&c.username, &c.locked, &c.reason, &c.lockedAt, &lastSignal); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		creds = append(creds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]CredentialView, 0, len(creds))
	for _, c := range creds {
		sigs, err := s.recentSignals(ctx, c.username, signalsPerCred)
		if err != nil {
			return nil, err
		}
		out = append(out, CredentialView{
			SASLUsername:  c.username,
			RecentSignals: sigs,
			Locked:        c.locked,
			Reason:        c.reason,
			LockedAt:      c.lockedAt,
		})
	}
	return out, nil
}

func (s *Store) recentSignals(ctx context.Context, username string, limit int) ([]Signal, error) {
	const q = `SELECT signal, detail, detected_at
	           FROM credential_auth_signal
	           WHERE sasl_username = $1
	           ORDER BY detected_at DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, q, username, limit)
	if err != nil {
		return nil, fmt.Errorf("recent signals: %w", err)
	}
	defer rows.Close()

	sigs := make([]Signal, 0, limit)
	for rows.Next() {
		var sig Signal
		if err := rows.Scan(&sig.Signal, &sig.Detail, &sig.DetectedAt); err != nil {
			return nil, fmt.Errorf("scan signal: %w", err)
		}
		sigs = append(sigs, sig)
	}
	return sigs, rows.Err()
}
