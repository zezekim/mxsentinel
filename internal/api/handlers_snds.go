package api

import (
	"net/http"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

// registerMicrosoftRoutes registers the Microsoft SNDS + JMRP endpoints. The orchestrator
// calls this from server.go (see INTEGRATION_microsoft.md). All reads are tenant-scoped: SNDS
// rows are attributed to the tenant that owns the egress IP, JMRP complaints to the tenant that
// owns the sending domain/IP.
func (s *Server) registerMicrosoftRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/microsoft/snds", s.requireScope(ScopeRead, s.handleMicrosoftSNDS))
	mux.HandleFunc("GET /v1/microsoft/jmrp", s.requireScope(ScopeRead, s.handleMicrosoftJMRP))
}

// sndsTrendPointJSON is one day of a sending IP's SNDS history.
type sndsTrendPointJSON struct {
	Date         string `json:"date"`
	FilterResult string `json:"filter_result"`
	TrapHits     int    `json:"trap_hits"`
	RcptCount    int64  `json:"rcpt_count"`
}

// sndsIPJSON is one sending IP's current Outlook/Hotmail filter state plus a short trend.
type sndsIPJSON struct {
	IP                string               `json:"ip"`
	DataDate          string               `json:"data_date"`
	FilterResult      string               `json:"filter_result"`
	ComplaintBand     string               `json:"complaint_band"`
	TrapHits          int                  `json:"trap_hits"`
	RcptCount         int64                `json:"rcpt_count"`
	DataCount         int64                `json:"data_count"`
	MessageRecipients int64                `json:"message_recipients"`
	SampleHELO        string               `json:"sample_helo"`
	SampleFrom        string               `json:"sample_from"`
	FetchedAt         string               `json:"fetched_at"`
	Trend             []sndsTrendPointJSON `json:"trend"`
}

// handleMicrosoftSNDS returns per-IP Outlook/Hotmail filter state (GREEN/YELLOW/RED), complaint
// band, spam-trap hits, and a short per-IP trend. Trends come from ClickHouse (long-horizon
// history) when it is deployed, falling back to the Postgres retention window otherwise.
//
// GET /v1/microsoft/snds?limit=&days=
func (s *Server) handleMicrosoftSNDS(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	limit := parseIntParam(r, "limit", 100, 1000)
	days := parseIntParam(r, "days", 14, 365)

	latest, err := s.pg.SNDSLatestByIP(r.Context(), tenantID, limit)
	if err != nil {
		s.log.Error("snds latest by ip", "tenant_id", tenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list SNDS data")
		return
	}

	out := make([]sndsIPJSON, 0, len(latest))
	for _, d := range latest {
		row := sndsIPJSON{
			IP:                d.IP,
			DataDate:          d.DataDate.Format("2006-01-02"),
			FilterResult:      d.FilterResult,
			ComplaintBand:     d.ComplaintBand,
			TrapHits:          d.TrapHits,
			RcptCount:         d.RcptCount,
			DataCount:         d.DataCount,
			MessageRecipients: d.MsgRecipients,
			SampleHELO:        d.SampleHELO,
			SampleFrom:        d.SampleFrom,
			FetchedAt:         d.FetchedAt.UTC().Format(time.RFC3339),
			Trend:             s.sndsTrend(r, tenantID, d.IP, days),
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ips": out})
}

// sndsTrend fetches a single IP's daily trend, preferring the ClickHouse long-horizon copy and
// falling back to the Postgres window when ClickHouse is unavailable or errors.
func (s *Server) sndsTrend(r *http.Request, tenantID, ip string, days int) []sndsTrendPointJSON {
	if s.ch != nil {
		pts, err := s.ch.SNDSIPTrend(r.Context(), tenantID, ip, days)
		if err == nil {
			return chTrendJSON(pts)
		}
		s.log.Warn("snds trend from clickhouse; falling back to postgres", "ip", ip, "err", err)
	}
	pts, err := s.pg.SNDSIPTrend(r.Context(), tenantID, ip, days)
	if err != nil {
		s.log.Warn("snds trend from postgres", "ip", ip, "err", err)
		return []sndsTrendPointJSON{}
	}
	out := make([]sndsTrendPointJSON, 0, len(pts))
	for _, p := range pts {
		out = append(out, sndsTrendPointJSON{
			Date:         p.DataDate.Format("2006-01-02"),
			FilterResult: p.FilterResult,
			TrapHits:     p.TrapHits,
			RcptCount:    p.RcptCount,
		})
	}
	return out
}

func chTrendJSON(pts []chstore.SNDSTrendPoint) []sndsTrendPointJSON {
	out := make([]sndsTrendPointJSON, 0, len(pts))
	for _, p := range pts {
		out = append(out, sndsTrendPointJSON{
			Date:         p.DataDate.Format("2006-01-02"),
			FilterResult: p.FilterResult,
			TrapHits:     int(p.TrapHits),
			RcptCount:    int64(p.RcptCount),
		})
	}
	return out
}

// jmrpComplaintJSON is one row of the JMRP complaint feed.
type jmrpComplaintJSON struct {
	SenderDomain   string `json:"sender_domain"`
	SendingIP      string `json:"sending_ip"`
	FeedbackType   string `json:"feedback_type"`
	Provider       string `json:"provider"`
	ComplaintDate  string `json:"complaint_date"`
	ComplaintCount int    `json:"complaint_count"`
	LastSeen       string `json:"last_seen"`
}

// handleMicrosoftJMRP returns the tenant's Junk Mail Reporting Program complaint feed:
// per (sending domain, sending IP, day) complaint counts, most recent first.
//
// GET /v1/microsoft/jmrp?limit=
func (s *Server) handleMicrosoftJMRP(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	limit := parseIntParam(r, "limit", 100, 1000)

	rows, err := s.pg.ListJMRPComplaints(r.Context(), tenantID, limit)
	if err != nil {
		s.log.Error("list jmrp complaints", "tenant_id", tenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list JMRP complaints")
		return
	}

	out := make([]jmrpComplaintJSON, 0, len(rows))
	for _, c := range rows {
		out = append(out, jmrpComplaintJSON{
			SenderDomain:   c.SenderDomain,
			SendingIP:      c.SendingIP,
			FeedbackType:   c.FeedbackType,
			Provider:       c.Provider,
			ComplaintDate:  c.ComplaintDate.Format("2006-01-02"),
			ComplaintCount: c.ComplaintCount,
			LastSeen:       c.LastSeen.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"complaints": out})
}
