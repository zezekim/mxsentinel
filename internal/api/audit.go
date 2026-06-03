package api

import (
	"context"
	"net/http"
	"time"
)

// auditWrites records every mutating request (method/path/status + tenant + credential)
// to the audit log. Recording is best-effort and asynchronous — it never blocks or fails
// the request. Runs after requireAuth so the tenant/credential are known.
func (s *Server) auditWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWrite(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		a, ok := authFromContext(r.Context())
		if !ok {
			return
		}
		tenant, cred := a.TenantID, a.CredID
		method, path, status := r.Method, r.URL.Path, sw.status
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.pg.InsertAuditEvent(ctx, tenant, cred, method, path, status); err != nil {
				s.log.Warn("audit insert failed", "err", err)
			}
		}()
	})
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

type auditEventJSON struct {
	ID           string  `json:"id"`
	CredentialID *string `json:"credential_id"`
	Method       string  `json:"method"`
	Path         string  `json:"path"`
	Status       int     `json:"status"`
	CreatedAt    string  `json:"created_at"`
}

// handleAudit lists a tenant's recent audit events.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pg.ListAuditEvents(r.Context(), s.tenant(r), parseIntParam(r, "limit", 50, 500))
	if err != nil {
		s.log.Error("list audit events", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list audit events")
		return
	}
	items := make([]auditEventJSON, 0, len(rows))
	for _, e := range rows {
		items = append(items, auditEventJSON{
			ID: e.ID, CredentialID: e.CredentialID, Method: e.Method, Path: e.Path,
			Status: e.Status, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_events": items})
}
