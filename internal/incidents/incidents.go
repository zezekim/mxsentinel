// Package incidents maps intelligence-layer events (reputation.*, dns.validation_failed)
// into persisted Incident records. The mapping is pure so it can be tested without a bus
// or database; cmd/incidentd wires it to JetStream + Postgres.
package incidents

import (
	"encoding/json"
	"fmt"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// FromEnvelope converts an event into an IncidentInput. ok is false for event types that
// do not represent an incident.
func FromEnvelope(ev *contracts.Envelope) (pgstore.IncidentInput, bool) {
	base := pgstore.IncidentInput{TenantID: ev.TenantID, SourceEventID: ev.EventID}

	switch ev.EventType {
	case contracts.EventReputationRateAnomaly, contracts.EventReputationBlacklistHit, contracts.EventReputationComplaintSpike:
		var p contracts.ReputationPayload
		if err := ev.DecodePayload(&p); err != nil {
			return base, false
		}
		return reputationIncident(ev, base, p), true

	case contracts.EventDNSValidationFailed:
		var p contracts.DNSPayload
		if err := ev.DecodePayload(&p); err != nil {
			return base, false
		}
		return dnsIncident(ev, base, p), true

	default:
		return base, false
	}
}

func reputationIncident(ev *contracts.Envelope, in pgstore.IncidentInput, p contracts.ReputationPayload) pgstore.IncidentInput {
	in.Severity = normSeverity(p.Severity)
	in.Subject = p.Subject
	if p.SubjectKind == "domain" {
		in.Domain = p.Subject
	} else {
		in.Domain = ev.Correlation.Domain
	}

	switch ev.EventType {
	case contracts.EventReputationRateAnomaly:
		in.Kind = "rejection_spike"
		in.Title = detailString(p.Detail, "root_cause")
		if in.Title == "" {
			in.Title = fmt.Sprintf("Rejection spike for %s at %s", orUnknown(p.Subject), orUnknown(p.Source))
		}
		in.Confidence = detailFloat(p.Detail, "confidence")
	case contracts.EventReputationBlacklistHit:
		in.Kind = "blacklist"
		in.Title = fmt.Sprintf("Blacklist hit: %s on %s", orUnknown(p.Subject), orUnknown(p.Source))
	default: // complaint_spike
		in.Kind = "other"
		in.Title = fmt.Sprintf("Complaint spike for %s at %s", orUnknown(p.Subject), orUnknown(p.Source))
	}

	in.Detail = marshalDetail(mergeDetail(p.Detail, map[string]any{
		"signal": p.Signal, "source": p.Source,
		"observed_value": p.ObservedValue, "window_secs": p.WindowSecs,
	}))
	return in
}

func dnsIncident(ev *contracts.Envelope, in pgstore.IncidentInput, p contracts.DNSPayload) pgstore.IncidentInput {
	in.Kind = "dns_validation"
	in.Domain = p.Domain
	in.Subject = p.Domain
	in.Severity = worstFindingSeverity(p.Findings)
	in.Title = fmt.Sprintf("DNS validation issues for %s (%d finding(s))", orUnknown(p.Domain), len(p.Findings))

	codes := make([]string, 0, len(p.Findings))
	for _, f := range p.Findings {
		codes = append(codes, f.Code)
	}
	in.Detail = marshalDetail(map[string]any{
		"snapshot_id": p.SnapshotID, "finding_count": len(p.Findings), "codes": codes,
	})
	return in
}

// --- helpers ---------------------------------------------------------------

func normSeverity(s string) string {
	switch s {
	case "info", "warning", "critical":
		return s
	default:
		return "warning"
	}
}

func worstFindingSeverity(fs []contracts.DNSFinding) string {
	rank := map[string]int{"info": 1, "warning": 2, "critical": 3}
	worst := "warning" // a validation_failed event always implies a problem
	worstN := 0
	for _, f := range fs {
		if rank[f.Severity] > worstN {
			worst, worstN = f.Severity, rank[f.Severity]
		}
	}
	return worst
}

func detailString(d map[string]any, key string) string {
	if d == nil {
		return ""
	}
	if v, ok := d[key].(string); ok {
		return v
	}
	return ""
}

func detailFloat(d map[string]any, key string) *float64 {
	if d == nil {
		return nil
	}
	if v, ok := d[key].(float64); ok {
		return &v
	}
	return nil
}

func mergeDetail(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

func marshalDetail(d map[string]any) json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
