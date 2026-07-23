package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/bimi"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// registerBIMIRoutes wires the BIMI/VMC readiness endpoints. The orchestrator calls this from
// server.go.
//
//	GET  /v1/bimi                        - readiness summary across all the tenant's domains
//	GET  /v1/domains/{id}/bimi           - one domain's readiness detail + checklist
//	POST /v1/domains/{id}/bimi/recheck   - perform a live BIMI assessment now and snapshot it
func (s *Server) registerBIMIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/bimi", s.requireScope(ScopeRead, s.handleBIMISummary))
	mux.HandleFunc("GET /v1/domains/{id}/bimi", s.requireScope(ScopeRead, s.handleBIMIDetail))
	mux.HandleFunc("POST /v1/domains/{id}/bimi/recheck", s.requireScope(ScopeWrite, s.handleBIMIRecheck))
}

// ---- response shapes -------------------------------------------------------

type bimiSummaryItem struct {
	DomainID      string  `json:"domain_id"`
	Domain        string  `json:"domain"`
	Readiness     string  `json:"readiness_state"`
	DMARCEnforced bool    `json:"dmarc_enforced"`
	LogoURL       string  `json:"logo_url"`
	VMCURL        string  `json:"vmc_url"`
	VMCExpiry     *string `json:"vmc_expiry"`
	CheckedAt     *string `json:"checked_at"`
}

type bimiDetailJSON struct {
	DomainID      string          `json:"domain_id"`
	Domain        string          `json:"domain"`
	Readiness     string          `json:"readiness_state"`
	Record        string          `json:"record"`
	LogoURL       string          `json:"logo_url"`
	VMCURL        string          `json:"vmc_url"`
	VMCExpiry     *string         `json:"vmc_expiry"`
	DMARCEnforced bool            `json:"dmarc_enforced"`
	Checklist     json.RawMessage `json:"checklist"`
	CheckedAt     *string         `json:"checked_at"`
}

func bimiTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func bimiDetailFromSnapshot(b pgstore.BIMISnapshot) bimiDetailJSON {
	checklist := b.Checklist
	if len(checklist) == 0 {
		checklist = json.RawMessage("[]")
	}
	checkedAt := b.CheckedAt.UTC().Format(time.RFC3339)
	return bimiDetailJSON{
		DomainID:      b.DomainID,
		Domain:        b.DomainName,
		Readiness:     b.Readiness,
		Record:        b.Record,
		LogoURL:       b.LogoURL,
		VMCURL:        b.VMCURL,
		VMCExpiry:     bimiTimePtr(b.VMCExpiry),
		DMARCEnforced: b.DMARCEnforced,
		Checklist:     checklist,
		CheckedAt:     &checkedAt,
	}
}

// ---- handlers --------------------------------------------------------------

// GET /v1/bimi
func (s *Server) handleBIMISummary(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)

	domains, err := s.pg.ListDomainHealth(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	snaps, err := s.pg.ListLatestBIMISnapshots(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	byDomain := make(map[string]pgstore.BIMISnapshot, len(snaps))
	for _, b := range snaps {
		byDomain[b.DomainID] = b
	}

	out := make([]bimiSummaryItem, 0, len(domains))
	for _, d := range domains {
		item := bimiSummaryItem{DomainID: d.DomainID, Domain: d.Name, Readiness: "unknown"}
		if b, ok := byDomain[d.DomainID]; ok {
			checkedAt := b.CheckedAt.UTC().Format(time.RFC3339)
			item.Readiness = b.Readiness
			item.DMARCEnforced = b.DMARCEnforced
			item.LogoURL = b.LogoURL
			item.VMCURL = b.VMCURL
			item.VMCExpiry = bimiTimePtr(b.VMCExpiry)
			item.CheckedAt = &checkedAt
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

// GET /v1/domains/{id}/bimi
func (s *Server) handleBIMIDetail(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	domainID := r.PathValue("id")

	dom, found, err := s.pg.GetBIMIDomain(r.Context(), tenantID, domainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}

	snap, ok, err := s.pg.GetBIMISnapshotForTenant(r.Context(), tenantID, domainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !ok {
		// No snapshot yet — return an empty, unknown detail rather than 404 so the UI can
		// offer a "Check now" action.
		writeJSON(w, http.StatusOK, bimiDetailJSON{
			DomainID:  dom.ID,
			Domain:    dom.Name,
			Readiness: "unknown",
			Checklist: json.RawMessage("[]"),
		})
		return
	}
	writeJSON(w, http.StatusOK, bimiDetailFromSnapshot(snap))
}

// POST /v1/domains/{id}/bimi/recheck
func (s *Server) handleBIMIRecheck(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	domainID := r.PathValue("id")

	dom, found, err := s.pg.GetBIMIDomain(r.Context(), tenantID, domainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}

	dmarcRecord, err := s.pg.LatestDMARCRecord(r.Context(), domainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	cfg := bimi.LoadConfig()
	resolver := dnsx.NewSystemResolver(5 * time.Second)
	fetcher := bimi.NewHTTPFetcher(cfg.FetchTimeout)

	rep, err := bimi.Inspect(r.Context(), resolver, fetcher, dom.Name, dmarcRecord)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bimi_lookup_failed", err.Error())
		return
	}

	checklist, _ := json.Marshal(rep.Checklist)
	snap := pgstore.BIMISnapshot{
		TenantID:      tenantID,
		DomainID:      domainID,
		DomainName:    dom.Name,
		Record:        rep.Record.Raw,
		LogoURL:       rep.LogoURL,
		VMCURL:        rep.VMCURL,
		VMCExpiry:     rep.VMCExpiry,
		DMARCEnforced: rep.DMARCEnforced,
		Readiness:     rep.Readiness,
		Checklist:     checklist,
		CheckedAt:     time.Now(),
	}
	if _, err := s.pg.InsertBIMISnapshot(r.Context(), snap); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bimiDetailFromSnapshot(snap))
}
