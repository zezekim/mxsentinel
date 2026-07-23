package smtpprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"time"
)

// Dialer abstracts establishing a TCP connection. The production implementation is a
// *net.Dialer; tests inject a fake that returns an in-memory net.Conn.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// TLSState is the subset of a completed TLS handshake the prober records.
type TLSState struct {
	Version          uint16
	CipherSuite      uint16
	PeerCertificates []*x509.Certificate
	VerifiedChains   [][]*x509.Certificate
}

// TLSHandshaker upgrades (or opens) a TLS session over an existing net.Conn and reports the
// handshake state. For implicit TLS (:465) it is called on the freshly-dialled conn; for
// STARTTLS it is called after the 220 response to the STARTTLS verb. serverName is used for
// SNI and certificate verification. The returned net.Conn carries subsequent SMTP traffic.
//
// Tests inject a fake that returns canned certificates so no real TLS/PKI is exercised.
type TLSHandshaker interface {
	Handshake(ctx context.Context, conn net.Conn, serverName string) (net.Conn, TLSState, error)
}

// Prober probes SMTP endpoints. All external dependencies (dialing, TLS, clock) are
// injected so the whole flow is testable without a network.
type Prober struct {
	Dialer            Dialer
	TLS               TLSHandshaker
	Now               func() time.Time
	EHLOName          string        // name to send in EHLO/HELO (defaults to "mxsentinel.probe")
	ConnectTimeout    time.Duration // TCP dial timeout (default 10s)
	CommandTimeout    time.Duration // per-command read/write deadline (default 10s)
	CertWarnThreshold time.Duration // cert-expiry warning lead time (default 14d)
	CheckResponse     bool          // sample MAIL/RCPT to detect greylisting/tempfail
}

// NewProber returns a Prober wired to the real network (net.Dialer + crypto/tls). insecure
// controls whether certificate verification failures abort the TLS handshake: when true the
// handshake still completes (so we can inspect an expired/untrusted cert) and ChainValid is
// derived from whether verification would have succeeded.
func NewProber(insecure bool) *Prober {
	return &Prober{
		Dialer:            &net.Dialer{},
		TLS:               &stdTLSHandshaker{insecureSkipVerify: insecure},
		Now:               time.Now,
		EHLOName:          "mxsentinel.probe",
		ConnectTimeout:    10 * time.Second,
		CommandTimeout:    10 * time.Second,
		CertWarnThreshold: DefaultCertWarnThreshold,
	}
}

func (p *Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Prober) commandTimeout() time.Duration {
	if p.CommandTimeout > 0 {
		return p.CommandTimeout
	}
	return 10 * time.Second
}

