package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Incident is a minimal projection of the incidents table.
type Incident struct {
	ID            string
	TenantID      string
	SourceEventID string
	Kind          string
	Severity      string
	Domain        string
	Subject       string
	Title         string
	Detail        json.RawMessage
	Status        string
	Confidence    *float64
	CreatedAt     time.Time
	ResolvedAt    *time.Time

	// AI diagnostics enrichment (Phase 3); nil/empty until aid analyzes the incident.
	AISummary     *string
	AIRemediation json.RawMessage
	AIModel       *string
	AIAnalyzedAt  *time.Time
}

// incidentColumns is the shared SELECT list; scanIncident reads rows in this order.
const incidentColumns = `id, tenant_id, source_event_id, kind::text, severity::text,
	COALESCE(domain, ''), COALESCE(subject, ''), title, detail, status::text,
	confidence, created_at, resolved_at, ai_summary, ai_remediation, ai_model, ai_analyzed_at`

func scanIncident(rows pgx.Rows) (Incident, error) {
	var inc Incident
	err := rows.Scan(
		&inc.ID, &inc.TenantID, &inc.SourceEventID, &inc.Kind, &inc.Severity,
		&inc.Domain, &inc.Subject, &inc.Title, &inc.Detail, &inc.Status,
		&inc.Confidence, &inc.CreatedAt, &inc.ResolvedAt,
		&inc.AISummary, &inc.AIRemediation, &inc.AIModel, &inc.AIAnalyzedAt,
	)
	return inc, err
}

// IncidentInput carries the fields needed to create a new incident.
type IncidentInput struct {
	TenantID      string
	SourceEventID string
	Kind          string // incident_kind label
	Severity      string // dns_finding_sev label
	Domain        string // "" -> NULL
	Subject       string
	Title         string
	Detail        json.RawMessage // nil/empty -> '{}'
	Confidence    *float64        // nil -> NULL
}

// InsertIncident inserts an incident idempotently. created=false (nil error) when an
// incident for (tenant_id, source_event_id) already exists.
func (s *Store) InsertIncident(ctx context.Context, in IncidentInput) (id string, created bool, err error) {
	const q = `
INSERT INTO incidents (tenant_id, source_event_id, kind, severity, domain, subject, title, detail, confidence)
VALUES ($1, $2, $3::incident_kind, $4::dns_finding_sev, NULLIF($5,'')::citext, $6, $7, COALESCE($8, '{}')::jsonb, $9)
ON CONFLICT (tenant_id, source_event_id) DO NOTHING
RETURNING id`

	var detail interface{}
	if len(in.Detail) > 0 {
		detail = in.Detail
	}

	err = s.Pool.QueryRow(ctx, q,
		in.TenantID,
		in.SourceEventID,
		in.Kind,
		in.Severity,
		in.Domain,
		in.Subject,
		in.Title,
		detail,
		in.Confidence,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil // conflict: already exists
	}
	if err != nil {
		return "", false, fmt.Errorf("insert incident: %w", err)
	}
	return id, true, nil
}

// ListIncidents returns a tenant's incidents newest-first. status and domain filters
// are optional ("" = no filter). limit defaults to 50 (<=0), capped at 500.
func (s *Store) ListIncidents(ctx context.Context, tenantID, status, domain string, limit int) ([]Incident, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	args := []any{tenantID}
	q := `SELECT ` + incidentColumns + ` FROM incidents WHERE tenant_id = $1`

	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status = $%d::incident_status", len(args))
	}
	if domain != "" {
		args = append(args, domain)
		q += fmt.Sprintf(" AND domain = $%d", len(args))
	}

	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()

	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// ListIncidentsNeedingAI returns incidents (across all tenants) that have not yet been
// analyzed by the AI layer, oldest first. Used by cmd/aid.
func (s *Store) ListIncidentsNeedingAI(ctx context.Context, limit int) ([]Incident, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT ` + incidentColumns + `
	      FROM incidents WHERE ai_analyzed_at IS NULL
	      ORDER BY created_at ASC LIMIT $1`
	rows, err := s.Pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list incidents needing ai: %w", err)
	}
	defer rows.Close()

	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// SetIncidentAI writes the AI-generated summary, structured remediation, and model name,
// and stamps ai_analyzed_at so the incident is not re-analyzed.
func (s *Store) SetIncidentAI(ctx context.Context, id, summary string, remediation json.RawMessage, model string) error {
	var rem interface{}
	if len(remediation) > 0 {
		rem = remediation
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE incidents
		 SET ai_summary = $2, ai_remediation = COALESCE($3, '[]')::jsonb,
		     ai_model = $4, ai_analyzed_at = now()
		 WHERE id = $1`,
		id, summary, rem, model,
	)
	if err != nil {
		return fmt.Errorf("set incident ai: %w", err)
	}
	return nil
}

// ResolveIncident marks an incident resolved. found=false if it doesn't exist for the tenant.
func (s *Store) ResolveIncident(ctx context.Context, tenantID, id string) (found bool, err error) {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE incidents SET status = 'resolved', resolved_at = now() WHERE id = $2 AND tenant_id = $1`,
		tenantID, id,
	)
	if err != nil {
		return false, fmt.Errorf("resolve incident: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecentIncidentTitles returns recent incident titles for a tenant's domain (excluding
// excludeID), newest first — historical context for AI diagnosis.
func (s *Store) RecentIncidentTitles(ctx context.Context, tenantID, domain, excludeID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5
	}
	const q = `SELECT title FROM incidents
	           WHERE tenant_id = $1 AND domain = $2 AND id <> $3
	           ORDER BY created_at DESC LIMIT $4`
	rows, err := s.Pool.Query(ctx, q, tenantID, domain, excludeID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent incident titles: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan incident title: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
