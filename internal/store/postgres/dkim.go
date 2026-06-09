package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DKIMRotationPlan represents a planned or in-progress DKIM key rotation.
type DKIMRotationPlan struct {
	ID            string
	TenantID      string
	DomainID      string
	SelectorOld   string
	SelectorNew   string
	PublicKeyNew  string
	PrivateKeyNew string
	KeyBits       int
	Stage         string // pending, published, testing, active, retired
	DNSVerified   bool
	TestPassed    bool
	CreatedAt     time.Time
	ActivatedAt   *time.Time
	RetiredAt     *time.Time
}

// ListDKIMPlans returns all rotation plans for a domain, newest first.
func (s *Store) ListDKIMPlans(ctx context.Context, tenantID, domainID string) ([]DKIMRotationPlan, error) {
	const q = `
		SELECT id, tenant_id, domain_id, selector_old, selector_new,
		       public_key_new, private_key_new, key_bits, stage,
		       dns_verified, test_passed, created_at, activated_at, retired_at
		FROM dkim_rotation_plans
		WHERE tenant_id = $1 AND domain_id = $2
		ORDER BY created_at DESC`

	rows, err := s.Pool.Query(ctx, q, tenantID, domainID)
	if err != nil {
		return nil, fmt.Errorf("dkim: list plans: %w", err)
	}
	defer rows.Close()

	var plans []DKIMRotationPlan
	for rows.Next() {
		var p DKIMRotationPlan
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.DomainID, &p.SelectorOld, &p.SelectorNew,
			&p.PublicKeyNew, &p.PrivateKeyNew, &p.KeyBits, &p.Stage,
			&p.DNSVerified, &p.TestPassed, &p.CreatedAt, &p.ActivatedAt, &p.RetiredAt,
		); err != nil {
			return nil, fmt.Errorf("dkim: list plans scan: %w", err)
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dkim: list plans rows: %w", err)
	}
	return plans, nil
}

// CreateDKIMPlan inserts a new rotation plan in the pending stage and returns its ID.
func (s *Store) CreateDKIMPlan(ctx context.Context, tenantID, domainID, selectorNew, pubKey, privKey string, keyBits int) (string, error) {
	const q = `
		INSERT INTO dkim_rotation_plans
			(tenant_id, domain_id, selector_new, public_key_new, private_key_new, key_bits)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`

	var id string
	err := s.Pool.QueryRow(ctx, q, tenantID, domainID, selectorNew, pubKey, privKey, keyBits).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("dkim: create plan: %w", err)
	}
	return id, nil
}

// GetDKIMPlan fetches a single rotation plan by ID. Returns pgx.ErrNoRows if not found.
func (s *Store) GetDKIMPlan(ctx context.Context, tenantID, id string) (DKIMRotationPlan, error) {
	const q = `
		SELECT id, tenant_id, domain_id, selector_old, selector_new,
		       public_key_new, private_key_new, key_bits, stage,
		       dns_verified, test_passed, created_at, activated_at, retired_at
		FROM dkim_rotation_plans
		WHERE tenant_id = $1 AND id = $2`

	var p DKIMRotationPlan
	err := s.Pool.QueryRow(ctx, q, tenantID, id).Scan(
		&p.ID, &p.TenantID, &p.DomainID, &p.SelectorOld, &p.SelectorNew,
		&p.PublicKeyNew, &p.PrivateKeyNew, &p.KeyBits, &p.Stage,
		&p.DNSVerified, &p.TestPassed, &p.CreatedAt, &p.ActivatedAt, &p.RetiredAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DKIMRotationPlan{}, pgx.ErrNoRows
		}
		return DKIMRotationPlan{}, fmt.Errorf("dkim: get plan: %w", err)
	}
	return p, nil
}

// UpdateDKIMStage sets the stage of a rotation plan. It stamps activated_at when
// stage becomes "active" and retired_at when stage becomes "retired". Returns true
// if a row was updated.
func (s *Store) UpdateDKIMStage(ctx context.Context, tenantID, id, stage string) (bool, error) {
	const q = `
		UPDATE dkim_rotation_plans
		SET stage        = $3,
		    activated_at = CASE WHEN $3 = 'active'  THEN COALESCE(activated_at, now()) ELSE activated_at END,
		    retired_at   = CASE WHEN $3 = 'retired' THEN COALESCE(retired_at,   now()) ELSE retired_at   END
		WHERE tenant_id = $1 AND id = $2`

	tag, err := s.Pool.Exec(ctx, q, tenantID, id, stage)
	if err != nil {
		return false, fmt.Errorf("dkim: update stage: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetDKIMVerified updates the dns_verified and test_passed flags on a rotation plan.
// Returns true if a row was updated.
func (s *Store) SetDKIMVerified(ctx context.Context, tenantID, id string, dnsVerified, testPassed bool) (bool, error) {
	const q = `
		UPDATE dkim_rotation_plans
		SET dns_verified = $3,
		    test_passed  = $4
		WHERE tenant_id = $1 AND id = $2`

	tag, err := s.Pool.Exec(ctx, q, tenantID, id, dnsVerified, testPassed)
	if err != nil {
		return false, fmt.Errorf("dkim: set verified: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteDKIMPlan removes a rotation plan. Returns true if a row was deleted.
func (s *Store) DeleteDKIMPlan(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM dkim_rotation_plans WHERE tenant_id = $1 AND id = $2`

	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("dkim: delete plan: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
