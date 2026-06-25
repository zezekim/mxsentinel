package cpanelplugin

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// relay.go is the WHM admin tool's core: it provisions the server's SASL credential on
// the MX Sentinel relay, drives the Exim overlay, and reports/tests status. It runs only
// in the privileged WHM CGI (root), never in the unprivileged user path.

const (
	defaultStatePath     = "/etc/mxsentinel/relay-state.json"
	defaultLocalDomains  = "/etc/localdomains"
	defaultRelayPort     = 587
	credentialNamePrefix = "cpanel"
)

// relayState is persisted root-only; it remembers the provisioned credential so Enable is
// idempotent (we don't re-mint a credential we can't read back).
type relayState struct {
	RelayHost     string `json:"relay_host"`
	RelayPort     int    `json:"relay_port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	SMTPUserID    string `json:"smtp_user_id"`
	Enabled       bool   `json:"enabled"`
	ProvisionedAt string `json:"provisioned_at"`
}

// relayManager wires the API client, the Exim overlay, and persisted state.
type relayManager struct {
	cfg              Config
	up               *upstream
	exim             *eximManager
	statePath        string
	localDomainsPath string
	hostname         string
	now              func() time.Time
}

func newRelayManager(cfg Config) *relayManager {
	host, _ := os.Hostname()
	return &relayManager{
		cfg:              cfg,
		up:               newUpstream(cfg),
		exim:             newEximManager(),
		statePath:        defaultStatePath,
		localDomainsPath: defaultLocalDomains,
		hostname:         host,
		now:              time.Now,
	}
}

// RelayStatus is the JSON the WHM UI renders.
type RelayStatus struct {
	APIBase     string `json:"api_base"`
	RelayHost   string `json:"relay_host"`
	RelayPort   int    `json:"relay_port"`
	Username    string `json:"username,omitempty"`
	Configured  bool   `json:"configured"`  // relay_host present in MX Sentinel settings
	Provisioned bool   `json:"provisioned"` // a credential has been minted
	Enabled     bool   `json:"enabled"`     // Exim overlay is installed
	Notice      string `json:"notice,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Status reports the current routing state. Settings are fetched best-effort so the page
// still renders if the API is briefly unreachable.
func (m *relayManager) Status(ctx context.Context) RelayStatus {
	st := RelayStatus{APIBase: m.cfg.APIBase}
	state, _ := m.loadState()
	st.Username = state.Username
	st.Provisioned = state.Username != ""
	st.RelayHost = state.RelayHost
	st.RelayPort = state.RelayPort

	if en, err := m.exim.enabled(); err != nil {
		st.Error = err.Error()
	} else {
		st.Enabled = en
	}

	if s, err := m.up.GetSettings(ctx); err != nil {
		if st.Notice == "" {
			st.Notice = "Could not reach MX Sentinel API: " + err.Error()
		}
	} else {
		if s.RelayHost != "" {
			st.RelayHost = s.RelayHost
			st.Configured = true
		}
		if st.RelayPort == 0 {
			st.RelayPort = s.RelayPort
		}
	}
	if st.RelayPort == 0 {
		st.RelayPort = defaultRelayPort
	}
	return st
}

// Enable provisions a credential (if needed) and installs the Exim smarthost overlay so
// all outbound non-local mail egresses via the relay.
func (m *relayManager) Enable(ctx context.Context) (RelayStatus, error) {
	s, err := m.up.GetSettings(ctx)
	if err != nil {
		return m.Status(ctx), fmt.Errorf("read MX Sentinel settings: %w", err)
	}
	host := strings.TrimSpace(s.RelayHost)
	if host == "" {
		return m.Status(ctx), fmt.Errorf("no relay host configured — set Relay Host in MX Sentinel Settings first")
	}
	port := s.RelayPort
	if port == 0 {
		port = defaultRelayPort
	}

	state, _ := m.loadState()
	state.RelayHost, state.RelayPort = host, port
	if state.Username == "" || state.SMTPUserID == "" || state.Password == "" {
		if err := m.provision(ctx, &state); err != nil {
			return m.Status(ctx), err
		}
		// Persist the credential immediately — before touching Exim — so a later
		// failure never orphans it (which previously forced "+timestamp" clones).
		if err := m.saveState(state); err != nil {
			return m.Status(ctx), fmt.Errorf("provisioned credential but failed to save state: %w", err)
		}
	}

	if err := m.exim.apply(ctx, host, port, state.Username, state.Password); err != nil {
		return m.Status(ctx), err
	}
	state.Enabled = true
	if err := m.saveState(state); err != nil {
		return m.Status(ctx), fmt.Errorf("routing enabled but failed to save state: %w", err)
	}
	return m.Status(ctx), nil
}

// provision sets a fresh SASL credential into state using a single canonical username.
// If that username already exists on the tenant (e.g. a prior partial run), it reuses
// that user and resets its password rather than minting a new "+timestamp" clone.
func (m *relayManager) provision(ctx context.Context, state *relayState) error {
	username := m.defaultUsername()
	password, err := randPassword(28)
	if err != nil {
		return err
	}
	user, err := m.up.CreateSMTPUser(ctx, username, password, m.hostname)
	if err != nil {
		// Likely a name collision with an existing credential — adopt it and reset
		// its password to the one we just generated, so the canonical name is reused.
		existing, lookupErr := m.findSMTPUser(ctx, username)
		if lookupErr != nil || existing.ID == "" {
			return fmt.Errorf("provision relay credential: %w", err)
		}
		if rErr := m.up.ResetSMTPUserPassword(ctx, existing.ID, password); rErr != nil {
			return fmt.Errorf("reset existing relay credential %q: %w", username, rErr)
		}
		user = existing
	}
	state.Username = username
	if user.Username != "" {
		state.Username = user.Username
	}
	state.Password = password
	state.SMTPUserID = user.ID
	state.ProvisionedAt = m.now().UTC().Format(time.RFC3339)
	return nil
}

// findSMTPUser looks up an SMTP user by exact username (case-insensitive).
func (m *relayManager) findSMTPUser(ctx context.Context, username string) (SMTPUser, error) {
	users, err := m.up.ListSMTPUsers(ctx)
	if err != nil {
		return SMTPUser{}, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	return SMTPUser{}, nil
}

// Disable removes the Exim overlay (mail returns to direct delivery). The credential is
// left intact so re-enabling is instant.
func (m *relayManager) Disable(ctx context.Context) (RelayStatus, error) {
	if err := m.exim.remove(ctx); err != nil {
		return m.Status(ctx), err
	}
	state, _ := m.loadState()
	state.Enabled = false
	_ = m.saveState(state)
	return m.Status(ctx), nil
}

// TestResult is returned by the auth/probe test.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Test connects to the relay, runs STARTTLS + AUTH with the provisioned credential, and
// optionally sends a message to `to`. It verifies the path without changing config.
func (m *relayManager) Test(ctx context.Context, to string) TestResult {
	state, _ := m.loadState()
	if state.Username == "" || state.Password == "" {
		return TestResult{Message: "No credential provisioned yet — click Enable first."}
	}
	host := state.RelayHost
	port := state.RelayPort
	if port == 0 {
		port = defaultRelayPort
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return TestResult{Message: fmt.Sprintf("cannot reach relay %s: %v", addr, err)}
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return TestResult{Message: fmt.Sprintf("smtp handshake failed: %v", err)}
	}
	defer c.Close()

	// snakeoil cert tolerated (see docs/smarthost.md §6); we require encryption, not a
	// verified chain.
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, InsecureSkipVerify: true}); err != nil {
			return TestResult{Message: fmt.Sprintf("STARTTLS failed: %v", err)}
		}
	} else {
		return TestResult{Message: "relay did not offer STARTTLS on this port"}
	}
	if err := c.Auth(smtp.PlainAuth("", state.Username, state.Password, host)); err != nil {
		return TestResult{Message: fmt.Sprintf("authentication failed: %v", err)}
	}

	if to = strings.TrimSpace(to); to != "" {
		from := state.Username
		if err := c.Mail(from); err != nil {
			return TestResult{Message: fmt.Sprintf("MAIL FROM rejected: %v", err)}
		}
		if err := c.Rcpt(to); err != nil {
			return TestResult{Message: fmt.Sprintf("RCPT TO rejected: %v", err)}
		}
		wc, err := c.Data()
		if err != nil {
			return TestResult{Message: fmt.Sprintf("DATA rejected: %v", err)}
		}
		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: MX Sentinel relay test\r\n\r\nThis is a test message from the MX Sentinel cPanel plugin on %s.\r\n", from, to, m.hostname)
		if _, err := wc.Write([]byte(msg)); err != nil {
			return TestResult{Message: fmt.Sprintf("writing message failed: %v", err)}
		}
		if err := wc.Close(); err != nil {
			return TestResult{Message: fmt.Sprintf("relay rejected the message: %v", err)}
		}
		_ = c.Quit()
		return TestResult{OK: true, Message: fmt.Sprintf("Authenticated and delivered a test message to %s via %s.", to, addr)}
	}

	_ = c.Quit()
	return TestResult{OK: true, Message: fmt.Sprintf("Authenticated successfully to %s as %s.", addr, state.Username)}
}

