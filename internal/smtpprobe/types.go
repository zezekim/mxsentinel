// Package smtpprobe implements active ("synthetic") SMTP health probing of the relay's
// own submission/relay endpoints, complementing the platform's passive maillog telemetry.
//
// For each configured endpoint (host:port with a transport mode) a probe performs:
//   - TCP connect + latency measurement,
//   - SMTP banner read,
//   - EHLO + capability parsing (STARTTLS / AUTH / PIPELINING / SIZE / …),
//   - STARTTLS (or implicit TLS on :465) negotiation and X.509 certificate inspection
//     (expiry, chain validity), and
//   - optional MAIL/RCPT response-behaviour sampling (greylisting detection).
//
// The probe logic depends only on the Dialer and TLSHandshaker interfaces so it is fully
// unit-testable with in-memory fakes — no live network or real ports are needed by tests.
// The pure parsing helpers (ParseEHLO, InspectCerts) are tested directly.
//
// This is relay-wide infrastructure state (like internal/rbl), not tenant-scoped: targets
// come from the environment (MXS_PROBE_* / RELAY_*), results are global, and the API serves
// them behind ScopeRead like every other read.
package smtpprobe

import "time"

// Mode is the transport an endpoint speaks.
type Mode string

const (
	// ModePlain is cleartext SMTP with no TLS (typically port 25 MX, or a plain submission
	// listener). STARTTLS, if advertised, is still attempted for certificate inspection.
	ModePlain Mode = "plain"
	// ModeSTARTTLS is cleartext SMTP that upgrades to TLS via the STARTTLS verb
	// (typically the submission port 587, and opportunistically port 25).
	ModeSTARTTLS Mode = "starttls"
	// ModeImplicitTLS is SMTPS: TLS is negotiated immediately on connect, before the
	// banner (typically port 465).
	ModeImplicitTLS Mode = "implicit_tls"
)

// Endpoint is one probe target.
type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Mode Mode   `json:"mode"`
}

// Addr returns the host:port dial string.
func (e Endpoint) Addr() string { return netJoin(e.Host, e.Port) }

// Label is a stable identifier for the endpoint ("host:port").
func (e Endpoint) Label() string { return e.Addr() }

// Capabilities is the parsed result of an EHLO exchange.
type Capabilities struct {
	Greeting     string   `json:"greeting,omitempty"`    // first EHLO line (server hostname banner)
	STARTTLS     bool     `json:"starttls"`              // STARTTLS advertised
	Auth         bool     `json:"auth"`                  // AUTH advertised
	AuthMechs    []string `json:"auth_mechs,omitempty"`  // e.g. ["PLAIN","LOGIN"]
	Pipelining   bool     `json:"pipelining"`            // PIPELINING advertised
	EightBitMIME bool     `json:"eightbitmime"`          // 8BITMIME advertised
	Enhanced     bool     `json:"enhanced_status_codes"` // ENHANCEDSTATUSCODES advertised
	Size         int64    `json:"size,omitempty"`        // SIZE limit (bytes), 0 if unset/unlimited
	Keywords     []string `json:"keywords,omitempty"`    // every advertised keyword, upper-cased
}

// CertInfo is the inspected state of the endpoint's TLS certificate chain.
type CertInfo struct {
	Subject         string    `json:"subject,omitempty"`
	Issuer          string    `json:"issuer,omitempty"`
	DNSNames        []string  `json:"dns_names,omitempty"`
	NotBefore       time.Time `json:"not_before,omitempty"`
	NotAfter        time.Time `json:"not_after,omitempty"`
	DaysUntilExpiry int       `json:"days_until_expiry"`
	Expiring        bool      `json:"expiring"` // within the warning threshold (or already expired)
	Expired         bool      `json:"expired"`
	ChainValid      bool      `json:"chain_valid"` // a verified chain to a trusted root was built
}

// TLSResult is the outcome of a TLS negotiation on the endpoint.
type TLSResult struct {
	Negotiated bool     `json:"negotiated"`
	Version    string   `json:"version,omitempty"` // e.g. "TLS 1.3"
	Cipher     string   `json:"cipher,omitempty"`
	Cert       CertInfo `json:"cert"`
}

// ProbeResult is the full result of probing one endpoint at one instant. It is JSON-encoded
// directly by the API. It never contains message bodies or subject lines (there is no mail
// content in a probe).
type ProbeResult struct {
	Endpoint Endpoint `json:"endpoint"`
	OK       bool     `json:"ok"`
	// Stage is where the probe ended: "complete" on success, otherwise the failing stage
	// ("connect", "tls_handshake", "banner", "ehlo", "starttls", "response").
	Stage     string `json:"stage"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"` // TCP connect latency

	Banner       string       `json:"banner,omitempty"`
	Capabilities Capabilities `json:"capabilities"`

	STARTTLSOffered bool     `json:"starttls_offered"`
	AuthAdvertised  bool     `json:"auth_advertised"`
	AuthMechs       []string `json:"auth_mechs,omitempty"`

	TLS *TLSResult `json:"tls,omitempty"`

	// Greylisting is set when response-behaviour sampling is enabled and a RCPT command
	// received a 4xx tempfail (the classic greylisting signature).
	Greylisting  bool   `json:"greylisting"`
	ResponseCode int    `json:"response_code,omitempty"` // last sampled MAIL/RCPT reply code
	ResponseText string `json:"response_text,omitempty"`

	ProbedAt time.Time `json:"probed_at"`
}

// CertExpiring reports whether the probe observed a TLS certificate within the warning
// window (or already expired).
func (r ProbeResult) CertExpiring() bool {
	return r.TLS != nil && r.TLS.Negotiated && r.TLS.Cert.Expiring
}
