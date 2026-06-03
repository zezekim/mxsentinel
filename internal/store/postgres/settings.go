package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// MailSettings are the tenant-tunable mail parameters surfaced on the dashboard's
// Settings page and in generated guidance / the smarthost docs. They are stored under
// the "mail" key of the tenants.settings JSONB column, so other settings (present or
// future) are preserved by read-modify-write.
type MailSettings struct {
	SPFInclude   string `json:"spf_include"`   // SPF include host advertised to senders, e.g. spf.example.net
	DKIMSelector string `json:"dkim_selector"` // default DKIM selector used in setup guidance, e.g. mxs
	DMARCPolicy  string `json:"dmarc_policy"`  // none | quarantine | reject
	DMARCRua     string `json:"dmarc_rua"`     // aggregate report address (rua=mailto:…)
	DMARCRuf     string `json:"dmarc_ruf"`     // forensic report address (ruf=mailto:…)
	RelayHost    string `json:"relay_host"`    // submission host shown in smarthost instructions
	RelayPort    int    `json:"relay_port"`    // submission port (typically 587)
}

const mailSettingsKey = "mail"

// GetMailSettings returns the tenant's mail settings (zero-valued fields when unset).
func (s *Store) GetMailSettings(ctx context.Context, tenantID string) (MailSettings, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return MailSettings{}, err
	}
	var m MailSettings
	if raw, ok := all[mailSettingsKey]; ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			return MailSettings{}, fmt.Errorf("decode mail settings: %w", err)
		}
	}
	return m, nil
}

// UpdateMailSettings writes the tenant's mail settings, merging into (not replacing) the
// rest of the settings JSONB. found=false (nil error) when the tenant does not exist.
func (s *Store) UpdateMailSettings(ctx context.Context, tenantID string, m MailSettings) (bool, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("encode mail settings: %w", err)
	}
	all[mailSettingsKey] = mb
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

// tenantSettings reads the raw settings JSONB into a top-level key→value map.
func (s *Store) tenantSettings(ctx context.Context, tenantID string) (map[string]json.RawMessage, error) {
	const q = `SELECT settings FROM tenants WHERE id = $1`
	var raw []byte
	if err := s.Pool.QueryRow(ctx, q, tenantID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("read tenant settings: %w", err)
	}
	all := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, fmt.Errorf("decode tenant settings: %w", err)
		}
	}
	return all, nil
}
