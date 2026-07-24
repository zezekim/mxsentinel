package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// IntegrationSettings holds the tenant's provider credentials/endpoints that were previously
// env-only (AI backend, Microsoft SNDS, Google Postmaster, notification email). Stored under
// the "integrations" key of tenants.settings JSONB (read-modify-write, preserving other
// settings), mirroring MailSettings/SmarthostSettings.
//
// Every *Enc field is SEALED with the AES-256-GCM Encryptor (MXS_ENCRYPTION_KEY); the API
// seals on write and the owning daemon opens on read. Non-secret fields (endpoints, model,
// host, from) are stored in the clear.
type IntegrationSettings struct {
	AI          AISettings          `json:"ai"`
	Microsoft   MicrosoftSettings   `json:"microsoft"`
	Google      GoogleSettings      `json:"google"`
	NotifyEmail NotifyEmailSettings `json:"notify_email"`
}

// AISettings configures the local/remote OpenAI-compatible LLM used by aid.
type AISettings struct {
	Endpoint    string `json:"endpoint"`
	Model       string `json:"model"`
	APIKeyEnc   string `json:"api_key_enc"`
	TimeoutSecs int    `json:"timeout_secs"`
}

// MicrosoftSettings holds the SNDS automated-data access key (sndsd).
type MicrosoftSettings struct {
	SNDSKeyEnc string `json:"snds_key_enc"`
}

// GoogleSettings holds the Gmail Postmaster Tools API token (fbld).
type GoogleSettings struct {
	PostmasterTokenEnc string `json:"postmaster_token_enc"`
}

// NotifyEmailSettings is the SMTP sender used for email alert delivery.
type NotifyEmailSettings struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	PasswordEnc string `json:"password_enc"`
	From        string `json:"from"`
}

const integrationSettingsKey = "integrations"

// GetIntegrationSettings returns the tenant's provider settings (zero value when unset).
func (s *Store) GetIntegrationSettings(ctx context.Context, tenantID string) (IntegrationSettings, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return IntegrationSettings{}, err
	}
	var m IntegrationSettings
	if raw, ok := all[integrationSettingsKey]; ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			return IntegrationSettings{}, fmt.Errorf("decode integration settings: %w", err)
		}
	}
	return m, nil
}

// UpdateIntegrationSettings writes the tenant's provider settings, merging into the rest of
// the settings JSONB. found=false (nil error) when the tenant does not exist. The caller
// seals *Enc fields before calling.
func (s *Store) UpdateIntegrationSettings(ctx context.Context, tenantID string, m IntegrationSettings) (bool, error) {
	all, err := s.tenantSettings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("encode integration settings: %w", err)
	}
	all[integrationSettingsKey] = mb
	out, err := json.Marshal(all)
	if err != nil {
		return false, fmt.Errorf("encode settings: %w", err)
	}
	const q = `UPDATE tenants SET settings = $2, updated_at = now() WHERE id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, out)
	if err != nil {
		return false, fmt.Errorf("update tenant integration settings: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