// Probe runs the full synthetic check against one endpoint and returns a ProbeResult. It
// never returns an error: a failed probe is a ProbeResult with OK=false and a populated
// Stage/Error, because a failed probe is itself the signal.
func (p *Prober) Probe(ctx context.Context, ep Endpoint) ProbeResult {
	res := ProbeResult{Endpoint: ep, ProbedAt: p.now(), Stage: "connect"}

	// --- TCP connect + latency ------------------------------------------------
	dialCtx := ctx
	if p.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, p.ConnectTimeout)
		defer cancel()
	}
	start := p.now()
	conn, err := p.Dialer.DialContext(dialCtx, "tcp", ep.Addr())
	res.LatencyMS = p.now().Sub(start).Milliseconds()
	if err != nil {
		res.Error = fmt.Sprintf("connect %s: %v", ep.Addr(), err)
		return res
	}
	defer conn.Close()

	// --- implicit TLS (:465) upgrades before the banner -----------------------
	if ep.Mode == ModeImplicitTLS {
		res.Stage = "tls_handshake"
		tconn, ok := p.doTLS(ctx, conn, ep, &res)
		if !ok {
			return res
		}
		conn = tconn
	}

	tp := textproto.NewConn(conn)

	// --- banner ---------------------------------------------------------------
	res.Stage = "banner"
	_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
	code, msg, err := tp.ReadResponse(0)
	if err != nil {
		res.Error = fmt.Sprintf("read banner: %v", err)
		return res
	}
	if code < 200 || code >= 400 {
		res.Error = fmt.Sprintf("banner %d: %s", code, firstLine(msg))
		return res
	}
	res.Banner = firstLine(msg)

	// --- EHLO -----------------------------------------------------------------
	res.Stage = "ehlo"
	caps, err := p.ehlo(conn, tp)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	p.applyCaps(&res, caps)

	// --- STARTTLS -------------------------------------------------------------
	if ep.Mode == ModeSTARTTLS || (ep.Mode == ModePlain && caps.STARTTLS) {
		if caps.STARTTLS {
			res.Stage = "starttls"
			_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
			scode, smsg, serr := cmd(tp, "STARTTLS")
			if serr != nil || scode != 220 {
				// A plain-mode endpoint that merely advertised STARTTLS is not required to
				// complete it for the probe to pass; a starttls-mode endpoint is.
				if ep.Mode == ModeSTARTTLS {
					if serr != nil {
						res.Error = fmt.Sprintf("STARTTLS: %v", serr)
					} else {
						res.Error = fmt.Sprintf("STARTTLS %d: %s", scode, firstLine(smsg))
					}
					return res
				}
			} else {
				tconn, ok := p.doTLS(ctx, conn, ep, &res)
				if !ok {
					return res
				}
				conn = tconn
				tp = textproto.NewConn(conn)
				// Re-EHLO over TLS: many servers only advertise AUTH after the session is
				// encrypted, so this reflects the real authenticated-submission capability.
				res.Stage = "ehlo"
				if caps2, err := p.ehlo(conn, tp); err == nil {
					p.applyCaps(&res, caps2)
				}
			}
		} else if ep.Mode == ModeSTARTTLS {
			res.Error = "endpoint did not advertise STARTTLS"
			return res
		}
	}

	// --- optional response-behaviour / greylisting sampling -------------------
	if p.CheckResponse {
		res.Stage = "response"
		p.sampleResponse(conn, tp, ep, &res)
	}

	// --- QUIT (best effort) ---------------------------------------------------
	_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
	_, _, _ = cmd(tp, "QUIT")

	res.OK = true
	res.Stage = "complete"
	return res
}

// ehlo sends EHLO and returns the parsed capabilities. Falls back to HELO on a hard EHLO
// rejection so the probe still completes against ancient servers.
func (p *Prober) ehlo(conn net.Conn, tp *textproto.Conn) (Capabilities, error) {
	_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
	code, msg, err := cmd(tp, "EHLO "+p.ehloName())
	if err != nil {
		return Capabilities{}, fmt.Errorf("EHLO: %w", err)
	}
	if code != 250 {
		// Try HELO as a fallback.
		_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
		hcode, hmsg, herr := cmd(tp, "HELO "+p.ehloName())
		if herr != nil {
			return Capabilities{}, fmt.Errorf("HELO: %w", herr)
		}
		if hcode != 250 {
			return Capabilities{}, fmt.Errorf("EHLO %d / HELO %d: %s", code, hcode, firstLine(hmsg))
		}
		return ParseEHLO(splitReplyLines(hmsg)), nil
	}
	return ParseEHLO(splitReplyLines(msg)), nil
}

func (p *Prober) applyCaps(res *ProbeResult, caps Capabilities) {
	res.Capabilities = caps
	res.STARTTLSOffered = res.STARTTLSOffered || caps.STARTTLS
	if caps.Auth {
		res.AuthAdvertised = true
		res.AuthMechs = caps.AuthMechs
	}
}

