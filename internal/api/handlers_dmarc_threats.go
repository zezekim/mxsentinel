package api

import (
	"net/http"
	"time"
)

type dmarcThreatJSON struct {
	SourceIP       string `json:"source_ip"`
	PTR            string `json:"ptr"`
	Domain         string `json:"domain"`
	FailCount      uint64 `json:"fail_count"`
	LastSeen       string `json:"last_seen"`
	TopDisposition string `json:"top_disposition"`
}

// handleDMARCThreats returns the tenant's worst spoofing sources: (source_ip, domain)
// pairs whose messages failed both DKIM and SPF alignment, ranked by failing volume.
// Each source IP is enriched with its reverse-DNS name (best-effort, "" on miss).
// GET /v1/dmarc/threats?since&until&domain&limit
func (s *Server) handleDMARCThreats(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	q := r.URL.Query()
	domain := q.Get("domain")
	since, until := parseSinceUntil(r)
	limit := parseIntParam(r, "limit", 50, 200)

	threats, err := s.ch.DMARCThreats(r.Context(), tenant, domain, since, until, limit)
	if err != nil {
		s.log.Error("dmarc threats", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load threats")
		return
	}

	items := make([]dmarcThreatJSON, 0, len(threats))
	for _, t := range threats {
		ip := t.SourceIP.String()
		items = append(items, dmarcThreatJSON{
			SourceIP:       ip,
			PTR:            s.lookupPTR(r.Context(), ip),
			Domain:         t.Domain,
			FailCount:      t.FailCount,
			LastSeen:       t.LastSeen.UTC().Format(time.RFC3339),
			TopDisposition: t.TopDisposition,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"threats": items})
}
