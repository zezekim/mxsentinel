package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// SmarthostSettings is the tenant's outbound fallback-smarthost configuration (e.g. mail.baby):
// where to route certain recipient domains when the relay's own IP can't deliver to them.
// It is stored under the "smarthost" key of tenants.settings JSONB (read-modify-write, so
// other settings are preserved), mirroring MailSettings.
//
// PasswordEnc holds the smarthost SMTP password SEALED with the AES-256-GCM Encryptor (the
// same one used for cPanel/WHMCS/alert-channel secrets). The API seals on write; relayfailoverd
// opens it to render the Postfix SASL map. The plaintext password is never stored or returned.
type SmarthostSettings struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`         // smarthost hostname, e.g. relay.mailbaby.net
	Port        int      `json:"port"`         // submission port, typically 587
	Username    string   `json:"username"`     // SMTP AUTH username at the smarthost
	PasswordEnc string   `json:"password_enc"` // sealed password ("v1:..."); never returned by the API
	Mode        string   `json:"mode"`         // "always" (pin domains) | "on_throttle" (4xx breaker)
	Domains     []string `json:"domains"`      // recipient domains to route via the smarthost

	// Breaker tuning (used when Mode == "on_throttle"). Zero = daemon default.
	TripRate    float64 `json:"trip_rate,omitempty"`
	WindowSecs  int     `json:"window_secs,omitempty"`
	HoldSecs    int     `json:"hold_secs,omitempty"`
	MinAttempts int     `json:"min_attempts,omitempty"`
	MinDefers   int     `json:"min_defers,omitempty"`
}

const smarthostSettingsKey = "smarthost"

// GetSmarthostSettings returns the tenant's fallback-smarthost config (zero value when unset).
func (s *Store) GetSmarthostSettings(ctx context.Context, tenantID string) (SmarthostSettings, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return SmarthostSettings{}, err
	}
	var m SmarthostSettings
	if raw, ok := all[smarthostSettingsKey]; ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			return SmarthostSettings{}, fmt.Errorf("decode smarthost settings: %w", err)
		}
	}
	return m, nil
}

// UpdateSmarthostSettings writes the tenant's smarthost config, merging into (not replacing)
// the rest of the settings JSONB. found=false (nil error) when the tenant does not exist.
// The caller is responsible for sealing PasswordEnc before calling.
func (s *Store) UpdateSmarthostSettings(ctx context.Context, tenantID string, m SmarthostSettings) (bool, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("encode smarthost settings: %w", err)
	}
	all[smarthostSettingsKey] = mb
	out, err := json.Marshal(all)
	if err != nil {
		return false, fmt.Errorf("encode settings: %w", err)
	}
	const q = `UPDATE tenants SET settings = $2, updated_at = now() WHERE id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, out)
	if err != nil {
		return false, fmt.Errorf("update tenant smarthost settings: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
