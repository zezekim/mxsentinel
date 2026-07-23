package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// NLQueryLogEntry is one recorded natural-language analytics request (POST /v1/ask). It holds
// only the operator's question, the whitelisted aggregate queries the planner chose, and the
// composed answer — never any raw mail content.
type NLQueryLogEntry struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	Question    string          `json:"question"`
	ChosenTools json.RawMessage `json:"chosen_tools"`
	Answer      string          `json:"answer"`
	CreatedAt   time.Time       `json:"created_at"`
}

// InsertNLQueryLog records a completed NL-analytics request for auditability and returns the
// new row id. chosenTools is a JSON array of {tool,args} objects (pass "null"/nil for none).
func (s *Store) InsertNLQueryLog(ctx context.Context, tenantID, question string, chosenTools []byte, answer string) (string, error) {
	if len(chosenTools) == 0 {
		chosenTools = []byte("[]")
	}
	const q = `
		INSERT INTO nl_query_log (tenant_id, question, chosen_tools, answer)
		VALUES ($1, $2, $3, $4)
		RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, question, chosenTools, answer).Scan(&id); err != nil {
		return "", fmt.Errorf("insert nl query log: %w", err)
	}
	return id, nil
}

// ListNLQueryLog returns the most recent NL-analytics requests for a tenant, newest first.
func (s *Store) ListNLQueryLog(ctx context.Context, tenantID string, limit int) ([]NLQueryLogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id, tenant_id, question, chosen_tools, answer, created_at
		FROM nl_query_log
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list nl query log: %w", err)
	}
	defer rows.Close()

	var out []NLQueryLogEntry
	for rows.Next() {
		var e NLQueryLogEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Question, &e.ChosenTools, &e.Answer, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan nl query log: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list nl query log rows: %w", err)
	}
	return out, nil
}
