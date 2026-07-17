package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

type dmarcSourceJSON struct {
	SourceIP string  `json:"source_ip"`
	PTR      string  `json:"ptr"`
	Total    uint64  `json:"total"`
	Pass     uint64  `json:"pass"`
	Fail     uint64  `json:"fail"`
	PassRate float64 `json:"pass_rate"`
}

// handleDMARCSources returns the tenant's top sending source IPs by DMARC-reported
// message volume, each enriched with a best-effort reverse-DNS (PTR) name.
// GET /v1/dmarc/sources?since&until&domain&limit
func (s *Server) handleDMARCSources(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	q := r.URL.Query()
	domain := q.Get("domain")
	limit := parseIntParam(r, "limit", 50, 200)

	var since, until time.Time
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			until = t
		}
	}

	rows, err := s.ch.DMARCTopSources(r.Context(), tenant, domain, since, until, limit)
	if err != nil {
		s.log.Error("dmarc top sources", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load sources")
		return
	}

	items := make([]dmarcSourceJSON, len(rows))
	for i, src := range rows {
		item := dmarcSourceJSON{
			SourceIP: src.SourceIP.String(),
			Total:    src.Total,
			Pass:     src.Pass,
			Fail:     src.Fail,
		}
		if src.Total > 0 {
			item.PassRate = float64(src.Pass) / float64(src.Total)
		}
		items[i] = item
	}

	// Enrich with reverse DNS. Resolve concurrently with a bounded worker pool and a
	// short per-lookup timeout; PTR failures simply leave ptr empty.
	s.enrichPTR(r.Context(), items)

	writeJSON(w, http.StatusOK, map[string]any{"sources": items})
}

// enrichPTR fills the PTR field of each source via reverse DNS, best-effort. It bounds
// latency with a small worker pool and a 1s per-lookup timeout; any error or empty result
// yields "".
func (s *Server) enrichPTR(ctx context.Context, items []dmarcSourceJSON) {
	if len(items) == 0 {
		return
	}

	const workers = 8
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				items[i].PTR = s.lookupPTR(ctx, items[i].SourceIP)
			}
		}()
	}
	for i := range items {
		idx <- i
	}
	close(idx)
	wg.Wait()
}

// lookupPTR returns the first reverse-DNS name for ip with the trailing dot stripped, or
// "" on error / no record.
func (s *Server) lookupPTR(ctx context.Context, ip string) string {
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	names, err := s.resolver.PTR(c, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}
