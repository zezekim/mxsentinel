package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/seedtest"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// registerSeedTestRoutes wires the inbox-placement / seed-list testing endpoints. The
// orchestrator calls this from server.go (see INTEGRATION_inbox-placement.md).
func (s *Server) registerSeedTestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/seed-lists", s.requireScope(ScopeRead, s.handleListSeedLists))
	mux.HandleFunc("POST /v1/seed-lists", s.requireScope(ScopeWrite, s.handleCreateSeedList))
	mux.HandleFunc("GET /v1/seed-lists/{id}", s.requireScope(ScopeRead, s.handleGetSeedList))
	mux.HandleFunc("DELETE /v1/seed-lists/{id}", s.requireScope(ScopeAdmin, s.handleDeleteSeedList))
	mux.HandleFunc("GET /v1/seed-tests", s.requireScope(ScopeRead, s.handleListSeedRuns))
	mux.HandleFunc("POST /v1/seed-tests", s.requireScope(ScopeWrite, s.handleStartSeedRun))
	mux.HandleFunc("GET /v1/seed-tests/{id}", s.requireScope(ScopeRead, s.handleGetSeedRun))
}

// ── JSON shapes ─────────────────────────────────────────────────────────────

type seedAddressJSON struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
}

type seedListJSON struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	AddressCount int               `json:"address_count"`
	Addresses    []seedAddressJSON `json:"addresses,omitempty"`
	CreatedAt    string            `json:"created_at"`
}

type seedRunJSON struct {
	ID          string  `json:"id"`
	ListID      *string `json:"list_id"`
	Name        string  `json:"name"`
	RunTag      string  `json:"run_tag"`
	FromAddress string  `json:"from_address"`
	IPPool      string  `json:"ip_pool"`
	Status      string  `json:"status"`
	SeedCount   int     `json:"seed_count"`
	SentCount   int     `json:"sent_count"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	CreatedAt   string  `json:"created_at"`
}

type seedResultJSON struct {
	Address    string  `json:"address"`
	Provider   string  `json:"provider"`
	Status     string  `json:"status"`
	Placement  string  `json:"placement"`
	Mailbox    string  `json:"mailbox"`
	SPFPass    *bool   `json:"spf_pass"`
	DKIMPass   *bool   `json:"dkim_pass"`
	DMARCPass  *bool   `json:"dmarc_pass"`
	Detail     string  `json:"detail"`
	SentAt     *string `json:"sent_at"`
	ObservedAt *string `json:"observed_at"`
}

type seedRunDetailJSON struct {
	seedRunJSON
	Summary seedtest.Summary `json:"summary"`
	Results []seedResultJSON `json:"results"`
}

func formatSeedList(l pgstore.SeedList) seedListJSON {
	return seedListJSON{
		ID:           l.ID,
		Name:         l.Name,
		Description:  l.Description,
		AddressCount: l.AddressCount,
		CreatedAt:    l.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func formatSeedRun(r pgstore.SeedRun) seedRunJSON {
	return seedRunJSON{
		ID:          r.ID,
		ListID:      r.ListID,
		Name:        r.Name,
		RunTag:      r.RunTag,
		FromAddress: r.FromAddress,
		IPPool:      r.IPPool,
		Status:      r.Status,
		SeedCount:   r.SeedCount,
		SentCount:   r.SentCount,
		StartedAt:   rfc3339Ptr(r.StartedAt),
		CompletedAt: rfc3339Ptr(r.CompletedAt),
		CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func formatSeedResult(r pgstore.SeedResult) seedResultJSON {
	return seedResultJSON{
		Address:    r.Address,
		Provider:   r.Provider,
		Status:     r.Status,
		Placement:  r.Placement,
		Mailbox:    r.Mailbox,
		SPFPass:    r.SPFPass,
		DKIMPass:   r.DKIMPass,
		DMARCPass:  r.DMARCPass,
		Detail:     r.Detail,
		SentAt:     rfc3339Ptr(r.SentAt),
		ObservedAt: rfc3339Ptr(r.ObservedAt),
	}
}

// ── Seed lists ──────────────────────────────────────────────────────────────

// GET /v1/seed-lists
func (s *Server) handleListSeedLists(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	lists, err := s.pg.ListSeedLists(r.Context(), tenant)
	if err != nil {
		s.log.Error("list seed lists", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list seed lists")
		return
	}
	out := make([]seedListJSON, 0, len(lists))
	for _, l := range lists {
		out = append(out, formatSeedList(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"lists": out})
}

// POST /v1/seed-lists
//
//	{ "name": "...", "description": "...",
//	  "addresses": [ {"address": "seed@gmail.com", "provider": "gmail"}, ... ] }
//
// provider is optional per address; when omitted it is inferred from the address domain.
func (s *Server) handleCreateSeedList(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Addresses   []struct {
			Address  string `json:"address"`
			Provider string `json:"provider"`
		} `json:"addresses"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}
	id, err := s.pg.CreateSeedList(r.Context(), tenant, req.Name, req.Description)
	if err != nil {
		s.log.Error("create seed list", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create seed list")
		return
	}
	for _, a := range req.Addresses {
		if a.Address == "" {
			continue
		}
		provider := a.Provider
		if provider == "" {
			provider = seedtest.ProviderForAddress(a.Address)
		}
		if _, err := s.pg.AddSeedAddress(r.Context(), tenant, id, a.Address, seedtest.NormalizeProvider(provider)); err != nil {
			s.log.Warn("add seed address", "err", err, "tenant_id", tenant, "address", a.Address)
		}
	}
	list, err := s.pg.GetSeedList(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list created but could not be retrieved")
		return
	}
	writeJSON(w, http.StatusCreated, formatSeedList(list))
}

