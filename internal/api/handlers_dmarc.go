package api

import (
	"net/http"
	"sort"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

type dmarcReportJSON struct {
	ID          string `json:"id"`
	OrgName     string `json:"org_name"`
	ReportID    string `json:"report_id"`
	Domain      string `json:"domain"`
	DateBegin   string `json:"date_begin"`
	DateEnd     string `json:"date_end"`
	RecordCount int    `json:"record_count"`
	PassCount   uint64 `json:"pass_count"`
	FailCount   uint64 `json:"fail_count"`
}

type dmarcAlignmentJSON struct {
	Total        uint64  `json:"total"`
	DKIMAligned  uint64  `json:"dkim_aligned"`
	SPFAligned   uint64  `json:"spf_aligned"`
	DKIMPassRate float64 `json:"dkim_pass_rate"`
	SPFPassRate  float64 `json:"spf_pass_rate"`
}

type dmarcRecordJSON struct {
	SourceIP     string `json:"source_ip"`
	Count        uint32 `json:"count"`
	Disposition  string `json:"disposition"`
	DKIMAligned  bool   `json:"dkim_aligned"`
	SPFAligned   bool   `json:"spf_aligned"`
	HeaderFrom   string `json:"header_from"`
	EnvelopeFrom string `json:"envelope_from"`
}

// parseSinceUntil reads the optional "since"/"until" RFC3339 query params. A missing or
// unparseable value yields the zero time (unbounded), matching the message explorer.
func parseSinceUntil(r *http.Request) (since, until time.Time) {
	q := r.URL.Query()
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
	return since, until
}

// matchesDMARCStatus classifies a report by its pass/fail message counts and reports
// whether it satisfies the requested status filter. An empty filter matches everything.
//
//	pass  -> all messages passed (fail == 0, pass > 0)
//	fail  -> all messages failed (pass == 0, fail > 0)
//	mixed -> some passed and some failed
func matchesDMARCStatus(status string, pass, fail uint64) bool {
	switch status {
	case "pass":
		return fail == 0 && pass > 0
	case "fail":
		return pass == 0 && fail > 0
	case "mixed":
		return pass > 0 && fail > 0
	default:
		return true
	}
}

// sortDMARCReports orders the enriched reports in place by a ClickHouse-derived key
// (pass, fail, or total messages). order is "asc" or "desc".
func sortDMARCReports(items []dmarcReportJSON, sortKey, order string) {
	keyOf := func(it dmarcReportJSON) uint64 {
		switch sortKey {
		case "pass":
			return it.PassCount
		case "fail":
			return it.FailCount
		default: // total
			return it.PassCount + it.FailCount
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := keyOf(items[i]), keyOf(items[j])
		if order == "asc" {
			return a < b
		}
		return a > b
	})
}

// handleDMARCReports lists archived DMARC reports (Postgres pointers) plus an aggregate
// SPF/DKIM alignment summary and per-report pass/fail message counts (ClickHouse).
// ClickHouse enrichment is best-effort: a hiccup still returns the report list.
func (s *Server) handleDMARCReports(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	q := r.URL.Query()
	domain := q.Get("domain")
	limit := parseIntParam(r, "limit", 50, 500)

	since, until := parseSinceUntil(r)

	status := q.Get("status") // pass|fail|mixed (filtered in Go after CH enrichment)
	order := q.Get("order")
	if order != "asc" {
		order = "desc"
	}
	sortKey := q.Get("sort") // date|pass|fail|total (default date)
	if sortKey != "pass" && sortKey != "fail" && sortKey != "total" {
		sortKey = "date"
	}

	reports, err := s.pg.ListDMARCReports(r.Context(), tenant, pgstore.DMARCReportFilter{
		Domain: domain,
		Org:    q.Get("org"),
		Since:  since,
		Until:  until,
		Order:  order,
		Limit:  limit,
	})
	if err != nil {
		s.log.Error("list dmarc reports", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list reports")
		return
	}

	keys := make([]chstore.DMARCReportStatKey, 0, len(reports))
	for _, rep := range reports {
		keys = append(keys, chstore.DMARCReportStatKey{OrgName: rep.OrgName, ReportID: rep.ReportID})
	}
	stats, serr := s.ch.DMARCReportStats(r.Context(), tenant, keys)
	if serr != nil {
		s.log.Warn("dmarc report stats failed", "err", serr)
	}

	items := make([]dmarcReportJSON, 0, len(reports))
	for _, rep := range reports {
		st := stats[chstore.DMARCReportStatKey{OrgName: rep.OrgName, ReportID: rep.ReportID}]
		if !matchesDMARCStatus(status, st.Pass, st.Fail) {
			continue
		}
		items = append(items, dmarcReportJSON{
			ID: rep.ID, OrgName: rep.OrgName, ReportID: rep.ReportID, Domain: rep.Domain,
			DateBegin:   rep.DateBegin.UTC().Format(time.RFC3339),
			DateEnd:     rep.DateEnd.UTC().Format(time.RFC3339),
			RecordCount: rep.RecordCount,
			PassCount:   st.Pass,
			FailCount:   st.Fail,
		})
	}

	// Sort pass/fail/total in Go (counts come from ClickHouse, not the base Postgres
	// list). For sort=date the rows already arrive ordered from Postgres.
	if sortKey != "date" {
		sortDMARCReports(items, sortKey, order)
	}

	align := dmarcAlignmentJSON{}
	if a, aerr := s.ch.DMARCAlignmentSummary(r.Context(), tenant, domain, since, until); aerr != nil {
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

type dmarcAnalyticsStatsJSON struct {
	Domains      uint64  `json:"domains"`
	Reports      int     `json:"reports"`
	Messages     uint64  `json:"messages"`
	PassMessages uint64  `json:"pass_messages"`
	FailMessages uint64  `json:"fail_messages"`
	PassRate     float64 `json:"pass_rate"`
}

type dmarcFailingDomainJSON struct {
	Domain   string  `json:"domain"`
	Fails    uint64  `json:"fails"`
	Total    uint64  `json:"total"`
	FailRate float64 `json:"fail_rate"`
}

// handleDMARCAnalytics returns tenant-wide DMARC analytics: message/domain totals with
// the overall pass rate (ClickHouse), the archived report count (Postgres), and the top
// failing domains by DMARC-failing message volume.
// GET /v1/dmarc/analytics
func (s *Server) handleDMARCAnalytics(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	since, until := parseSinceUntil(r)

	totals, err := s.ch.DMARCAnalyticsTotals(r.Context(), tenant, since, until)
	if err != nil {
		s.log.Error("dmarc analytics totals", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load analytics")
		return
	}

	reports, err := s.pg.CountDMARCReports(r.Context(), tenant, since, until)
	if err != nil {
		s.log.Error("count dmarc reports", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load analytics")
		return
	}

	failing, err := s.ch.DMARCTopFailingDomains(r.Context(), tenant, since, until, 10)
	if err != nil {
		s.log.Error("dmarc top failing domains", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load analytics")
		return
	}

	stats := dmarcAnalyticsStatsJSON{
		Domains:      totals.Domains,
		Reports:      reports,
		Messages:     totals.Messages,
		PassMessages: totals.Pass,
		FailMessages: totals.Fail,
	}
	if totals.Messages > 0 {
		stats.PassRate = float64(totals.Pass) / float64(totals.Messages)
	}

	top := make([]dmarcFailingDomainJSON, 0, len(failing))
	for _, d := range failing {
		item := dmarcFailingDomainJSON{Domain: d.Domain, Fails: d.Fails, Total: d.Total}
		if d.Total > 0 {
			item.FailRate = float64(d.Fails) / float64(d.Total)
		}
		top = append(top, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "top_failing_domains": top})
}

// handleDMARCReportRecords returns the per-source rows of one report: the IPs that sent
// as the domain, message counts, dispositions, and DKIM/SPF alignment.
// GET /v1/dmarc/reports/{id}/records
func (s *Server) handleDMARCReportRecords(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	rep, found, err := s.pg.GetDMARCReport(r.Context(), tenant, id)
	if err != nil {
		s.log.Error("get dmarc report", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load report")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "report not found")
		return
	}

	recs, err := s.ch.ListDMARCRecords(r.Context(), tenant, rep.OrgName, rep.ReportID)
	if err != nil {
		s.log.Error("list dmarc records", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load records")
		return
	}

	items := make([]dmarcRecordJSON, 0, len(recs))
	for _, rec := range recs {
		items = append(items, dmarcRecordJSON{
			SourceIP:     rec.SourceIP.String(),
			Count:        rec.Count,
			Disposition:  rec.Disposition,
			DKIMAligned:  rec.DKIMAligned == 1,
			SPFAligned:   rec.SPFAligned == 1,
			HeaderFrom:   rec.HeaderFrom,
			EnvelopeFrom: rec.EnvelopeFrom,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"records": items})
}
