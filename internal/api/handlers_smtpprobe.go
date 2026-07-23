package api

import (
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/smtpprobe"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// registerSMTPProbeRoutes wires the synthetic-SMTP-probe endpoints. Both are relay-wide
// infrastructure reads (like /v1/rbl/status), served behind ScopeRead.
//
//	GET /v1/smtp-probes          -> current status of every configured endpoint
//	GET /v1/smtp-probes/history  -> per-probe latency/uptime history (+ rollup)
func (s *Server) registerSMTPProbeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/smtp-probes", s.requireScope(ScopeRead, s.handleSMTPProbes))
	mux.HandleFunc("GET /v1/smtp-probes/history", s.requireScope(ScopeRead, s.handleSMTPProbeHistory))
}

// --- response shapes --------------------------------------------------------

type smtpProbeTLSView struct {
	Negotiated      bool       `json:"negotiated"`
	Version         string     `json:"version,omitempty"`
	Cipher          string     `json:"cipher,omitempty"`
	ChainValid      bool       `json:"chain_valid"`
	CertSubject     string     `json:"cert_subject,omitempty"`
	CertIssuer      string     `json:"cert_issuer,omitempty"`
	CertNotAfter    *time.Time `json:"cert_not_after,omitempty"`
	DaysUntilExpiry *int       `json:"days_until_expiry,omitempty"`
	Expiring        bool       `json:"expiring"`
}

type smtpProbeEndpointView struct {
	Endpoint        smtpprobe.Endpoint `json:"endpoint"`
	Probed          bool               `json:"probed"` // has at least one recorded result
	OK              bool               `json:"ok"`
	Stage           string             `json:"stage,omitempty"`
	Error           string             `json:"error,omitempty"`
	LatencyMS       int64              `json:"latency_ms"`
	Banner          string             `json:"banner,omitempty"`
	STARTTLSOffered bool               `json:"starttls_offered"`
	AuthAdvertised  bool               `json:"auth_advertised"`
	AuthMechs       []string           `json:"auth_mechs,omitempty"`
	TLS             *smtpProbeTLSView  `json:"tls,omitempty"`
	Greylisting     bool               `json:"greylisting"`
	ProbedAt        *time.Time         `json:"probed_at,omitempty"`
}

type smtpProbeSummary struct {
	TotalEndpoints int `json:"total_endpoints"`
	Healthy        int `json:"healthy"`
	Failing        int `json:"failing"`
	Unprobed       int `json:"unprobed"`
	CertWarnings   int `json:"cert_warnings"`
}

type smtpProbeStatus struct {
	ProbedAt  *time.Time              `json:"probed_at"`
	Summary   smtpProbeSummary        `json:"summary"`
	Endpoints []smtpProbeEndpointView `json:"endpoints"`
}

