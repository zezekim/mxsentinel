package api

import (
	"net/http"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

var heatmapWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// parseWindowParam parses a ?window=24h|7d|30d query param and returns a since
// time and the canonical window string. Defaults to 7d if the param is absent
// or unrecognised.
func parseWindowParam(r *http.Request) (since time.Time, window string) {
	window = r.URL.Query().Get("window")
	dur, ok := heatmapWindows[window]
	if !ok {
		window = "7d"
		dur = heatmapWindows["7d"]
	}
	since = time.Now().Add(-dur)
	return
}

// heatmapRowJSON is the JSON shape for a single ProviderHeatmapRow.
type heatmapRowJSON struct {
	Provider        string  `json:"provider"`
	RecipientDomain string  `json:"recipient_domain"`
	Delivered       uint64  `json:"delivered"`
	Deferred        uint64  `json:"deferred"`
	Bounced         uint64  `json:"bounced"`
	Rejected        uint64  `json:"rejected"`
	Total           uint64  `json:"total"`
	AcceptanceRate  float64 `json:"acceptance_rate"`
}

func toHeatmapRowJSON(rows []chstore.ProviderHeatmapRow) []heatmapRowJSON {
	out := make([]heatmapRowJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, heatmapRowJSON{
			Provider:        r.Provider,
			RecipientDomain: r.RecipientDomain,
			Delivered:       r.Delivered,
			Deferred:        r.Deferred,
			Bounced:         r.Bounced,
			Rejected:        r.Rejected,
			Total:           r.Total,
			AcceptanceRate:  r.AcceptanceRate,
		})
	}
	return out
}

// handleHeatmap handles GET /v1/heatmap
//
// Query params:
//   - window=24h|7d|30d  (default 7d)
//   - view=providers|recipients  (default providers)
//
// Returns provider heatmap or top recipient domains depending on view.
func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	since, window := parseWindowParam(r)
	until := time.Time{} // unbounded upper end

	view := r.URL.Query().Get("view")
	if view == "" {
		view = "providers"
	}
	if view != "providers" && view != "recipients" {
		writeError(w, http.StatusBadRequest, "bad_request", "view must be providers or recipients")
		return
	}

	tenant := s.tenant(r)
	ctx := r.Context()

	var rows []chstore.ProviderHeatmapRow
	var err error

	switch view {
	case "recipients":
		rows, err = s.ch.TopRecipientDomains(ctx, tenant, since, until, 100)
		if err != nil {
			s.log.Error("heatmap top recipient domains", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to compute recipient domain heatmap")
			return
		}
	default:
		rows, err = s.ch.ProviderHeatmap(ctx, tenant, since, until)
		if err != nil {
			s.log.Error("provider heatmap", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to compute provider heatmap")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"view":   view,
		"window": window,
		"rows":   toHeatmapRowJSON(rows),
	})
}

// perUserStatJSON is the JSON shape for a single PerUserStat.
type perUserStatJSON struct {
	SASLUsername     string  `json:"sasl_username"`
	Sent             uint64  `json:"sent"`
	Delivered        uint64  `json:"delivered"`
	Bounced          uint64  `json:"bounced"`
	Rejected         uint64  `json:"rejected"`
	UniqueRecipients uint64  `json:"unique_recipients"`
	BounceRate       float64 `json:"bounce_rate"`
}

// handlePerUserStats handles GET /v1/analytics/per-user
//
// Query params:
//   - window=24h|7d|30d  (default 7d)
//
// Returns per-SASL-user send stats for the SMTP abuse dashboard.
func (s *Server) handlePerUserStats(w http.ResponseWriter, r *http.Request) {
	since, window := parseWindowParam(r)
	until := time.Time{} // unbounded upper end

	stats, err := s.ch.PerUserStats(r.Context(), s.tenant(r), since, until)
	if err != nil {
		s.log.Error("per-user stats", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to compute per-user stats")
		return
	}

	users := make([]perUserStatJSON, 0, len(stats))
	for _, u := range stats {
		users = append(users, perUserStatJSON{
			SASLUsername:     u.SASLUsername,
			Sent:             u.Sent,
			Delivered:        u.Delivered,
			Bounced:          u.Bounced,
			Rejected:         u.Rejected,
			UniqueRecipients: u.UniqueRecipients,
			BounceRate:       u.BounceRate,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"window": window,
		"users":  users,
	})
}
