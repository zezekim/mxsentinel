package api

import (
	"net/http"
	"time"
)

type dmarcReportJSON struct {
	ID          string `json:"id"`
	OrgName     string `json:"org_name"`
	ReportID    string `json:"report_id"`
	Domain      string `json:"domain"`
	DateBegin   string `json:"date_begin"`
	DateEnd     string `json:"date_end"`
	RecordCount int    `json:"record_count"`
}

type dmarcAlignmentJSON struct {
	Total        uint64  `json:"total"`
	DKIMAligned  uint64  `json:"dkim_aligned"`
	SPFAligned   uint64  `json:"spf_aligned"`
	DKIMPassRate float64 `json:"dkim_pass_rate"`
	SPFPassRate  float64 `json:"spf_pass_rate"`
}

// handleDMARCReports lists archived DMARC reports (Postgres pointers) plus an aggregate
// SPF/DKIM alignment summary (ClickHouse). Alignment is best-effort: a ClickHouse hiccup
// still returns the report list.
func (s *Server) handleDMARCReports(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	domain := r.URL.Query().Get("domain")
	limit := parseIntParam(r, "limit", 50, 500)

	reports, err := s.pg.ListDMARCReports(r.Context(), tenant, domain, limit)
	if err != nil {
		s.log.Error("list dmarc reports", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list reports")
		return
	}

	items := make([]dmarcReportJSON, 0, len(reports))
	for _, rep := range reports {
		items = append(items, dmarcReportJSON{
			ID: rep.ID, OrgName: rep.OrgName, ReportID: rep.ReportID, Domain: rep.Domain,
			DateBegin:   rep.DateBegin.UTC().Format(time.RFC3339),
			DateEnd:     rep.DateEnd.UTC().Format(time.RFC3339),
			RecordCount: rep.RecordCount,
		})
	}

	align := dmarcAlignmentJSON{}
	if a, aerr := s.ch.DMARCAlignmentSummary(r.Context(), tenant, domain, time.Time{}, time.Time{}); aerr != nil {
		s.log.Warn("dmarc alignment summary failed", "err", aerr)
	} else {
		align.Total, align.DKIMAligned, align.SPFAligned = a.Total, a.DKIMAligned, a.SPFAligned
		if a.Total > 0 {
			align.DKIMPassRate = float64(a.DKIMAligned) / float64(a.Total)
			align.SPFPassRate = float64(a.SPFAligned) / float64(a.Total)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"reports": items, "alignment": align})
}
