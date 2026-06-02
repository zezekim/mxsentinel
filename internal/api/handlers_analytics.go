package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/zezekim/mxsentinel/internal/correlate"
)

type providerStatsJSON struct {
	Provider      string  `json:"provider"`
	Delivered     uint64  `json:"delivered"`
	Deferred      uint64  `json:"deferred"`
	Bounced       uint64  `json:"bounced"`
	Rejected      uint64  `json:"rejected"`
	Total         uint64  `json:"total"`
	DeliveredRate float64 `json:"delivered_rate"`
}

// handleDeliverability returns per-provider outcome counts + delivered rate.
func (s *Server) handleDeliverability(w http.ResponseWriter, r *http.Request) {
	since := parseTimeParam(r, "since")
	until := parseTimeParam(r, "until")
	stats, err := s.ch.DeliverabilityByProvider(r.Context(), s.tenant(r), since, until)
	if err != nil {
		s.log.Error("deliverability", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to compute deliverability")
		return
	}
	out := make([]providerStatsJSON, 0, len(stats))
	for _, p := range stats {
		rate := 0.0
		if p.Total > 0 {
			rate = float64(p.Delivered) / float64(p.Total)
		}
		out = append(out, providerStatsJSON{
			Provider: p.Provider, Delivered: p.Delivered, Deferred: p.Deferred,
			Bounced: p.Bounced, Rejected: p.Rejected, Total: p.Total, DeliveredRate: rate,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

type reasonCountJSON struct {
	Reason string `json:"reason"`
	Count  uint64 `json:"count"`
}

type rejectionGroupJSON struct {
	SMTPCode       uint16 `json:"smtp_code"`
	EnhancedStatus string `json:"enhanced_status"`
	BounceClass    string `json:"bounce_class"`
	Provider       string `json:"provider"`
	Reason         string `json:"reason"`
	Sample         string `json:"sample"`
	Count          uint64 `json:"count"`
}

// handleRejections returns rejection groups classified into reasons (via the correlation
// engine's provider-aware classifier) plus a reason breakdown.
func (s *Server) handleRejections(w http.ResponseWriter, r *http.Request) {
	since := parseTimeParam(r, "since")
	until := parseTimeParam(r, "until")
	limit := parseIntParam(r, "limit", 50, 500)

	groups, err := s.ch.RejectionGroups(r.Context(), s.tenant(r), since, until, limit)
	if err != nil {
		s.log.Error("rejection groups", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to compute rejections")
		return
	}

	byReason := map[string]uint64{}
	items := make([]rejectionGroupJSON, 0, len(groups))
	for _, g := range groups {
		rej := correlate.ClassifyRejection(int(g.SMTPCode), g.EnhancedStatus, g.Sample, g.Provider)
		reason := string(rej.Category)
		byReason[reason] += g.Count
		items = append(items, rejectionGroupJSON{
			SMTPCode: g.SMTPCode, EnhancedStatus: g.EnhancedStatus, BounceClass: g.BounceClass,
			Provider: g.Provider, Reason: reason, Sample: truncate(g.Sample, 200), Count: g.Count,
		})
	}

	reasons := make([]reasonCountJSON, 0, len(byReason))
	for reason, count := range byReason {
		reasons = append(reasons, reasonCountJSON{Reason: reason, Count: count})
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].Count > reasons[j].Count })

	writeJSON(w, http.StatusOK, map[string]any{"reasons": reasons, "groups": items})
}

func parseTimeParam(r *http.Request, name string) time.Time {
	if v := r.URL.Query().Get(name); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
