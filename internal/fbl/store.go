// Package fbl ingests feedback-loop (ARF) complaint emails and Gmail Postmaster Tools
// reputation, deriving a per-sending-domain complaint reputation. It is the engine behind
// cmd/fbld and the /v1/reputation API.
//
// Shared-relay note: a complaint identifies the SENDING domain (the From: of the
// complained-about message), not the SASL login — one relay credential fronts hundreds of
// client domains, so attribution must be per sender, mirroring cmd/abused.
package fbl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store runs this feature's SQL against the shared pgx pool. It deliberately owns no DDL
// and adds no methods to internal/store/postgres — it takes the exported *pgxpool.Pool
// handle ((*postgres.Store).Pool) and queries the 00008 tables directly.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgx pool (pass (*postgres.Store).Pool).
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Complaint is a parsed ARF feedback report to be recorded.
type Complaint struct {
	FeedbackType string
	SenderDomain string
	SASLUsername string
	Provider     string
	MessageID    string
}

// InsertComplaint records one parsed ARF complaint.
func (s *Store) InsertComplaint(ctx context.Context, c Complaint) error {
	const q = `
INSERT INTO fbl_complaints (feedback_type, sender_domain, sasl_username, provider, message_id)
VALUES (NULLIF($1,''), NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''))`
	_, err := s.pool.Exec(ctx, q, c.FeedbackType, c.SenderDomain, c.SASLUsername, c.Provider, c.MessageID)
	if err != nil {
		return fmt.Errorf("insert fbl complaint: %w", err)
	}
	return nil
}

// ComplaintCount24h returns how many complaints a sending domain has accrued in the last
// 24 hours — the simple raw-count signal used by the abuse rollup.
func (s *Store) ComplaintCount24h(ctx context.Context, domain string) (int, error) {
	const q = `SELECT count(*) FROM fbl_complaints
	           WHERE sender_domain = $1 AND received_at > now() - interval '24 hours'`
	var n int
	if err := s.pool.QueryRow(ctx, q, domain).Scan(&n); err != nil {
		return 0, fmt.Errorf("complaint count 24h: %w", err)
	}
	return n, nil
}

// UpsertReputation stores (or refreshes) a domain's Postmaster grade + spam rate.
func (s *Store) UpsertReputation(ctx context.Context, domain, source, reputation string, spamRate float64) error {
	const q = `
INSERT INTO domain_reputation (domain, source, reputation, spam_rate, fetched_at)
VALUES ($1, $2, NULLIF($3,''), $4, now())
ON CONFLICT (domain) DO UPDATE
   SET source = EXCLUDED.source,
       reputation = EXCLUDED.reputation,
       spam_rate = EXCLUDED.spam_rate,
       fetched_at = EXCLUDED.fetched_at`
	if _, err := s.pool.Exec(ctx, q, domain, source, reputation, spamRate); err != nil {
		return fmt.Errorf("upsert domain reputation: %w", err)
	}
	return nil
}

// ReputationRow is one row of the /v1/reputation view: a sending domain with its 24h and
// lifetime complaint counts plus its latest Postmaster grade (when available).
type ReputationRow struct {
	Domain        string
	Complaints24h int
	ComplaintsTot int
	Reputation    string     // "" when no Postmaster data
	SpamRate      *float64   // nil when no Postmaster data
	FetchedAt     *time.Time // nil when no Postmaster data
}

// ListReputation returns the union of every sender domain that has either complaints or a
// Postmaster reputation record, newest/worst-signal first by 24h complaint volume. A
// FULL OUTER JOIN is used because a domain may have Postmaster data without complaints (or
// vice versa). limit defaults to 100 (<=0), capped at 1000.
func (s *Store) ListReputation(ctx context.Context, limit int) ([]ReputationRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = `
WITH agg AS (
    SELECT sender_domain AS domain,
           count(*) FILTER (WHERE received_at > now() - interval '24 hours') AS c24,
           count(*) AS ctot
    FROM fbl_complaints
    WHERE sender_domain IS NOT NULL
    GROUP BY sender_domain
)
SELECT COALESCE(agg.domain, dr.domain)        AS domain,
       COALESCE(agg.c24, 0)                   AS complaints_24h,
       COALESCE(agg.ctot, 0)                  AS complaints_total,
       COALESCE(dr.reputation, '')            AS reputation,
       dr.spam_rate,
       dr.fetched_at
FROM agg
FULL OUTER JOIN domain_reputation dr ON dr.domain = agg.domain
ORDER BY complaints_24h DESC, complaints_total DESC, domain ASC
LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list reputation: %w", err)
	}
	defer rows.Close()

	var out []ReputationRow
	for rows.Next() {
		var r ReputationRow
		if err := rows.Scan(&r.Domain, &r.Complaints24h, &r.ComplaintsTot,
			&r.Reputation, &r.SpamRate, &r.FetchedAt); err != nil {
			return nil, fmt.Errorf("scan reputation row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReputationForDomain returns the latest stored Postmaster grade for a single domain.
// ok=false (nil error) when there is no record yet.
func (s *Store) ReputationForDomain(ctx context.Context, domain string) (reputation string, ok bool, err error) {
	const q = `SELECT COALESCE(reputation, '') FROM domain_reputation WHERE domain = $1`
	err = s.pool.QueryRow(ctx, q, domain).Scan(&reputation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reputation for domain: %w", err)
	}
	return reputation, true, nil
}
