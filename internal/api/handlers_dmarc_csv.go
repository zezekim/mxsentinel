package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

// handleDMARCReportsCSV exports the tenant's DMARC reports list as CSV, with the
// same domain/org/date filters as the JSON listing plus ClickHouse-derived
// pass/fail message counts per report.
// GET /v1/dmarc/reports.csv
func (s *Server) handleDMARCReportsCSV(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	q := r.URL.Query()
	since, until := parseSinceUntil(r)

	reports, err := s.pg.ReportsForCSV(r.Context(), tenant, q.Get("domain"), q.Get("org"), since, until)
	if err != nil {
		s.log.Error("reports for csv", "err", err)
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

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dmarc-reports.csv"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"org_name", "report_id", "domain", "date_begin", "date_end", "record_count", "pass_count", "fail_count"})
	for _, rep := range reports {
		st := stats[chstore.DMARCReportStatKey{OrgName: rep.OrgName, ReportID: rep.ReportID}]
		_ = cw.Write([]string{
			rep.OrgName,
			rep.ReportID,
			rep.Domain,
			rep.DateBegin.UTC().Format(time.RFC3339),
			rep.DateEnd.UTC().Format(time.RFC3339),
			strconv.Itoa(rep.RecordCount),
			strconv.FormatUint(st.Pass, 10),
			strconv.FormatUint(st.Fail, 10),
		})
	}
}

// handleDMARCRecordsCSV exports one report's per-source records as CSV.
// GET /v1/dmarc/reports/{id}/records.csv
func (s *Server) handleDMARCRecordsCSV(w http.ResponseWriter, r *http.Request) {
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

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dmarc-records.csv"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"source_ip", "count", "disposition", "dkim_aligned", "spf_aligned", "header_from", "envelope_from"})
	for _, rec := range recs {
		_ = cw.Write([]string{
			rec.SourceIP.String(),
			strconv.FormatUint(uint64(rec.Count), 10),
			rec.Disposition,
			strconv.Itoa(int(rec.DKIMAligned)),
			strconv.Itoa(int(rec.SPFAligned)),
			rec.HeaderFrom,
			rec.EnvelopeFrom,
		})
	}
}