// DNSRecord is one record to publish for a sending domain.
type DNSRecord struct {
	Domain string `json:"domain"`
	SPF    string `json:"spf"`
	DKIM   string `json:"dkim"`
	DMARC  string `json:"dmarc"`
}

// DNSRecords renders the records each local domain should publish so relayed mail
// authenticates, derived from MX Sentinel settings.
func (m *relayManager) DNSRecords(ctx context.Context) ([]DNSRecord, error) {
	s, err := m.up.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	selector := s.DKIMSelector
	if selector == "" {
		selector = "mxs"
	}
	policy := s.DMARCPolicy
	if policy == "" {
		policy = "none"
	}
	spfVal := "v=spf1 ~all"
	if s.SPFInclude != "" {
		spfVal = fmt.Sprintf("v=spf1 include:%s ~all", s.SPFInclude)
	}
	dmarc := fmt.Sprintf("v=DMARC1; p=%s", policy)
	if s.DMARCRua != "" {
		dmarc += fmt.Sprintf("; rua=mailto:%s", s.DMARCRua)
	}

	records := []DNSRecord{}
	for _, d := range m.localDomains() {
		records = append(records, DNSRecord{
			Domain: d,
			SPF:    fmt.Sprintf("%s. TXT \"%s\"", d, spfVal),
			DKIM:   fmt.Sprintf("%s._domainkey.%s. TXT (publish the public key from MX Sentinel Settings / the relay's /etc/opendkim/keys)", selector, d),
			DMARC:  fmt.Sprintf("_dmarc.%s. TXT \"%s\"", d, dmarc),
		})
	}
	return records, nil
}

// --- helpers ---------------------------------------------------------------------

func (m *relayManager) loadState() (relayState, error) {
	var s relayState
	b, err := os.ReadFile(m.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func (m *relayManager) saveState(s relayState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.statePath, b, 0o600)
}

func (m *relayManager) hostOrName() string {
	if m.hostname != "" {
		return m.hostname
	}
	return "localhost"
}

// defaultUsername builds a per-server SASL login from the hostname, e.g. relay@host.fqdn.
func (m *relayManager) defaultUsername() string {
	return fmt.Sprintf("%s-relay@%s", credentialNamePrefix, m.hostOrName())
}

// localDomains reads /etc/localdomains (cPanel's list of locally-handled domains).
func (m *relayManager) localDomains() []string {
	b, err := os.ReadFile(m.localDomainsPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		d := strings.ToLower(strings.TrimSpace(ln))
		if d != "" && !strings.HasPrefix(d, "*") {
			out = append(out, d)
		}
	}
	return out
}

// randPassword returns an n-char alphanumeric password. Alphanumeric only, so it is safe
// inside Exim's colon-separated client_send list (no ':', '\', or '$' to escape).
func randPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf), nil
}
