package smtpprobe

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// --- fakes ------------------------------------------------------------------

type fakeDialer struct {
	conn net.Conn
	err  error
}

func (f *fakeDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

type fakeTLS struct {
	certs  []*x509.Certificate
	chains [][]*x509.Certificate
	err    error
}

func (f *fakeTLS) Handshake(_ context.Context, conn net.Conn, _ string) (net.Conn, TLSState, error) {
	if f.err != nil {
		return nil, TLSState{}, f.err
	}
	// Passthrough: tests run "TLS" over the same in-memory pipe in cleartext.
	return conn, TLSState{
		Version:          tls.VersionTLS13,
		CipherSuite:      tls.TLS_AES_128_GCM_SHA256,
		PeerCertificates: f.certs,
		VerifiedChains:   f.chains,
	}, nil
}

type fakeSrv struct {
	starttls bool
	auth     []string
	size     int64
	rcptCode int // 0 => 250
}

func (c fakeSrv) ehloResponse() string {
	lines := []string{"mail.test greets you", "PIPELINING", "8BITMIME"}
	if c.size > 0 {
		lines = append(lines, fmt.Sprintf("SIZE %d", c.size))
	}
	if c.starttls {
		lines = append(lines, "STARTTLS")
	}
	if len(c.auth) > 0 {
		lines = append(lines, "AUTH "+strings.Join(c.auth, " "))
	}
	var b strings.Builder
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		fmt.Fprintf(&b, "250%s%s\r\n", sep, l)
	}
	return b.String()
}

func fakeSMTPServer(conn net.Conn, cfg fakeSrv) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	write := func(s string) bool {
		_, err := conn.Write([]byte(s))
		return err == nil
	}
	if !write("220 mail.test ESMTP\r\n") {
		return
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "EHLO", "HELO":
			write(cfg.ehloResponse())
		case "STARTTLS":
			write("220 2.0.0 Ready to start TLS\r\n")
		case "MAIL":
			write("250 2.1.0 Ok\r\n")
		case "RCPT":
			code := cfg.rcptCode
			if code == 0 {
				code = 250
			}
			write(fmt.Sprintf("%d 2.1.5 Ok\r\n", code))
		case "RSET":
			write("250 2.0.0 Ok\r\n")
		case "QUIT":
			write("221 2.0.0 Bye\r\n")
			return
		default:
			write("250 2.0.0 Ok\r\n")
		}
	}
}

func validCerts(daysOut int) ([]*x509.Certificate, [][]*x509.Certificate) {
	c := leaf("mail.test", "Test CA", time.Now().Add(time.Duration(daysOut)*24*time.Hour))
	return []*x509.Certificate{c}, [][]*x509.Certificate{{c}}
}

func runProbe(ep Endpoint, cfg fakeSrv, ftls TLSHandshaker, checkResponse bool) ProbeResult {
	cconn, sconn := net.Pipe()
	go fakeSMTPServer(sconn, cfg)
	p := &Prober{
		Dialer:            &fakeDialer{conn: cconn},
		TLS:               ftls,
		Now:               time.Now,
		EHLOName:          "probe.test",
		ConnectTimeout:    2 * time.Second,
		CommandTimeout:    2 * time.Second,
		CertWarnThreshold: 14 * 24 * time.Hour,
		CheckResponse:     checkResponse,
	}
	return p.Probe(context.Background(), ep)
}

// --- tests ------------------------------------------------------------------

func TestProbeSTARTTLSHappyPath(t *testing.T) {
	certs, chains := validCerts(60)
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 587, Mode: ModeSTARTTLS},
		fakeSrv{starttls: true, auth: []string{"PLAIN", "LOGIN"}, size: 10 << 20},
		&fakeTLS{certs: certs, chains: chains},
		false,
	)
	if !res.OK {
		t.Fatalf("probe not OK: stage=%s err=%s", res.Stage, res.Error)
	}
	if res.Stage != "complete" {
		t.Errorf("stage = %q", res.Stage)
	}
	if !res.STARTTLSOffered {
		t.Errorf("STARTTLS should be offered")
	}
	if res.TLS == nil || !res.TLS.Negotiated {
		t.Fatalf("expected TLS negotiated")
	}
	if !res.TLS.Cert.ChainValid {
		t.Errorf("chain should be valid")
	}
	if res.TLS.Cert.Expiring {
		t.Errorf("60d cert should not be expiring")
	}
	if !res.AuthAdvertised || len(res.AuthMechs) != 2 {
		t.Errorf("auth = %v %v", res.AuthAdvertised, res.AuthMechs)
	}
	if res.Banner == "" {
		t.Errorf("banner should be captured")
	}
	if res.Capabilities.Size != 10<<20 {
		t.Errorf("size = %d", res.Capabilities.Size)
	}
}

