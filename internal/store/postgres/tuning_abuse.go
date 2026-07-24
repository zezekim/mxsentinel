package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// AbuseTuning are the dashboard-managed runtime knobs for the abuse/compromise (authwatchd)
// and bounce/suppression (bounced) daemons. They are NON-SECRET numeric/bool tuning values
// that were previously env-only; surfacing them here lets an operator adjust them from the
// Settings page without redeploying.
//
// Stored under the "tuning_abuse" key of the tenants.settings JSONB column (read-modify-write
// so sibling settings groups are preserved). Every field is numeric or bool, and a ZERO value
// (0 / false) means "unset — fall back to the daemon's env var / built-in default". Durations
// are carried as integer SECONDS to keep the JSON/API contract language-neutral.
type AbuseTuning struct {
	Authwatch AuthwatchTuning `json:"authwatch"`
	Bounce    BounceTuning    `json:"bounce"`
}

// AuthwatchTuning mirrors the tunable fields of internal/authwatch.Config (plus the opt-in
// auto-lock switch). Window/Cooldown are seconds. Zero = unset.
type AuthwatchTuning struct {
	Threshold      float64 `json:"threshold"`       // AUTHWATCH_THRESHOLD — trip line on the summed score
	WindowSecs     int     `json:"window_secs"`     // AUTHWATCH_WINDOW — rolling per-credential window
	CooldownSecs   int     `json:"cooldown_secs"`   // AUTHWATCH_COOLDOWN — min time between trips for one credential
	MinVolume      int     `json:"min_volume"`      // AUTHWATCH_MIN_VOLUME — messages before bounce-rate scoring applies
	DistinctRcpt   int     `json:"distinct_rcpt"`   // AUTHWATCH_DISTINCT_RCPT — distinct-recipient-domain burst threshold
	BounceRate     float64 `json:"bounce_rate"`     // AUTHWATCH_BOUNCE_RATE — spam/block bounce fraction that contributes
	VolumeFactor   float64 `json:"volume_factor"`   // AUTHWATCH_VOLUME_FACTOR — multiple of baseline treated as a spike
	VolumeFloor    int     `json:"volume_floor"`    // AUTHWATCH_VOLUME_FLOOR — minimum volume before spike scoring applies
	OffHoursStart  int     `json:"offhours_start"`  // AUTHWATCH_OFFHOURS_START — inclusive UTC hour [0,24)
	OffHoursEnd    int     `json:"offhours_end"`    // AUTHWATCH_OFFHOURS_END — exclusive UTC hour [0,24)
	OffHoursRate   float64 `json:"offhours_rate"`   // AUTHWATCH_OFFHOURS_RATE — off-hours concentration that contributes
	OffHoursWeight float64 `json:"offhours_weight"` // AUTHWATCH_OFFHOURS_WEIGHT — score weight for the off-hours signal
	Autolock       bool    `json:"autolock"`        // AUTHWATCH_AUTOLOCK — auto-lock a credential on trip (opt-in)
}

// BounceTuning mirrors internal/bounce.Config. Interval/Lookback are seconds. Zero = unset.
type BounceTuning struct {
	IntervalSecs int `json:"interval_secs"` // MXS_BOUNCE_INTERVAL — how often recent bounces are re-scanned
	LookbackSecs int `json:"lookback_secs"` // MXS_BOUNCE_LOOKBACK — window of recent bounces re-read each tick
	MaxRows      int `json:"max_rows"`      // MXS_BOUNCE_MAXROWS — cap on rows pulled per tick
}

const abuseTuningKey = "tuning_abuse"

// GetAbuseTuning returns the tenant's abuse/bounce tuning (zero-valued fields when unset).
func (s *Store) GetAbuseTuning(ctx context.Context, tenantID string) (AbuseTuning, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return AbuseTuning{}, err
	}
	var t AbuseTuning
	if raw, ok := all[abuseTuningKey]; ok {
		if err := json.Unmarshal(raw, &t); err != nil {
			return AbuseTuning{}, fmt.Errorf("decode abuse tuning: %w", err)
		}
	}
	return t, nil
}

// UpdateAbuseTuning writes the tenant's abuse/bounce tuning, merging into (not replacing) the
// rest of the settings JSONB. found=false (nil error) when the tenant does not exist.
func (s *Store) UpdateAbuseTuning(ctx context.Context, tenantID string, t AbuseTuning) (bool, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	tb, err := json.Marshal(t)
	if err != nil {
		return false, fmt.Errorf("encode abuse tuning: %w", err)
	}
	all[abuseTuningKey] = tb
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
