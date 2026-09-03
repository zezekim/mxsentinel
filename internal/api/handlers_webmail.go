package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Roundcube autologin — the "open webmail" button on the SMTP Users page.
//
// Flow (docs/webmail-autologin.md):
//
//	dashboard ──POST /v1/smtp-users/{id}/webmail-session──▶ apid   (admin scope, audited)
//	          ◀── { url: <roundcube>/?_mxs_autologin=mxw_… } ──┘
//	browser   ──GET that url─────────────────────────────▶ Roundcube (mxs_autologin plugin)
//	Roundcube ──POST /v1/webmail/redeem  { token }───────▶ apid   (X-MXS-Webmail-Secret)
//	          ◀── { username, password, imap_host, … } ──┘  then rcmail::login()
//
// The token is single-use, expires in seconds, and is stored only as a SHA-256 hash. The
// password it resolves to comes from smtp_users.password_enc, unsealed here — apid is the
// only component holding both the encryption key and the redeem secret.

// webmailSecretHeader carries the shared secret the Roundcube plugin presents when
// redeeming a token. /v1/webmail/redeem is registered outside the tenant auth pipeline
// (the plugin has no API token of its own), so this header is its entire authentication.
const webmailSecretHeader = "X-MXS-Webmail-Secret"

// defaultWebmailTokenTTL bounds how long a minted autologin token stays redeemable when no
// TTL is configured. The token is consumed by an immediate redirect, so this is generous.
const defaultWebmailTokenTTL = 60 * time.Second

// WebmailOptions configures the autologin handoff. Autologin stays disabled unless both
// BaseURL and PluginSecret are set — a half-configured handoff would mint tokens nothing
// can redeem, or worse, expose a redeem endpoint with an empty secret.
type WebmailOptions struct {
	BaseURL      string        // public Roundcube origin+path, e.g. https://sentinel.example.com/roundcube
	PluginSecret string        // shared secret for POST /v1/webmail/redeem
	IMAPHost     string        // hostname as resolved from Roundcube's network
	IMAPPort     int           // 143 (STARTTLS) or 993 (implicit TLS)
	IMAPTLS      string        // "starttls" | "tls" | "none"
	TokenTTL     time.Duration // 0 → defaultWebmailTokenTTL
}

// webmailEnabled reports whether the autologin handoff is fully configured.
func (s *Server) webmailEnabled() bool {
	return s.webmail.BaseURL != "" && s.webmail.PluginSecret != ""
}

// tokenTTL returns the configured autologin token lifetime, or the default.
func (o WebmailOptions) tokenTTL() time.Duration {
	if o.TokenTTL <= 0 {
		return defaultWebmailTokenTTL
	}
	return o.TokenTTL
}

// roundcubeHost renders the IMAP endpoint in the form Roundcube expects for its host
// argument: a "tls://" prefix asks for STARTTLS on 143, "ssl://" for implicit TLS on 993,
// and a bare hostname for an unencrypted connection.
func (o WebmailOptions) roundcubeHost() string {
	host := o.IMAPHost
	if host == "" {
		return ""
	}
	switch strings.ToLower(o.IMAPTLS) {
	case "tls", "ssl", "implicit":
		return "ssl://" + host
	case "none", "plain", "":
		return host
	default: // "starttls"
		return "tls://" + host
	}
}

// roundcubePort returns the IMAP port, defaulting to the one implied by the TLS mode.
func (o WebmailOptions) roundcubePort() int {
	if o.IMAPPort > 0 {
		return o.IMAPPort
	}
	switch strings.ToLower(o.IMAPTLS) {
	case "tls", "ssl", "implicit":
		return 993
	default:
		return 143
	}
}

// autologinURL builds the one-shot URL the browser is sent to.
func (o WebmailOptions) autologinURL(token string) string {
	return strings.TrimRight(o.BaseURL, "/") + "/?_mxs_autologin=" + url.QueryEscape(token)
}

type webmailSessionJSON struct {
	Username  string `json:"username"`
	URL       string `json:"url"`
	Token     string `json:"token"` // returned once; the row stores only its hash
	ExpiresAt string `json:"expires_at"`
}