func TestProbePlainOpportunisticSTARTTLS(t *testing.T) {
	certs, chains := validCerts(30)
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 25, Mode: ModePlain},
		fakeSrv{starttls: true},
		&fakeTLS{certs: certs, chains: chains},
		false,
	)
	if !res.OK {
		t.Fatalf("probe not OK: %s", res.Error)
	}
	if res.TLS == nil || !res.TLS.Negotiated {
		t.Fatalf("plain-mode probe should opportunistically STARTTLS when offered")
	}
}

func TestProbeImplicitTLS(t *testing.T) {
	certs, chains := validCerts(45)
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 465, Mode: ModeImplicitTLS},
		fakeSrv{auth: []string{"PLAIN"}},
		&fakeTLS{certs: certs, chains: chains},
		false,
	)
	if !res.OK {
		t.Fatalf("probe not OK: stage=%s err=%s", res.Stage, res.Error)
	}
	if res.TLS == nil || !res.TLS.Negotiated {
		t.Fatalf("implicit TLS should negotiate before banner")
	}
}

func TestProbeConnectFailure(t *testing.T) {
	p := &Prober{
		Dialer: &fakeDialer{err: errors.New("connection refused")},
		TLS:    &fakeTLS{},
		Now:    time.Now,
	}
	res := p.Probe(context.Background(), Endpoint{Host: "down.test", Port: 587, Mode: ModeSTARTTLS})
	if res.OK {
		t.Fatal("expected failure")
	}
	if res.Stage != "connect" {
		t.Errorf("stage = %q, want connect", res.Stage)
	}
	if !strings.Contains(res.Error, "connection refused") {
		t.Errorf("error = %q", res.Error)
	}
}

func TestProbeExpiringCert(t *testing.T) {
	certs, chains := validCerts(5) // within 14d warn window
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 465, Mode: ModeImplicitTLS},
		fakeSrv{},
		&fakeTLS{certs: certs, chains: chains},
		false,
	)
	if !res.OK {
		t.Fatalf("probe not OK: %s", res.Error)
	}
	if !res.CertExpiring() {
		t.Fatalf("5d cert should be flagged expiring")
	}
	sig, ok := DeriveSignal(res)
	if !ok || sig.Key != "mail.test:465|cert_expiring" {
		t.Fatalf("expected cert_expiring signal, got ok=%v key=%q", ok, sig.Key)
	}
}

func TestProbeTLSHandshakeFailure(t *testing.T) {
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 587, Mode: ModeSTARTTLS},
		fakeSrv{starttls: true},
		&fakeTLS{err: errors.New("handshake timeout")},
		false,
	)
	if res.OK {
		t.Fatal("expected failure on TLS handshake error")
	}
	if res.Stage != "tls_handshake" {
		t.Errorf("stage = %q, want tls_handshake", res.Stage)
	}
}

func TestProbeSTARTTLSNotOffered(t *testing.T) {
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 587, Mode: ModeSTARTTLS},
		fakeSrv{starttls: false},
		&fakeTLS{},
		false,
	)
	if res.OK {
		t.Fatal("starttls-mode probe must fail when STARTTLS is not advertised")
	}
	if !strings.Contains(res.Error, "advertise") {
		t.Errorf("error = %q", res.Error)
	}
}

func TestProbeGreylistingDetected(t *testing.T) {
	certs, chains := validCerts(30)
	res := runProbe(
		Endpoint{Host: "mail.test", Port: 587, Mode: ModeSTARTTLS},
		fakeSrv{starttls: true, rcptCode: 451},
		&fakeTLS{certs: certs, chains: chains},
		true, // enable response sampling
	)
	if !res.OK {
		t.Fatalf("probe not OK: %s", res.Error)
	}
	if !res.Greylisting {
		t.Errorf("expected greylisting detection on 451 RCPT")
	}
	if res.ResponseCode != 451 {
		t.Errorf("response code = %d, want 451", res.ResponseCode)
	}
}