// GET /v1/seed-lists/{id}
func (s *Server) handleGetSeedList(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	list, err := s.pg.GetSeedList(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "seed list not found")
		return
	}
	addrs, err := s.pg.ListSeedAddresses(r.Context(), list.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load addresses")
		return
	}
	out := formatSeedList(list)
	out.Addresses = make([]seedAddressJSON, 0, len(addrs))
	for _, a := range addrs {
		out.Addresses = append(out.Addresses, seedAddressJSON{ID: a.ID, Address: a.Address, Provider: a.Provider, Enabled: a.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /v1/seed-lists/{id}
func (s *Server) handleDeleteSeedList(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	found, err := s.pg.DeleteSeedList(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete seed list")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "seed list not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Seed runs ───────────────────────────────────────────────────────────────

// GET /v1/seed-tests
func (s *Server) handleListSeedRuns(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	runs, err := s.pg.ListSeedRuns(r.Context(), tenant, 100)
	if err != nil {
		s.log.Error("list seed runs", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list seed tests")
		return
	}
	out := make([]seedRunJSON, 0, len(runs))
	for _, run := range runs {
		out = append(out, formatSeedRun(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// POST /v1/seed-tests
//
//	{ "list_id": "...", "name": "...", "from_address": "...", "ip_pool": "..." }
//
// Creates a run and one pending result per enabled seed in the list. The seedd daemon then
// sends the probes and polls for placement; the run is returned immediately in "pending" state.
func (s *Server) handleStartSeedRun(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	var req struct {
		ListID      string `json:"list_id"`
		Name        string `json:"name"`
		FromAddress string `json:"from_address"`
		IPPool      string `json:"ip_pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.ListID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "list_id is required")
		return
	}
	list, err := s.pg.GetSeedList(r.Context(), tenant, req.ListID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "seed list not found")
		return
	}
	addrs, err := s.pg.ListSeedAddresses(r.Context(), list.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load seed addresses")
		return
	}
	results := make([]pgstore.NewSeedResultInput, 0, len(addrs))
	for _, a := range addrs {
		if !a.Enabled {
			continue
		}
		results = append(results, pgstore.NewSeedResultInput{
			Address:  a.Address,
			Provider: a.Provider,
			ProbeTag: seedtest.NewProbeTag(),
		})
	}
	if len(results) == 0 {
		writeError(w, http.StatusBadRequest, "empty_list", "seed list has no enabled addresses")
		return
	}
	runTag := seedtest.NewProbeTag()
	runID, err := s.pg.CreateSeedRun(r.Context(), tenant, list.ID, req.Name, runTag, req.FromAddress, req.IPPool, results)
	if err != nil {
		s.log.Error("create seed run", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to start seed test")
		return
	}
	run, err := s.pg.GetSeedRun(r.Context(), tenant, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "run created but could not be retrieved")
		return
	}
	s.log.Info("seed run started", "tenant_id", tenant, "run_id", runID, "seeds", len(results))
	writeJSON(w, http.StatusCreated, formatSeedRun(run))
}

// GET /v1/seed-tests/{id}
func (s *Server) handleGetSeedRun(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	run, err := s.pg.GetSeedRun(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "seed test not found")
		return
	}
	rawResults, err := s.pg.ListSeedResults(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load results")
		return
	}
	results := make([]seedResultJSON, 0, len(rawResults))
	summaryInput := make([]seedtest.Result, 0, len(rawResults))
	for _, res := range rawResults {
		results = append(results, formatSeedResult(res))
		summaryInput = append(summaryInput, seedtest.Result{
			Address:   res.Address,
			Provider:  res.Provider,
			Status:    res.Status,
			Placement: seedtest.Placement(res.Placement),
			SPFPass:   res.SPFPass,
			DKIMPass:  res.DKIMPass,
			DMARCPass: res.DMARCPass,
		})
	}
	detail := seedRunDetailJSON{
		seedRunJSON: formatSeedRun(run),
		Summary:     seedtest.Summarize(summaryInput),
		Results:     results,
	}
	writeJSON(w, http.StatusOK, detail)
}