// handleCreateWebmailSession mints a single-use Roundcube autologin URL for one SMTP user.
//
// POST /v1/smtp-users/{id}/webmail-session   (scope: admin)
//
// Admin scope, not write: this hands the caller a live session as that mailbox, so it is
// exactly as privileged as resetting the credential's password.
func (s *Server) handleCreateWebmailSession(w http.ResponseWriter, r *http.Request) {
	if !s.webmailEnabled() {
		writeError(w, http.StatusServiceUnavailable, "not_configured",
			"webmail autologin is not configured (set MXS_WEBMAIL_BASEURL and MXS_WEBMAIL_PLUGINSECRET)")
		return
	}
	tenant := s.tenant(r)
	id := r.PathValue("id")

	username, enabled, hasWebmail, found, err := s.pg.GetSMTPUserForWebmail(r.Context(), tenant, id)
	if err != nil {
		s.log.Error("get smtp user for webmail", "tenant_id", tenant, "smtp_user_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to look up smtp user")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "smtp user not found")
		return
	}
	if !enabled {
		writeError(w, http.StatusConflict, "disabled", "this SMTP user is disabled; enable it before opening webmail")
		return
	}
	if !hasWebmail {
		// Either no encryption key is configured, or the row predates the sealed-password
		// column. Both are fixed the same way: reset the password so apid can seal it.
		writeError(w, http.StatusConflict, "no_webmail_credential",
			"webmail is unavailable for this user — reset its password to enable autologin")
		return
	}

	token, prefix, hash, err := GenerateWebmailToken()
	if err != nil {
		s.log.Error("generate webmail token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to mint token")
		return
	}
	expiresAt := time.Now().Add(s.webmail.tokenTTL())

	a, _ := authFromContext(r.Context())
	if _, err := s.pg.CreateWebmailToken(r.Context(), tenant, id, prefix, hash, a.UserID, clientIP(r), expiresAt); err != nil {
		s.log.Error("create webmail token", "tenant_id", tenant, "smtp_user_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to mint token")
		return
	}
	s.log.Info("webmail session minted",
		"tenant_id", tenant, "smtp_user_id", id, "smtp_user", username,
		"token_prefix", prefix, "by_user_id", a.UserID, "client_ip", clientIP(r))

	writeJSON(w, http.StatusCreated, webmailSessionJSON{
		Username:  username,
		URL:       s.webmail.autologinURL(token),
		Token:     token,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

type webmailRedeemResponse struct {
	Username string `json:"username"`
	Password string `json:"password"`
	IMAPHost string `json:"imap_host"` // Roundcube host form, e.g. "tls://mail.example.com"
	IMAPPort int    `json:"imap_port"`
}

// handleRedeemWebmailToken exchanges a single-use autologin token for IMAP credentials.
//
// POST /v1/webmail/redeem   (no tenant auth; X-MXS-Webmail-Secret required)
//
// Only the Roundcube mxs_autologin plugin calls this, from inside the deployment network.
// Every failure mode answers with the same generic 401/400 so the endpoint cannot be used
// to probe which tokens or usernames exist.
func (s *Server) handleRedeemWebmailToken(w http.ResponseWriter, r *http.Request) {
	if !s.webmailEnabled() {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "webmail autologin is not configured")
		return
	}
	presented := r.Header.Get(webmailSecretHeader)
	if subtle.ConstantTimeCompare([]byte(presented), []byte(s.webmail.PluginSecret)) != 1 {
		s.log.Warn("webmail redeem rejected: bad plugin secret", "client_ip", clientIP(r))
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webmail plugin secret")
		return
	}
	// Checked after the secret so an unauthenticated caller learns nothing about our config.
	// The sealed copy could only have been written by a component that HAD a key (mxctl on
	// another host, say); refuse rather than dereference a passthrough encryptor.
	if !s.enc.Enabled() {
		s.log.Error("webmail redeem impossible: no encryption key configured")
		writeError(w, http.StatusServiceUnavailable, "not_configured", "webmail autologin requires MXS_ENCRYPTION_KEY")
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	prefix := WebmailTokenPrefixOf(body.Token)
	if prefix == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed autologin token")
		return
	}

	cred, found, err := s.pg.RedeemWebmailToken(r.Context(), prefix, HashToken(body.Token))
	if err != nil {
		s.log.Error("redeem webmail token", "token_prefix", prefix, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to redeem token")
		return
	}
	if !found {
		// Expired, already redeemed, unknown, or the user was disabled in between.
		s.log.Warn("webmail redeem rejected: token not redeemable", "token_prefix", prefix, "client_ip", clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid_token", "autologin token is not valid")
		return
	}

	password, err := s.enc.Open(cred.PasswordEnc)
	if err != nil {
		s.log.Error("unseal webmail credential", "tenant_id", cred.TenantID, "smtp_user_id", cred.SMTPUserID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to unseal credential")
		return
	}
	s.log.Info("webmail token redeemed",
		"tenant_id", cred.TenantID, "smtp_user_id", cred.SMTPUserID, "smtp_user", cred.Username,
		"token_prefix", prefix, "client_ip", clientIP(r))

	writeJSON(w, http.StatusOK, webmailRedeemResponse{
		Username: cred.Username,
		Password: password,
		IMAPHost: s.webmail.roundcubeHost(),
		IMAPPort: s.webmail.roundcubePort(),
	})
}
