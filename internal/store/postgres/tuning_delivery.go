package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// DeliveryTuning holds the notification/data-pull daemon tuning knobs that were previously
// env-only (poll/scan intervals, throttle/dedup windows, thresholds, the notification
// dashboard URL, and the NL-analytics tool cap). Every field is NON-SECRET (numeric,
// duration-in-seconds, or a plain string), so — unlike IntegrationSettings — nothing here is
// sealed: values are stored and returned in the clear.
//
// It is stored under the "tuning_delivery" key of tenants.settings JSONB (read-modify-write,
// preserving other settings), mirroring IntegrationSettings/SmarthostSettings. The owning
// daemon overlays these onto its env-based config at startup with precedence DASHBOARD (DB) >
// env > default: a value set in the dashboard is authoritative; a blank/zero field means
// "not set" and the daemon keeps its env/default. Changes apply on daemon restart.
type DeliveryTuning struct {
	Notify    NotifyTuning    `json:"notify"`
	SNDS      SNDSTuning      `json:"snds"`
	Seed      SeedTuning      `json:"seed"`
	DMARCPull DMARCPullTuning `json:"dmarc_pull"`
	NL        NLTuning        `json:"nl"`
}

// NotifyTuning tunes notifyd (internal/alertchannels). Durations are in seconds; 0 = default.
type NotifyTuning struct {
	PollIntervalSecs int    `json:"poll_interval_secs,omitempty"` // MXS_NOTIFY_POLL_INTERVAL
	ThrottleSecs     int    `json:"throttle_secs,omitempty"`      // MXS_NOTIFY_THROTTLE
	DedupSecs        int    `json:"dedup_secs,omitempty"`         // MXS_NOTIFY_DEDUP
	LookbackSecs     int    `json:"lookback_secs,omitempty"`      // MXS_NOTIFY_LOOKBACK
	HTTPTimeoutSecs  int    `json:"http_timeout_secs,omitempty"`  // MXS_NOTIFY_HTTP_TIMEOUT
	DashboardURL     string `json:"dashboard_url,omitempty"`      // MXS_NOTIFY_DASHBOARD_URL
}

// SNDSTuning tunes sndsd (internal/snds). The SNDS access key is a secret handled by
// IntegrationSettings and is intentionally NOT part of this group.
type SNDSTuning struct {
	IntervalSecs           int `json:"interval_secs,omitempty"`            // MXS_SNDS_INTERVAL
	JMRPScanIntervalSecs   int `json:"jmrp_scan_interval_secs,omitempty"`  // MXS_JMRP_SCAN_INTERVAL
	JMRPComplaintThreshold int `json:"jmrp_complaint_threshold,omitempty"` // MXS_JMRP_COMPLAINT_THRESHOLD
}

// SeedTuning tunes seedd (internal/seedtest). SMTP/IMAP credentials are secrets handled
// separately and are NOT part of this group.
type SeedTuning struct {
	IntervalSecs      int `json:"interval_secs,omitempty"`       // MXS_SEEDTEST_INTERVAL
	CollectWindowSecs int `json:"collect_window_secs,omitempty"` // MXS_SEEDTEST_COLLECT_WINDOW
}

// DMARCPullTuning tunes dmarcpulld (internal/dmarcpull). The receiver API key is a secret and
// is NOT part of this group.
type DMARCPullTuning struct {
	IntervalSecs int `json:"interval_secs,omitempty"` // MXS_DMARCP_INTERVAL
	LookbackDays int `json:"lookback_days,omitempty"` // MXS_DMARCP_LOOKBACKDAYS
}

// NLTuning tunes the natural-language analytics planner (internal/nlquery), consumed by apid.
type NLTuning struct {
	MaxTools int `json:"max_tools,omitempty"` // MXS_NLQUERY_MAX_TOOLS
}

const deliveryTuningKey = "tuning_delivery"

// GetDeliveryTuning returns the tenant's delivery/data tuning knobs (zero value when unset).
func (s *Store) GetDeliveryTuning(ctx context.Context, tenantID string) (DeliveryTuning, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return DeliveryTuning{}, err
	}
	var m DeliveryTuning
	if raw, ok := all[deliveryTuningKey]; ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			return DeliveryTuning{}, fmt.Errorf("decode delivery tuning: %w", err)
		}
	}
	return m, nil
}

// UpdateDeliveryTuning writes the tenant's delivery/data tuning knobs, merging into (not
// replacing) the rest of the settings JSONB. found=false (nil error) when the tenant does not
// exist. Nothing here is secret, so there is no sealing.
func (s *Store) UpdateDeliveryTuning(ctx context.Context, tenantID string, m DeliveryTuning) (bool, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("encode delivery tuning: %w", err)
	}
	all[deliveryTuningKey] = mb
	out, err := json.Marshal(all)
	if err != nil {
		return false, fmt.Errorf("encode settings: %w", err)
	}
	const q = `UPDATE tenants SET settings = $2, updated_at = now() WHERE id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, out)
	if err != nil {
		return false, fmt.Errorf("update tenant delivery tuning: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
