// Package integrationsettings resolves the dashboard-managed provider settings (AI backend,
// Microsoft SNDS, Google Postmaster, notification email) for a daemon, opening the sealed
// secrets. Precedence is DASHBOARD (DB) value > env var > built-in default: when a value is
// set in the dashboard it is authoritative (so editing it in the UI actually takes effect);
// env remains the bootstrap/fallback for deployments that don't use the dashboard for these.
// Daemons call Resolve at startup (or per tick) and overlay the returned values onto their
// env-based config with Prefer(dbValue, envValue).
package integrationsettings

import (
	"context"
	"log/slog"
	"strings"

	"github.com/zezekim/mxsentinel/internal/crypto"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Resolved holds the opened (plaintext) provider settings. Empty fields mean "not configured
// in the dashboard" — the daemon keeps its env/default value for those.
type Resolved struct {
	AIEndpoint      string
	AIModel         string
	AIAPIKey        string
	AITimeoutSecs   int
	SNDSKey         string
	PostmasterToken string
	NotifyEmailHost string
	NotifyEmailPort int
	NotifyEmailUser string
	NotifyEmailPass string
	NotifyEmailFrom string
}

// Resolve reads the tenant's dashboard-managed provider settings and opens the sealed secrets.
// tenantID == "" returns an empty Resolved (no error): the deployment isn't using the
// dashboard for these, so daemons fall back to env. A read/decrypt error is logged and treated
// as "not configured" so a transient DB/keys issue never blanks a working env credential.
func Resolve(ctx context.Context, pg *pgstore.Store, enc *crypto.Encryptor, tenantID string, log *slog.Logger) Resolved {
	if tenantID == "" || pg == nil {
		return Resolved{}
	}
	m, err := pg.GetIntegrationSettings(ctx, tenantID)
	if err != nil {
		if log != nil {
			log.Warn("read dashboard provider settings; using env config", "err", err)
		}
		return Resolved{}
	}
	open := func(sealed string) string {
		if sealed == "" {
			return ""
		}
		if enc == nil {
			return sealed // stored plaintext (no MXS_ENCRYPTION_KEY)
		}
		pt, oerr := enc.Open(sealed)
		if oerr != nil {
			if log != nil {
				log.Error("open dashboard provider secret; ignoring", "err", oerr)
			}
			return ""
		}
		return pt
	}
	return Resolved{
		AIEndpoint:      strings.TrimSpace(m.AI.Endpoint),
		AIModel:         strings.TrimSpace(m.AI.Model),
		AIAPIKey:        open(m.AI.APIKeyEnc),
		AITimeoutSecs:   m.AI.TimeoutSecs,
		SNDSKey:         open(m.Microsoft.SNDSKeyEnc),
		PostmasterToken: open(m.Google.PostmasterTokenEnc),
		NotifyEmailHost: strings.TrimSpace(m.NotifyEmail.Host),
		NotifyEmailPort: m.NotifyEmail.Port,
		NotifyEmailUser: strings.TrimSpace(m.NotifyEmail.Username),
		NotifyEmailPass: open(m.NotifyEmail.PasswordEnc),
		NotifyEmailFrom: strings.TrimSpace(m.NotifyEmail.From),
	}
}

// Prefer returns primary if non-empty (trimmed), else fallback. Callers pass the dashboard
// (DB) value as primary and the env value as fallback, so the dashboard wins when set.
func Prefer(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

// PreferInt is Prefer for ints (0 = unset).
func PreferInt(primary, fallback int) int {
	if primary != 0 {
		return primary
	}
	return fallback
}
