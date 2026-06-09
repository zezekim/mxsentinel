package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// statusTenantJSON is the minimal tenant projection exposed on the public status page.
type statusTenantJSON struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// statusDomainJSON is the minimal domain summary exposed on the public status page.
type statusDomainJSON struct {
	Name    string `json:"name"`
	Overall string `json:"overall"`
}

// statusIncidentJSON is the minimal incident summary exposed on the public status page.
type statusIncidentJSON struct {
	Kind      string `json:"kind"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Domain    string `json:"domain"`
	CreatedAt string `json:"created_at"`
}

// publicStatusResponse is the full shape returned by GET /v1/status/{slug}.
type publicStatusResponse struct {
	Tenant        statusTenantJSON     `json:"tenant"`
	Status        string               `json:"status"`
	Domains       []statusDomainJSON   `json:"domains"`
	OpenIncidents []statusIncidentJSON `json:"open_incidents"`
	CheckedAt     string               `json:"checked_at"`
}

// domainOverall maps a domain's internal status string to the public-facing health label.
//
//	monitored → healthy
//	paused    → unknown
//	error     → critical
//	(any other value) → unknown
func domainOverall(status string) string {
	switch status {
	case "monitored":
		return "healthy"
	case "paused":
		return "unknown"
	case "error":
		return "critical"
	default:
		return "unknown"
	}
}

// handlePublicStatus serves the public status page data for a tenant identified by slug.
//
// GET /v1/status/{slug}
//
// No authentication is required. The response deliberately exposes MINIMAL data:
// no internal IDs, no raw telemetry, no configuration details.
//
// Status derivation:
//   - 0 open incidents            → "operational"
//   - any open critical incident  → "down"
//   - any other open incident     → "degraded"
func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "slug is required")
		return
	}

	ctx := r.Context()

	tenant, err := s.pg.GetTenantBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "tenant not found")
			return
		}
		s.log.Error("get tenant by slug", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to look up tenant")
		return
	}

	domains, err := s.pg.ListDomains(ctx, tenant.ID)
	if err != nil {
		s.log.Error("list domains for status page", "tenant_id", tenant.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load domains")
		return
	}

	// Fetch open incidents only; cap at 10 for the status page.
	incidents, err := s.pg.ListIncidents(ctx, tenant.ID, "open", "", 10, 0)
	if err != nil {
		s.log.Error("list incidents for status page", "tenant_id", tenant.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load incidents")
		return
	}

	// Derive the overall status from the open incident set.
	overallStatus := "operational"
	for _, inc := range incidents {
		if inc.Severity == "critical" {
			overallStatus = "down"
			break
		}
		overallStatus = "degraded"
	}

	// Build the domain summary list.
	domainItems := make([]statusDomainJSON, 0, len(domains))
	for _, d := range domains {
		domainItems = append(domainItems, statusDomainJSON{
			Name:    d.Name,
			Overall: domainOverall(d.Status),
		})
	}

	// Build the incident summary list (no IDs, no internal details).
	incidentItems := make([]statusIncidentJSON, 0, len(incidents))
	for _, inc := range incidents {
		incidentItems = append(incidentItems, statusIncidentJSON{
			Kind:      inc.Kind,
			Severity:  inc.Severity,
			Title:     inc.Title,
			Domain:    inc.Domain,
			CreatedAt: inc.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, publicStatusResponse{
		Tenant: statusTenantJSON{
			Name: tenant.Name,
			Slug: tenant.Slug,
		},
		Status:        overallStatus,
		Domains:       domainItems,
		OpenIncidents: incidentItems,
		CheckedAt:     time.Now().UTC().Format(time.RFC3339),
	})
}
