package api

import (
	"net/http"
	"time"
)

type dmarcTrendPointJSON struct {
	Date     string  `json:"date"`
	Total    uint64  `json:"total"`
	Pass     uint64  `json:"pass"`
	Fail     uint64  `json:"fail"`
	PassRate float64 `json:"pass_rate"`
}

// handleDMARCTrend returns the tenant's DMARC pass-rate time-series as daily buckets,
// oldest first, over dmarc_records. Filterable by domain and an RFC3339 since/until
// range (empty = unbounded). points is [] (never null) when there is no data.
// GET /v1/dmarc/trend
func (s *Server) handleDMARCTrend(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	q := r.URL.Query()
	domain := q.Get("domain")

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

	buckets, err := s.ch.DMARCTrend(r.Context(), tenant, domain, since, until)
	if err != nil {
		s.log.Error("dmarc trend", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load trend")
		return
	}

	points := make([]dmarcTrendPointJSON, 0, len(buckets))
	for _, b := range buckets {
		p := dmarcTrendPointJSON{
			Date:  b.Date.Format("2006-01-02"),
			Total: b.Total,
			Pass:  b.Pass,
			Fail:  b.Fail,
		}
		if b.Total > 0 {
			p.PassRate = float64(b.Pass) / float64(b.Total)
		}
		points = append(points, p)
	}

	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}
