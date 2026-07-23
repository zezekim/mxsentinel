package smtpprobe

import (
	"testing"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func TestDeriveSignalHealthy(t *testing.T) {
	r := ProbeResult{
		Endpoint: Endpoint{Host: "relay.example.com", Port: 587, Mode: ModeSTARTTLS},
		OK:       true,
		TLS:      &TLSResult{Negotiated: true, Cert: CertInfo{DaysUntilExpiry: 60}},
	}
	if _, ok := DeriveSignal(r); ok {
		t.Fatalf("healthy probe should not emit a signal")
	}
}

func TestDeriveSignalFailure(t *testing.T) {
	r := ProbeResult{
		Endpoint:  Endpoint{Host: "relay.example.com", Port: 587, Mode: ModeSTARTTLS},
		OK:        false,
		Stage:     "connect",
		Error:     "connection refused",
		LatencyMS: 5,
	}
	sig, ok := DeriveSignal(r)
	if !ok {
		t.Fatal("failed probe should emit a signal")
	}
	if sig.EventType != contracts.EventReputationRateAnomaly {
		t.Errorf("event type = %q", sig.EventType)
	}
	if sig.Payload.Severity != "critical" {
		t.Errorf("severity = %q, want critical", sig.Payload.Severity)
	}
	if sig.Payload.SubjectKind != "domain" {
		t.Errorf("subject_kind = %q, want domain", sig.Payload.SubjectKind)
	}
	if sig.Key != "relay.example.com:587|probe_failed" {
		t.Errorf("key = %q", sig.Key)
	}
	if sig.Payload.Detail["root_cause"] == "" || sig.Payload.Detail["root_cause"] == nil {
		t.Errorf("expected root_cause in detail")
	}
	if sig.Correlation.Domain != "relay.example.com" {
		t.Errorf("correlation domain = %q", sig.Correlation.Domain)
	}
}

func TestDeriveSignalFailureIPSubject(t *testing.T) {
	r := ProbeResult{Endpoint: Endpoint{Host: "203.0.113.9", Port: 25, Mode: ModePlain}, OK: false, Stage: "banner"}
	sig, ok := DeriveSignal(r)
	if !ok {
		t.Fatal("want signal")
	}
	if sig.Payload.SubjectKind != "ip" {
		t.Errorf("subject_kind = %q, want ip", sig.Payload.SubjectKind)
	}
	if sig.Correlation.RelayIP != "203.0.113.9" {
		t.Errorf("correlation relay_ip = %q", sig.Correlation.RelayIP)
	}
}

func TestDeriveSignalCertExpiring(t *testing.T) {
	r := ProbeResult{
		Endpoint: Endpoint{Host: "relay.example.com", Port: 465, Mode: ModeImplicitTLS},
		OK:       true,
		TLS: &TLSResult{
			Negotiated: true,
			Cert: CertInfo{
				Subject: "relay.example.com", Issuer: "R3",
				NotAfter: time.Now().Add(9 * 24 * time.Hour), DaysUntilExpiry: 9, Expiring: true,
			},
		},
	}
	sig, ok := DeriveSignal(r)
	if !ok {
		t.Fatal("expiring cert should emit a signal")
	}
	if sig.Key != "relay.example.com:465|cert_expiring" {
		t.Errorf("key = %q", sig.Key)
	}
	// 9 days > 7 → warning, not critical.
	if sig.Payload.Severity != "warning" {
		t.Errorf("severity = %q, want warning", sig.Payload.Severity)
	}
	if sig.Payload.Source != "smtpprobe:tls_expiry" {
		t.Errorf("source = %q", sig.Payload.Source)
	}
}

func TestDeriveSignalCertExpiredCritical(t *testing.T) {
	r := ProbeResult{
		Endpoint: Endpoint{Host: "relay.example.com", Port: 465, Mode: ModeImplicitTLS},
		OK:       true,
		TLS: &TLSResult{
			Negotiated: true,
			Cert:       CertInfo{DaysUntilExpiry: -1, Expiring: true, Expired: true},
		},
	}
	sig, ok := DeriveSignal(r)
	if !ok {
		t.Fatal("want signal")
	}
	if sig.Payload.Severity != "critical" {
		t.Errorf("severity = %q, want critical for expired cert", sig.Payload.Severity)
	}
}

// TestDeriveSignalPayloadValidatesAgainstReputationSchema guards the design decision to
// route probe signals through the reputation family: the emitted payload must satisfy the
// (immutable) reputation event schema so bus.Publish will accept it.
func TestDeriveSignalPayloadShape(t *testing.T) {
	r := ProbeResult{Endpoint: Endpoint{Host: "h.example.com", Port: 587, Mode: ModeSTARTTLS}, OK: false, Stage: "ehlo", Error: "x"}
	sig, _ := DeriveSignal(r)
	// Required reputation fields present.
	if sig.Payload.Signal == "" || sig.Payload.Subject == "" || sig.Payload.SubjectKind == "" || sig.Payload.Severity == "" {
		t.Fatalf("payload missing required reputation fields: %+v", sig.Payload)
	}
	switch sig.Payload.Signal {
	case "blacklist_hit", "complaint_spike", "rate_anomaly":
	default:
		t.Fatalf("signal %q is not a valid reputation signal enum value", sig.Payload.Signal)
	}
	switch sig.Payload.SubjectKind {
	case "ip", "ip_pool", "domain":
	default:
		t.Fatalf("subject_kind %q is not a valid reputation enum value", sig.Payload.SubjectKind)
	}
}
