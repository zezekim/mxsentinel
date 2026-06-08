package api

import (
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/anomaly"
)

type anomalyJSON struct {
	SenderDomain      string  `json:"sender_domain"`
	ObservedHourCount int64   `json:"observed_hour_count"`
	Baseline          float64 `json:"baseline"`
	Factor            float64 `json:"factor"`
	DetectedAt        string  `json:"detected_at"`
}

type moverJSON struct {
	SenderDomain string  `json:"sender_domain"`
	Current      int64   `json:"current"`
	Baseline     float64 `json:"baseline"`
	Ratio        float64 `json:"ratio"`
}

// handleAnomalyRecent returns a tenant's recent send-volume anomalies plus the current
// "top movers" (domains whose observed hourly volume sits furthest above their baseline).
// Powers the dashboard's Velocity page. Read scope. Query params:
//
//	limit    (default 50, max 500) — number of recent anomalies
//	movers   (default 10, max 100) — number of top movers
//	lookback_hours (default 24, max 720) — window for the top-movers ranking
func (s *Server) handleAnomalyRecent(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	store := anomaly.NewStore(s.pg.Pool)

	limit := parseIntParam(r, "limit", 50, 500)
	moverN := parseIntParam(r, "movers", 10, 100)
	lookbackHours := parseIntParam(r, "lookback_hours", 24, 720)

	anomalies, err := store.RecentAnomalies(r.Context(), tenant, limit)
	if err != nil {
		s.log.Error("recent anomalies", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list anomalies")
		return
	}
	movers, err := store.TopMovers(r.Context(), tenant, time.Duration(lookbackHours)*time.Hour, moverN)
	if err != nil {
		s.log.Error("top movers", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to compute top movers")
		return
	}

	aitems := make([]anomalyJSON, 0, len(anomalies))
	for _, a := range anomalies {
		aitems = append(aitems, anomalyJSON{
			SenderDomain:      a.SenderDomain,
			ObservedHourCount: a.ObservedHourCount,
			Baseline:          a.Baseline,
			Factor:            a.Factor,
			DetectedAt:        a.DetectedAt.UTC().Format(time.RFC3339),
		})
	}
	mitems := make([]moverJSON, 0, len(movers))
	for _, m := range movers {
		mitems = append(mitems, moverJSON{
			SenderDomain: m.SenderDomain,
			Current:      m.Current,
			Baseline:     m.Baseline,
			Ratio:        m.Ratio,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"anomalies":  aitems,
		"top_movers": mitems,
	})
}
