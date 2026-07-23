package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/healthscore"
)

// registerHealthScoreRoutes wires the Deliverability Health Score endpoints. The orchestrator
// calls this from server.go. All routes are reads (ScopeRead).
//
//	GET /v1/health-score                       -> tenant summary + per-domain latest scores
//	GET /v1/domains/{id}/health-score          -> one domain's score + component breakdown
//	GET /v1/domains/{id}/health-score/history  -> one domain's score trend (newest first)
func (s *Server) registerHealthScoreRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/health-score", s.requireScope(ScopeRead, s.handleHealthScoreSummary))
	mux.HandleFunc("GET /v1/domains/{id}/health-score", s.requireScope(ScopeRead, s.handleDomainHealthScore))
	mux.HandleFunc("GET /v1/domains/{id}/health-score/history", s.requireScope(ScopeRead, s.handleDomainHealthScoreHistory))
}

// ---- response shapes -------------------------------------------------------

type healthScoreDomainJSON struct {
	DomainID   string                       `json:"domain_id"`
	Domain     string                       `json:"domain"`
	Score      float64                      `json:"score"`
	Grade      string                       `json:"grade"`
	HasData    bool                         `json:"has_data"`
	Coverage   float64                      `json:"coverage"`
	Pending    bool                         `json:"pending"` // true when never scored yet
	ComputedAt *string                      `json:"computed_at"`
	Components []healthscore.ComponentScore `json:"components,omitempty"`
}

type healthScoreSummaryJSON struct {
	Tenant struct {
		Score        float64 `json:"score"`
		Grade        string  `json:"grade"`
		DomainsTotal int     `json:"domains_total"`
		DomainsRated int     `json:"domains_rated"`
	} `json:"tenant"`
	Domains []healthScoreDomainJSON `json:"domains"`
}

// handleHealthScoreSummary returns the latest score for every domain of the tenant plus a
// tenant-level composite (the mean of rated domains). Domains not yet scored appear as pending.
func (s *Server) handleHealthScoreSummary(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)

	domains, err := s.pg.ListDomains(r.Context(), tenant)
	if err != nil {
		s.log.Error("health-score list domains", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list domains")
		return
	}
	snaps, err := s.pg.LatestHealthScores(r.Context(), tenant)
	if err != nil {
		s.log.Error("health-score latest", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read health scores")
		return
	}
	byDomainID := make(map[string]int, len(snaps))
	for i, sn := range snaps {
		byDomainID[sn.DomainID] = i
	}

	out := healthScoreSummaryJSON{Domains: make([]healthScoreDomainJSON, 0, len(domains))}
	var sum float64
	var rated int
	for _, d := range domains {
		item := healthScoreDomainJSON{DomainID: d.ID, Domain: d.Name, Grade: "N/A", Pending: true}
		if idx, ok := byDomainID[d.ID]; ok {
			sn := snaps[idx]
			item = healthScoreDomainJSON{
				DomainID:   d.ID,
				Domain:     d.Name,
				Score:      sn.Score,
				Grade:      sn.Grade,
				HasData:    sn.HasData,
				Coverage:   sn.Coverage,
				Pending:    false,
				ComputedAt: rfc3339PtrVal(sn.ComputedAt),
			}
			if sn.HasData {
				sum += sn.Score
				rated++
			}
		}
		out.Domains = append(out.Domains, item)
	}

	out.Tenant.DomainsTotal = len(domains)
	out.Tenant.DomainsRated = rated
	if rated > 0 {
		out.Tenant.Score = round1(sum / float64(rated))
		out.Tenant.Grade = healthscore.Grade(out.Tenant.Score)
	} else {
		out.Tenant.Grade = "N/A"
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDomainHealthScore returns one domain's latest persisted score with its full component
// breakdown. If the domain has never been scored (cmd/scored hasn't run yet), it computes a
// fresh score live from the collector so the view is never empty.
func (s *Server) handleDomainHealthScore(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	domainID := r.PathValue("id")

	dh, found, err := s.pg.GetDomainHealth(r.Context(), tenant, domainID)
	if err != nil {
		s.log.Error("health-score get domain", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read domain")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}

	// Prefer the latest persisted snapshot (carries the trend context).
	snap, ok, err := s.pg.LatestHealthScoreForDomain(r.Context(), tenant, domainID)
	if err != nil {
		s.log.Error("health-score latest domain", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read health score")
		return
	}
	if ok {
		var comps []healthscore.ComponentScore
		_ = json.Unmarshal(snap.Components, &comps)
		writeJSON(w, http.StatusOK, healthScoreDomainJSON{
			DomainID:   domainID,
			Domain:     dh.Name,
			Score:      snap.Score,
			Grade:      snap.Grade,
			HasData:    snap.HasData,
			Coverage:   snap.Coverage,
			ComputedAt: rfc3339PtrVal(snap.ComputedAt),
			Components: comps,
		})
		return
	}

	// Fallback: compute live (not persisted — cmd/scored owns persistence).
	res, err := healthscore.NewCollector(s.pg, s.ch).ScoreDomain(r.Context(), tenant, dh.Name, time.Now())
	if err != nil {
		s.log.Error("health-score live compute", "tenant_id", tenant, "domain", dh.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to compute health score")
		return
	}
	writeJSON(w, http.StatusOK, healthScoreDomainJSON{
		DomainID:   domainID,
		Domain:     dh.Name,
		Score:      res.Score,
		Grade:      res.Grade,
		HasData:    res.HasData,
		Coverage:   res.Coverage,
		Pending:    true, // computed on the fly, not yet snapshotted
		Components: res.Components,
	})
}

// handleDomainHealthScoreHistory returns a domain's score trend (newest first). ?limit caps rows.
func (s *Server) handleDomainHealthScoreHistory(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	domainID := r.PathValue("id")

	// Ownership check (also yields a 404 for foreign/unknown domains).
	if _, found, err := s.pg.GetDomainHealth(r.Context(), tenant, domainID); err != nil {
		s.log.Error("health-score history owner check", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read domain")
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}

	limit := parseIntParam(r, "limit", 100, 1000)
	snaps, err := s.pg.HealthScoreHistory(r.Context(), tenant, domainID, limit)
	if err != nil {
		s.log.Error("health-score history", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read history")
		return
	}

	type point struct {
		Score      float64 `json:"score"`
		Grade      string  `json:"grade"`
		HasData    bool    `json:"has_data"`
		Coverage   float64 `json:"coverage"`
		ComputedAt string  `json:"computed_at"`
	}
	points := make([]point, 0, len(snaps))
	for _, sn := range snaps {
		points = append(points, point{
			Score:      sn.Score,
			Grade:      sn.Grade,
			HasData:    sn.HasData,
			Coverage:   sn.Coverage,
			ComputedAt: sn.ComputedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": points})
}

// ---- helpers ---------------------------------------------------------------

func rfc3339PtrVal(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}
