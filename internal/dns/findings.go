package dns

import "github.com/zezekim/mxsentinel/pkg/contracts"

// Severity levels (match the dns_finding_sev enum in schemas/postgres).
const (
	SevInfo     = "info"
	SevWarning  = "warning"
	SevCritical = "critical"
)

// Categories (match the dns_category enum).
const (
	CatSPF    = "spf"
	CatDKIM   = "dkim"
	CatDMARC  = "dmarc"
	CatMX     = "mx"
	CatMTASTS = "mta_sts"
	CatTLSRPT = "tls_rpt"
	CatBIMI   = "bimi"
)

// Stable machine codes for findings. The dashboard and alerting key off these.
const (
	CodeSPFMissing      = "SPF_MISSING"
	CodeSPFMultiple     = "SPF_MULTIPLE_RECORDS"
	CodeSPFLookupLimit  = "SPF_LOOKUP_LIMIT"
	CodeSPFRecursive    = "SPF_RECURSIVE_INCLUDE"
	CodeSPFPermError    = "SPF_PERMERROR"
	CodeSPFPlusAll      = "SPF_PLUS_ALL"
	CodeSPFNoAll        = "SPF_NO_ALL_MECHANISM"
	CodeSPFLookupFailed = "SPF_LOOKUP_FAILED"

	CodeDKIMMissingSelector = "DKIM_MISSING_SELECTOR"
	CodeDKIMRevoked         = "DKIM_REVOKED"
	CodeDKIMWeakKey         = "DKIM_WEAK_KEY"
	CodeDKIMTesting         = "DKIM_TESTING_MODE"
	CodeDKIMInvalid         = "DKIM_INVALID_RECORD"

	CodeDMARCMissing    = "DMARC_MISSING"
	CodeDMARCPolicyNone = "DMARC_POLICY_NONE"
	CodeDMARCMissingRUA = "DMARC_MISSING_RUA"
	CodeDMARCPctPartial = "DMARC_PCT_PARTIAL"
	CodeDMARCInvalid    = "DMARC_INVALID_RECORD"

	CodeMXMissing = "MX_MISSING"
)

// finding builds a contracts.DNSFinding. detail may be nil.
func finding(category, severity, code, message string, detail map[string]any) contracts.DNSFinding {
	return contracts.DNSFinding{
		Category: category,
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
