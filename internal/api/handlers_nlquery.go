package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/ai"
	"github.com/zezekim/mxsentinel/internal/nlquery"
)

// registerNLQueryRoutes wires the natural-language analytics ("ask your mail logs") endpoints.
// Both are reads: /v1/ask runs a question through the whitelisted-query planner + local LLM;
// /v1/ask/history returns the tenant's recent questions. The orchestrator calls this from
// server.go.
func (s *Server) registerNLQueryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ask", s.requireScope(ScopeRead, s.handleAsk))
	mux.HandleFunc("GET /v1/ask/history", s.requireScope(ScopeRead, s.handleAskHistory))
}

type askRequest struct {
	Question string `json:"question"`
}

// handleAsk answers a plain-English analytics question. The LLM only ever plans over a fixed
// whitelist of aggregate queries (internal/nlquery) — it never receives message bodies or
// subject lines, and cannot emit free-form SQL. The chosen aggregate results are returned
// alongside the composed answer so the UI can show the operator exactly what backed it.
//
// POST /v1/ask  {"question": "..."} -> {"answer": "...", "used_queries": [...], "data": [...]}
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if len(req.Question) == 0 || len(req.Question) > 2000 {
		writeError(w, http.StatusBadRequest, "bad_request", "question must be 1–2000 characters")
		return
	}
	tenant := s.tenant(r)

	cfg := nlquery.LoadConfig() // reuses MXS_AI_ENDPOINT / MXS_AI_MODEL / MXS_AI_APIKEY
	client := ai.NewOpenAIClient(cfg.Endpoint, cfg.APIKey, cfg.Model, time.Duration(cfg.TimeoutSecs)*time.Second)

	// *chstore.Store satisfies nlquery.Executor (aggregate-only method subset).
	res, err := nlquery.Answer(r.Context(), client, s.ch, cfg, tenant, req.Question)
	if err != nil {
		s.log.Error("nl analytics ask", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable", "could not answer the question: "+err.Error())
		return
	}

	// Audit: store the question, the whitelisted tools/args that ran, and the answer.
	// Never store raw mail content (none is available to this path anyway).
	if tools, mErr := json.Marshal(res.Data); mErr == nil {
		if _, lErr := s.pg.InsertNLQueryLog(r.Context(), tenant, req.Question, tools, res.Answer); lErr != nil {
			s.log.Warn("record nl query log", "tenant_id", tenant, "err", lErr)
		}
	}

	writeJSON(w, http.StatusOK, res)
}

// handleAskHistory returns the tenant's recent NL-analytics questions + answers.
//
// GET /v1/ask/history?limit=N -> {"history": [{id, question, chosen_tools, answer, created_at}]}
func (s *Server) handleAskHistory(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r, "limit", 50, 200)
	entries, err := s.pg.ListNLQueryLog(r.Context(), s.tenant(r), limit)
	if err != nil {
		s.log.Error("nl analytics history", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load ask history")
		return
	}
	if entries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"history": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": entries})
}
