package dns

import (
	"context"
	"strings"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// evaluateDMARC resolves and validates the DMARC policy at _dmarc.<domain>.
func evaluateDMARC(ctx context.Context, r Resolver, domain string) (string, []contracts.DNSFinding) {
	var findings []contracts.DNSFinding
	name := "_dmarc." + domain

	txts, err := r.TXT(ctx, name)
	if err != nil {
		return "", append(findings, finding(CatDMARC, SevWarning, CodeDMARCInvalid,
			"DMARC lookup failed: "+err.Error(), nil))
	}

	var record string
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=dmarc1") {
			record = t
			break
		}
	}
	if record == "" {
		return "", append(findings, finding(CatDMARC, SevCritical, CodeDMARCMissing,
			"No DMARC record published for "+domain, nil))
	}

	tags := parseTags(record)

	p := strings.ToLower(tags["p"])
	switch p {
	case "":
		findings = append(findings, finding(CatDMARC, SevWarning, CodeDMARCInvalid,
			"DMARC record is missing the required policy (p=) tag", nil))
	case "none":
		findings = append(findings, finding(CatDMARC, SevWarning, CodeDMARCPolicyNone,
			"DMARC policy is p=none (monitoring only, not enforced)", nil))
	case "quarantine", "reject":
		// enforcing — good
	default:
		findings = append(findings, finding(CatDMARC, SevWarning, CodeDMARCInvalid,
			"DMARC policy value is invalid: p="+p, map[string]any{"p": p}))
	}

	if strings.TrimSpace(tags["rua"]) == "" {
		findings = append(findings, finding(CatDMARC, SevWarning, CodeDMARCMissingRUA,
			"DMARC has no rua= address; aggregate reports cannot be collected", nil))
	}

	if pct, ok := tags["pct"]; ok && strings.TrimSpace(pct) != "100" && strings.TrimSpace(pct) != "" {
		findings = append(findings, finding(CatDMARC, SevWarning, CodeDMARCPctPartial,
			"DMARC pct="+pct+" applies the policy to only a fraction of mail",
			map[string]any{"pct": pct}))
	}

	return record, findings
}
