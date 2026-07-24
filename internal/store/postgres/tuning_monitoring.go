package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// MonitoringTuning holds the dashboard-managed tuning knobs for the monitoring daemons
// (tlsrptd, probed, bimid). These are the NON-SECRET numeric/duration/bool/string cadence
// and timeout parameters that were previously env-only (MXS_TLSRPT_*, MXS_MTASTS_*,
// MXS_PROBE_*, MXS_BIMI_*). They are stored under the "tuning_monitoring" key of the
// tenants.settings JSONB column so other settings are preserved by read-modify-write.
//
// A zero-valued field (0 / "") means "not set on the dashboard" — the daemon falls back to
// its environment variable, then its built-in default. Durations are stored as whole
// seconds; day-based knobs as whole days.
type MonitoringTuning struct {
	TLSRPT TLSRPTTuning `json:"tlsrpt"`
	MTASTS MTASTSTuning `json:"mtasts"`
	Probe  ProbeTuning  `json:"probe"`
	BIMI   BIMITuning   `json:"bimi"`
}

// TLSRPTTuning maps to the TLS-RPT drop-dir scanner (MXS_TLSRPT_INTERVAL).
type TLSRPTTuning struct {
	IntervalSecs int `json:"interval_secs"` // MXS_TLSRPT_INTERVAL — drop-dir scan interval
}

// MTASTSTuning maps to the MTA-STS / MX-cert monitor.
type MTASTSTuning struct {
	IntervalSecs    int `json:"interval_secs"`     // MXS_MTASTS_INTERVAL — re-check interval
	CertWarnDays    int `json:"cert_warn_days"`    // MXS_MTASTS_CERT_WARN_DAYS
	CertTimeoutSecs int `json:"cert_timeout_secs"` // MXS_MTASTS_CERT_TIMEOUT
	HTTPTimeoutSecs int `json:"http_timeout_secs"` // MXS_MTASTS_HTTP_TIMEOUT
}

// ProbeTuning maps to the synthetic SMTP prober (endpoint topology stays env-only).
type ProbeTuning struct {
	IntervalSecs       int    `json:"interval_secs"`        // MXS_PROBE_INTERVAL
	ConnectTimeoutSecs int    `json:"connect_timeout_secs"` // MXS_PROBE_CONNECT_TIMEOUT
	CommandTimeoutSecs int    `json:"command_timeout_secs"` // MXS_PROBE_COMMAND_TIMEOUT
	CertWarnDays       int    `json:"cert_warn_days"`       // MXS_PROBE_CERT_WARN (days)
	EHLOName           string `json:"ehlo_name"`            // MXS_PROBE_EHLO_NAME
	TLSInsecure        bool   `json:"tls_insecure"`         // MXS_PROBE_TLS_INSECURE
	CheckResponse      bool   `json:"check_response"`       // MXS_PROBE_CHECK_RESPONSE
}

// BIMITuning maps to the BIMI / VMC readiness daemon.
type BIMITuning struct {
	IntervalSecs     int `json:"interval_secs"`      // MXS_BIMI_INTERVAL
	FetchTimeoutSecs int `json:"fetch_timeout_secs"` // MXS_BIMI_FETCH_TIMEOUT
}

const monitoringTuningKey = "tuning_monitoring"

// GetMonitoringTuning returns the tenant's monitoring tuning (zero-valued fields when unset).
func (s *Store) GetMonitoringTuning(ctx context.Context, tenantID string) (MonitoringTuning, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return MonitoringTuning{}, err
	}
	var m MonitoringTuning
	if raw, ok := all[monitoringTuningKey]; ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			return MonitoringTuning{}, fmt.Errorf("decode monitoring tuning: %w", err)
		}
	}
	return m, nil
}

// UpdateMonitoringTuning writes the tenant's monitoring tuning, merging into (not replacing)
// the rest of the settings JSONB. found=false (nil error) when the tenant does not exist.
func (s *Store) UpdateMonitoringTuning(ctx context.Context, tenantID string, m MonitoringTuning) (bool, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("encode monitoring tuning: %w", err)
	}
	all[monitoringTuningKey] = mb
	out, err := json.Marshal(all)
	if err != nil {
		return false, fmt.Errorf("encode settings: %w", err)
	}
	const q = `UPDATE tenants SET settings = $2, updated_at = now() WHERE id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, out)
	if err != nil {
		return false, fmt.Errorf("update tenant settings: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