// doTLS performs the handshake via the injected TLSHandshaker and records the TLS result on
// res. It returns the upgraded conn and ok=false (with res.Error set) on failure.
func (p *Prober) doTLS(ctx context.Context, conn net.Conn, ep Endpoint, res *ProbeResult) (net.Conn, bool) {
	tconn, state, err := p.TLS.Handshake(ctx, conn, ep.Host)
	if err != nil {
		res.Stage = "tls_handshake"
		res.Error = fmt.Sprintf("TLS handshake %s: %v", ep.Addr(), err)
		return nil, false
	}
	warn := p.CertWarnThreshold
	if warn <= 0 {
		warn = DefaultCertWarnThreshold
	}
	res.TLS = &TLSResult{
		Negotiated: true,
		Version:    tlsVersionName(state.Version),
		Cipher:     tls.CipherSuiteName(state.CipherSuite),
		Cert:       InspectCerts(state.PeerCertificates, state.VerifiedChains, p.now(), warn),
	}
	return tconn, true
}

// sampleResponse issues a lightweight MAIL FROM / RCPT TO transaction (then RSET) to observe
// the endpoint's response behaviour — chiefly to detect greylisting (a 4xx tempfail on
// RCPT). It never sends DATA, so no message is ever transmitted. Errors are non-fatal.
func (p *Prober) sampleResponse(conn net.Conn, tp *textproto.Conn, ep Endpoint, res *ProbeResult) {
	_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
	if _, _, err := cmd(tp, "MAIL FROM:<probe@"+p.ehloName()+">"); err != nil {
		return
	}
	_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
	code, msg, err := cmd(tp, "RCPT TO:<postmaster@"+ep.Host+">")
	if err != nil {
		return
	}
	res.ResponseCode = code
	res.ResponseText = firstLine(msg)
	if code >= 400 && code < 500 {
		res.Greylisting = true
	}
	_ = conn.SetDeadline(p.now().Add(p.commandTimeout()))
	_, _, _ = cmd(tp, "RSET")
}

func (p *Prober) ehloName() string {
	if strings.TrimSpace(p.EHLOName) != "" {
		return p.EHLOName
	}
	return "mxsentinel.probe"
}

// cmd writes an SMTP command line and reads the (possibly multiline) response.
func cmd(tp *textproto.Conn, line string) (int, string, error) {
	id, err := tp.Cmd("%s", line)
	if err != nil {
		return 0, "", err
	}
	tp.StartResponse(id)
	defer tp.EndResponse(id)
	return tp.ReadResponse(0)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	case 0:
		return ""
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// NewTLSHandshaker returns the production TLSHandshaker backed by crypto/tls. insecure has
// the same meaning as in NewProber: when true, chain-validity reporting is skipped.
func NewTLSHandshaker(insecure bool) TLSHandshaker {
	return &stdTLSHandshaker{insecureSkipVerify: insecure}
}

// stdTLSHandshaker is the production TLSHandshaker backed by crypto/tls.
type stdTLSHandshaker struct {
	insecureSkipVerify bool
}

func (h *stdTLSHandshaker) Handshake(ctx context.Context, conn net.Conn, serverName string) (net.Conn, TLSState, error) {
	// We always complete the handshake without aborting on verification errors so an
	// expired/untrusted certificate can still be inspected and reported. Trust is evaluated
	// separately below and surfaced as ChainValid.
	tconn := tls.Client(conn, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true, //nolint:gosec // verification is re-done explicitly for reporting
		MinVersion:         tls.VersionTLS10,
	})
	if dl, ok := ctx.Deadline(); ok {
		_ = tconn.SetDeadline(dl)
	}
	if err := tconn.HandshakeContext(ctx); err != nil {
		return nil, TLSState{}, err
	}
	cs := tconn.ConnectionState()
	st := TLSState{
		Version:          cs.Version,
		CipherSuite:      cs.CipherSuite,
		PeerCertificates: cs.PeerCertificates,
	}
	// Re-verify against the system roots (or SNI name) purely to populate ChainValid; a
	// failure here does not fail the probe (the connection is fine for inspection).
	if !h.insecureSkipVerify && len(cs.PeerCertificates) > 0 {
		roots, _ := x509.SystemCertPool()
		opts := x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: x509.NewCertPool()}
		for _, ic := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(ic)
		}
		if chains, verr := cs.PeerCertificates[0].Verify(opts); verr == nil {
			st.VerifiedChains = chains
		}
	}
	return tconn, st, nil
}
