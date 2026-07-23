package smtpprobe

import (
	"fmt"
	"net"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Signal is the incident-eligible signal derived from a probe result, if any.
type Signal struct {
	EventType   contracts.EventType
	Payload     contracts.ReputationPayload
	Correlation contracts.Correlation
	// Key is a stable dedupe/cooldown key: "<endpoint>|<kind>".
	Key string
}

// DeriveSignal maps a ProbeResult onto an incident-eligible reputation event, or returns
// emit=false when the endpoint is healthy and its certificate is comfortably valid.
//
// It is a pure function (no clock/network) so it is unit-tested directly.
//
// Design note: MX Sentinel's event envelope schema restricts event_type to the four
// families smtp|dns|reputation|ai (schemas/events/envelope.schema.json — a shared,
// immutable file). Rather than introduce an unroutable fifth family, probe signals are
// modelled as reputation.rate_anomaly events so they flow through the existing incidentd
// consumer (REPUTATION stream) and surface as incidents. The custom incident title is
// carried in detail["root_cause"], which incidentd uses verbatim for rate_anomaly events.
func DeriveSignal(r ProbeResult) (Signal, bool) {
	subjectKind := "domain"
	if net.ParseIP(r.Endpoint.Host) != nil {
		subjectKind = "ip"
	}
	corr := contracts.Correlation{Domain: r.Endpoint.Host}
	if subjectKind == "ip" {
		corr.RelayIP = r.Endpoint.Host
	}

	switch {
	case !r.OK:
		title := fmt.Sprintf("SMTP probe failed for %s (stage %s): %s",
			r.Endpoint.Label(), orDash(r.Stage), orDash(r.Error))
		return Signal{
			EventType:   contracts.EventReputationRateAnomaly,
			Correlation: corr,
			Key:         r.Endpoint.Label() + "|probe_failed",
			Payload: contracts.ReputationPayload{
				Signal:      "rate_anomaly",
				SubjectKind: subjectKind,
				Subject:     r.Endpoint.Host,
				Severity:    "critical",
				Source:      "smtpprobe:" + orDash(r.Stage),
				Detail: map[string]any{
					"root_cause": title,
					"probe":      "smtp",
					"endpoint":   r.Endpoint.Label(),
					"host":       r.Endpoint.Host,
					"port":       r.Endpoint.Port,
					"mode":       string(r.Endpoint.Mode),
					"stage":      r.Stage,
					"error":      r.Error,
					"latency_ms": r.LatencyMS,
				},
			},
		}, true

	case r.CertExpiring():
		c := r.TLS.Cert
		severity := "warning"
		if c.Expired || c.DaysUntilExpiry <= 7 {
			severity = "critical"
		}
		var title string
		if c.Expired {
			title = fmt.Sprintf("TLS certificate EXPIRED for %s (%s)", r.Endpoint.Label(), c.CertSummary())
		} else {
			title = fmt.Sprintf("TLS certificate expiring in %dd for %s (%s)",
				c.DaysUntilExpiry, r.Endpoint.Label(), c.CertSummary())
		}
		return Signal{
			EventType:   contracts.EventReputationRateAnomaly,
			Correlation: corr,
			Key:         r.Endpoint.Label() + "|cert_expiring",
			Payload: contracts.ReputationPayload{
				Signal:      "rate_anomaly",
				SubjectKind: subjectKind,
				Subject:     r.Endpoint.Host,
				Severity:    severity,
				Source:      "smtpprobe:tls_expiry",
				Detail: map[string]any{
					"root_cause":        title,
					"probe":             "tls_expiry",
					"endpoint":          r.Endpoint.Label(),
					"host":              r.Endpoint.Host,
					"port":              r.Endpoint.Port,
					"cert_subject":      c.Subject,
					"cert_issuer":       c.Issuer,
					"not_after":         c.NotAfter,
					"days_until_expiry": c.DaysUntilExpiry,
					"expired":           c.Expired,
					"chain_valid":       c.ChainValid,
				},
			},
		}, true
	}

	return Signal{}, false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
