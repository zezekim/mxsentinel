package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/internal/report"
)

// GET /v1/reports/domain?domain=&since=&until=&format=
//
// Returns an ad-hoc per-domain deliverability report (outcome counts + rates, per-provider
// breakdown, health score, seed-list placement) plus a ready-to-paste text block. since/until
// are RFC3339 or a date (YYYY-MM-DD); default is the last 30 days. format=text returns the
// plain-text block only (for curl / copy-paste); default is JSON {report, text}.
//
// Distinct from /v1/reports (scheduled report management) — this is an on-demand snapshot.
func (s *Server) handleDomainReport(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if domain == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "domain is required")
		return
	}

	now := time.Now().UTC()
	until := parseReportTime(r.URL.Query().Get("until"), now)
	since := parseReportTime(r.URL.Query().Get("since"), until.Add(-30*24*time.Hour))
	if !since.Before(until) {
		writeError(w, http.StatusBadRequest, "bad_request", "since must be before until")
		return
	}

	rep, err := report.Build(r.Context(), s.ch, s.pg, tenantID, domain, since, until)
	if err != nil {
		s.log.Error("build domain report", "domain", domain, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to build report")
		return
	}

	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(rep.Text()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "text": rep.Text()})
}

// parseReportTime accepts RFC3339 or a bare YYYY-MM-DD date; returns def on empty/invalid.
func parseReportTime(v string, def time.Time) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC()
	}
	return def
}
