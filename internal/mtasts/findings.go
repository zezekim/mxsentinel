package mtasts

import "github.com/zezekim/mxsentinel/pkg/contracts"

// Severity levels (match the dns_finding severity enum; findings are emitted as
// contracts.DNSFinding so they ride the existing dns.validation_failed event).
const (
	SevInfo     = "info"
	SevWarning  = "warning"
	SevCritical = "critical"
)

// CatMTASTS is the finding category (matches the dns_event category enum, which already
// includes "mta_sts").
const CatMTASTS = "mta_sts"

// Stable machine codes for MTA-STS findings. The dashboard and alerting key off these.
const (
	CodeTXTMissing        = "MTASTS_TXT_MISSING"
	CodeTXTInvalid        = "MTASTS_TXT_INVALID"
	CodePolicyUnreachable = "MTASTS_POLICY_UNREACHABLE"
	CodePolicyInvalid     = "MTASTS_POLICY_INVALID"
	CodePolicyIDMismatch  = "MTASTS_POLICY_ID_MISMATCH"
	CodeModeNotEnforced   = "MTASTS_MODE_NOT_ENFORCED"
	CodeNoMXHosts         = "MTASTS_NO_MX_HOSTS"
	CodeCertExpired       = "MTASTS_CERT_EXPIRED"
	CodeCertExpiringSoon  = "MTASTS_CERT_EXPIRING_SOON"
	CodeCertInvalid       = "MTASTS_CERT_INVALID"
	CodeCertUnreachable   = "MTASTS_CERT_UNREACHABLE"
)

// finding builds a contracts.DNSFinding in the mta_sts category. detail may be nil.
func finding(severity, code, message string, detail map[string]any) contracts.DNSFinding {
	return contracts.DNSFinding{
		Category: CatMTASTS,
		Severity: severity,
		Code:     code,
		Message:  message,
		Detail:   detail,
	}
}

// worstSeverity returns the highest severity among findings ("" if none).
func worstSeverity(fs []contracts.DNSFinding) string {
	rank := map[string]int{SevInfo: 1, SevWarning: 2, SevCritical: 3}
	worst := ""
	for _, f := range fs {
		if rank[f.Severity] > rank[worst] {
			worst = f.Severity
		}
	}
	return worst
}

// HasAlertable reports whether any finding is warning-or-worse — the trigger for emitting
// a dns.validation_failed-style signal.
func HasAlertable(fs []contracts.DNSFinding) bool {
	for _, f := range fs {
		if f.Severity == SevWarning || f.Severity == SevCritical {
			return true
		}
	}
	return false
}
