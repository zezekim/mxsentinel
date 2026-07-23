package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// registerTLSReportingRoutes wires the TLS Reporting (TLS-RPT + MTA-STS) endpoints. The
// orchestrator calls this from server.go's route table.
//
//	GET /v1/tls-reporting/mta-sts   -> latest MTA-STS policy state per monitored domain
//	GET /v1/tls-reporting/reports   -> archived TLS-RPT reports + aggregate TLS success/failure
//	GET /v1/domains/{id}/mta-sts    -> latest MTA-STS snapshot for one domain
func (s *Server) registerTLSReportingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tls-reporting/mta-sts", s.requireScope(ScopeRead, s.handleMTASTSList))
	mux.HandleFunc("GET /v1/tls-reporting/reports", s.requireScope(ScopeRead, s.handleTLSRPTReports))
	mux.HandleFunc("GET /v1/domains/{id}/mta-sts", s.requireScope(ScopeRead, s.handleDomainMTASTS))
}

type mtastsSnapshotJSON struct {
	ID         string          `json:"id"`
	DomainID   string          `json:"domain_id"`
	Domain     string          `json:"domain"`
	Mode       string          `json:"mode"`
	MaxAge     int             `json:"max_age"`
	MXHosts    []string        `json:"mx_hosts"`
	Checksum   string          `json:"checksum"`
	CertExpiry string          `json:"cert_expiry,omitempty"`
	Healthy    bool            `json:"healthy"`
	Findings   json.RawMessage `json:"findings,omitempty"`
	CapturedAt string          `json:"captured_at"`
}

// handleMTASTSList returns the latest MTA-STS policy state for each of the tenant's domains.
func (s *Server) handleMTASTSList(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	items, err := s.pg.ListMTASTSSnapshots(r.Context(), tenant)
	if err != nil {
		s.log.Error("list mta-sts snapshots", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list MTA-STS state")
		return
	}
	out := make([]mtastsSnapshotJSON, 0, len(items))
	for i := range items {
		out = append(out, mtastsItemToJSON(
			items[i].ID, items[i].DomainID, items[i].Domain, items[i].Mode, items[i].MaxAge,
			items[i].MXHosts, items[i].Checksum, items[i].CertExpiry, items[i].IsHealthy,
			items[i].Findings, items[i].CapturedAt))
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

// handleDomainMTASTS returns the latest MTA-STS snapshot for a single domain.
func (s *Server) handleDomainMTASTS(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	domainID := r.PathValue("id")

	it, found, err := s.pg.GetMTASTSSnapshot(r.Context(), tenant, domainID)
	if err != nil {
		s.log.Error("get mta-sts snapshot", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read MTA-STS state")
		return
	}
	if !found {
		// Domain may exist but not yet be inspected; distinguish would need an extra query.
		writeError(w, http.StatusNotFound, "not_found", "no MTA-STS snapshot for this domain")
		return
	}
	writeJSON(w, http.StatusOK, mtastsItemToJSON(
		it.ID, it.DomainID, it.Domain, it.Mode, it.MaxAge, it.MXHosts, it.Checksum,
		it.CertExpiry, it.IsHealthy, it.Findings, it.CapturedAt))
}

type tlsrptReportJSON struct {
	ID           string `json:"id"`
	OrgName      string `json:"org_name"`
	ReportID     string `json:"report_id"`
	Domain       string `json:"domain"`
	DateBegin    string `json:"date_begin"`
	DateEnd      string `json:"date_end"`
	PolicyCount  int    `json:"policy_count"`
	SuccessCount uint64 `json:"success_count"`
	FailureCount uint64 `json:"failure_count"`
}

type tlsrptSummaryJSON struct {
	Success     uint64            `json:"success"`
	Failure     uint64            `json:"failure"`
	SuccessRate float64           `json:"success_rate"`
	ByType      []tlsFailTypeJSON `json:"by_type"`
}

type tlsFailTypeJSON struct {
	ResultType string `json:"result_type"`
	Failures   uint64 `json:"failures"`
}

// handleTLSRPTReports lists archived TLS-RPT reports (Postgres pointers) plus an aggregate
// TLS success/failure summary (ClickHouse). The summary is best-effort: a ClickHouse hiccup
// still returns the report list.
func (s *Server) handleTLSRPTReports(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	domain := r.URL.Query().Get("domain")
	limit := parseIntParam(r, "limit", 50, 500)

	reports, err := s.pg.ListTLSRPTReports(r.Context(), tenant, domain, limit)
	if err != nil {
		s.log.Error("list tlsrpt reports", "err", err, "tenant_id", tenant)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list reports")
		return
	}
	items := make([]tlsrptReportJSON, 0, len(reports))
	for _, rep := range reports {
		items = append(items, tlsrptReportJSON{
			ID: rep.ID, OrgName: rep.OrgName, ReportID: rep.ReportID, Domain: rep.Domain,
			DateBegin:    fmtTime(rep.DateBegin),
			DateEnd:      fmtTime(rep.DateEnd),
			PolicyCount:  rep.PolicyCount,
			SuccessCount: rep.SuccessCount,
			FailureCount: rep.FailureCount,
		})
	}

	summary := tlsrptSummaryJSON{ByType: []tlsFailTypeJSON{}}
	if sum, serr := s.ch.TLSRPTSummaryFor(r.Context(), tenant, domain, time.Time{}, time.Time{}); serr != nil {
		s.log.Warn("tlsrpt summary failed", "err", serr)
	} else {
		summary.Success, summary.Failure = sum.Success, sum.Failure
		if total := sum.Success + sum.Failure; total > 0 {
			summary.SuccessRate = float64(sum.Success) / float64(total)
		}
	}
	if byType, terr := s.ch.TLSRPTFailuresByType(r.Context(), tenant, domain, time.Time{}, time.Time{}); terr != nil {
		s.log.Warn("tlsrpt failures by type failed", "err", terr)
	} else {
		for _, t := range byType {
			summary.ByType = append(summary.ByType, tlsFailTypeJSON{ResultType: t.ResultType, Failures: t.Failures})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"reports": items, "summary": summary})
}

func mtastsItemToJSON(id, domainID, domain, mode string, maxAge int, mx []string, checksum string, certExpiry *time.Time, healthy bool, findings json.RawMessage, capturedAt time.Time) mtastsSnapshotJSON {
	j := mtastsSnapshotJSON{
		ID: id, DomainID: domainID, Domain: domain, Mode: mode, MaxAge: maxAge,
		MXHosts: mx, Checksum: checksum, Healthy: healthy, Findings: findings,
		CapturedAt: fmtTime(capturedAt),
	}
	if j.MXHosts == nil {
		j.MXHosts = []string{}
	}
	if certExpiry != nil && !certExpiry.IsZero() {
		j.CertExpiry = certExpiry.UTC().Format(time.RFC3339)
	}
	return j
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
