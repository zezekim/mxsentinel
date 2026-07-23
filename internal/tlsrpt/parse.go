// Package tlsrpt implements parsing and ingestion of SMTP TLS Reporting (TLS-RPT, RFC 8460)
// reports. Reports are JSON documents summarizing, per sending MTA and per policy, the
// success/failure counts of TLS negotiation to a domain's MX hosts. The ingest pipeline is
// structurally identical to the DMARC one (internal/dmarc): archive raw -> Postgres pointer
// -> ClickHouse detail rows. Parsing is pure and network-free for unit testing.
package tlsrpt

import (
	"encoding/json"
	"fmt"
	"time"
)

// Report is a parsed TLS-RPT report (RFC 8460 §4.3).
type Report struct {
	OrganizationName string
	DateBegin        time.Time
	DateEnd          time.Time
	ContactInfo      string
	ReportID         string
	Policies         []Policy
}

// Policy is one policy block within a report, with its per-MTA outcomes.
type Policy struct {
	PolicyType     string // "tlsa" | "sts" | "no-policy-found"
	PolicyDomain   string
	PolicyStrings  []string
	MXHosts        []string
	SuccessCount   uint64
	FailureCount   uint64
	FailureDetails []FailureDetail
}

// FailureDetail is one failure aggregation for a policy (RFC 8460 §4.4).
type FailureDetail struct {
	ResultType          string
	SendingMTAIP        string
	ReceivingMXHostname string
	ReceivingMXHelo     string
	ReceivingIP         string
	FailedSessionCount  uint64
	AdditionalInfoURI   string
	FailureReasonCode   string
}

// --- unexported JSON mirror structs (dashed keys per RFC 8460) ---

type jsonReport struct {
	OrganizationName string          `json:"organization-name"`
	DateRange        jsonDateRange   `json:"date-range"`
	ContactInfo      string          `json:"contact-info"`
	ReportID         string          `json:"report-id"`
	Policies         []jsonPolicyObj `json:"policies"`
}

type jsonDateRange struct {
	Start time.Time `json:"start-datetime"`
	End   time.Time `json:"end-datetime"`
}

type jsonPolicyObj struct {
	Policy         jsonPolicy    `json:"policy"`
	Summary        jsonSummary   `json:"summary"`
	FailureDetails []jsonFailure `json:"failure-details"`
}

type jsonPolicy struct {
	PolicyType   string   `json:"policy-type"`
	PolicyString []string `json:"policy-string"`
	PolicyDomain string   `json:"policy-domain"`
	MXHost       []string `json:"mx-host"`
}

type jsonSummary struct {
	SuccessCount uint64 `json:"total-successful-session-count"`
	FailureCount uint64 `json:"total-failure-session-count"`
}

type jsonFailure struct {
	ResultType          string `json:"result-type"`
	SendingMTAIP        string `json:"sending-mta-ip"`
	ReceivingMXHostname string `json:"receiving-mx-hostname"`
	ReceivingMXHelo     string `json:"receiving-mx-helo"`
	ReceivingIP         string `json:"receiving-ip"`
	FailedSessionCount  uint64 `json:"failed-session-count"`
	AdditionalInfoURI   string `json:"additional-information"`
	FailureReasonCode   string `json:"failure-reason-code"`
}

// ParseBytes decodes a TLS-RPT report JSON document. It returns a non-nil error for
// malformed JSON or a report missing its report-id.
func ParseBytes(b []byte) (*Report, error) {
	var raw jsonReport
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("tlsrpt: JSON decode error: %w", err)
	}
	rep := &Report{
		OrganizationName: raw.OrganizationName,
		DateBegin:        raw.DateRange.Start.UTC(),
		DateEnd:          raw.DateRange.End.UTC(),
		ContactInfo:      raw.ContactInfo,
		ReportID:         raw.ReportID,
	}
	for _, p := range raw.Policies {
		pol := Policy{
			PolicyType:    p.Policy.PolicyType,
			PolicyDomain:  p.Policy.PolicyDomain,
			PolicyStrings: p.Policy.PolicyString,
			MXHosts:       p.Policy.MXHost,
			SuccessCount:  p.Summary.SuccessCount,
			FailureCount:  p.Summary.FailureCount,
		}
		for _, f := range p.FailureDetails {
			pol.FailureDetails = append(pol.FailureDetails, FailureDetail(f))
		}
		rep.Policies = append(rep.Policies, pol)
	}
	if rep.ReportID == "" {
		return nil, fmt.Errorf("tlsrpt: report missing report-id")
	}
	return rep, nil
}

// PrimaryDomain returns the policy-domain the report is about (the first policy's domain),
// used to resolve the owning tenant. Empty if the report has no policies with a domain.
func (r *Report) PrimaryDomain() string {
	for _, p := range r.Policies {
		if p.PolicyDomain != "" {
			return p.PolicyDomain
		}
	}
	return ""
}

// Totals returns the summed success and failure session counts across all policies.
func (r *Report) Totals() (success, failure uint64) {
	for _, p := range r.Policies {
		success += p.SuccessCount
		failure += p.FailureCount
	}
	return success, failure
}