// handleSMTPProbes returns the current status of every configured probe endpoint. The
// endpoint universe comes from the same env vars cmd/probed uses (MXS_PROBE_* / RELAY_*),
// falling back to the smtp_probe_targets table, so an endpoint that has never been probed
// still appears (marked unprobed). Latest results are read from Postgres.
func (s *Server) handleSMTPProbes(w http.ResponseWriter, r *http.Request) {
	cfg := smtpprobe.LoadConfig()
	universe := cfg.Endpoints

	// Fall back to the stored targets table when no endpoints are configured in this env.
	if len(universe) == 0 {
		if targets, err := s.pg.ListSMTPProbeTargets(r.Context()); err == nil {
			for _, t := range targets {
				universe = append(universe, smtpprobe.Endpoint{Host: t.Host, Port: t.Port, Mode: smtpprobe.Mode(t.Mode)})
			}
		}
	}

	latest, err := s.pg.LatestSMTPProbeResults(r.Context())
	if err != nil {
		s.log.Error("smtp-probes latest", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read probe status")
		return
	}
	byEndpoint := make(map[string]pgstore.SMTPProbeResult, len(latest))
	for _, row := range latest {
		byEndpoint[row.Endpoint] = row
	}

	// If nothing is configured, still surface whatever has been probed historically.
	if len(universe) == 0 {
		for _, row := range latest {
			universe = append(universe, smtpprobe.Endpoint{Host: row.Host, Port: row.Port, Mode: smtpprobe.Mode(row.Mode)})
		}
	}

	var latestTS *time.Time
	out := smtpProbeStatus{Endpoints: make([]smtpProbeEndpointView, 0, len(universe))}
	for _, ep := range universe {
		view := smtpProbeEndpointView{Endpoint: ep}
		if row, ok := byEndpoint[ep.Label()]; ok {
			view = probeRowToView(ep, row)
			if latestTS == nil || row.ProbedAt.After(*latestTS) {
				t := row.ProbedAt
				latestTS = &t
			}
		}
		out.Endpoints = append(out.Endpoints, view)

		out.Summary.TotalEndpoints++
		switch {
		case !view.Probed:
			out.Summary.Unprobed++
		case view.OK:
			out.Summary.Healthy++
		default:
			out.Summary.Failing++
		}
		if view.TLS != nil && view.TLS.Expiring {
			out.Summary.CertWarnings++
		}
	}
	out.ProbedAt = latestTS
	writeJSON(w, http.StatusOK, out)
}

func probeRowToView(ep smtpprobe.Endpoint, row pgstore.SMTPProbeResult) smtpProbeEndpointView {
	probedAt := row.ProbedAt
	v := smtpProbeEndpointView{
		Endpoint:        ep,
		Probed:          true,
		OK:              row.OK,
		Stage:           row.Stage,
		Error:           row.Error,
		LatencyMS:       row.LatencyMS,
		Banner:          row.Banner,
		STARTTLSOffered: row.STARTTLSOffered,
		AuthAdvertised:  row.AuthAdvertised,
		AuthMechs:       row.AuthMechs,
		Greylisting:     row.Greylisting,
		ProbedAt:        &probedAt,
	}
	if row.TLSNegotiated {
		v.TLS = &smtpProbeTLSView{
			Negotiated:      true,
			Version:         row.TLSVersion,
			Cipher:          row.TLSCipher,
			ChainValid:      row.TLSChainValid,
			CertSubject:     row.CertSubject,
			CertIssuer:      row.CertIssuer,
			CertNotAfter:    row.CertNotAfter,
			DaysUntilExpiry: row.CertDaysLeft,
			Expiring:        row.CertExpiring,
		}
	}
	return v
}

type smtpProbeHistoryResponse struct {
	Endpoint string                    `json:"endpoint,omitempty"`
	Points   []chstore.SMTPProbePoint  `json:"points"`
	Uptime   []chstore.SMTPProbeUptime `json:"uptime"`
	Source   string                    `json:"source"` // "clickhouse" | "postgres"
}

// handleSMTPProbeHistory returns per-probe latency/uptime history for charting. The
// high-frequency series comes from ClickHouse; if ClickHouse is unavailable it falls back to
// the recent rows retained in Postgres. Query params: endpoint, since, until, limit.
func (s *Server) handleSMTPProbeHistory(w http.ResponseWriter, r *http.Request) {
	endpoint := r.URL.Query().Get("endpoint")
	since := parseTimeParam(r, "since")
	until := parseTimeParam(r, "until")
	limit := parseIntParam(r, "limit", 1000, 20000)

	resp := smtpProbeHistoryResponse{Endpoint: endpoint, Points: []chstore.SMTPProbePoint{}, Uptime: []chstore.SMTPProbeUptime{}, Source: "clickhouse"}

	if s.ch != nil {
		points, err := s.ch.SMTPProbeHistory(r.Context(), endpoint, since, until, limit)
		if err == nil {
			resp.Points = points
			if up, uerr := s.ch.SMTPProbeUptimeByEndpoint(r.Context(), since, until); uerr == nil {
				resp.Uptime = up
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		s.log.Warn("smtp-probes history from clickhouse failed, falling back to postgres", "err", err)
	}

	// Postgres fallback: recent rows only.
	rows, err := s.pg.SMTPProbeHistory(r.Context(), endpoint, since, limit)
	if err != nil {
		s.log.Error("smtp-probes history", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read probe history")
		return
	}
	resp.Source = "postgres"
	for _, row := range rows {
		days := int32(0)
		if row.CertDaysLeft != nil {
			days = int32(*row.CertDaysLeft)
		}
		resp.Points = append(resp.Points, chstore.SMTPProbePoint{
			ProbedAt:     row.ProbedAt,
			Endpoint:     row.Endpoint,
			OK:           row.OK,
			LatencyMS:    uint32(row.LatencyMS),
			Stage:        row.Stage,
			CertDaysLeft: days,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
