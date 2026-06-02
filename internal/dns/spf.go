package dns

import (
	"context"
	"strings"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// spfMaxLookups is the RFC 7208 §4.6.4 limit on DNS-querying mechanisms.
const spfMaxLookups = 10

// isSPF reports whether a TXT record is an SPF record.
func isSPF(txt string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(txt)), "v=spf1")
}

// evaluateSPF resolves and validates a domain's SPF policy. It returns the chosen record
// (empty if none) and any findings.
func evaluateSPF(ctx context.Context, r Resolver, domain string) (string, []contracts.DNSFinding) {
	var findings []contracts.DNSFinding

	txts, err := r.TXT(ctx, domain)
	if err != nil {
		return "", append(findings, finding(CatSPF, SevWarning, CodeSPFLookupFailed,
			"SPF lookup failed: "+err.Error(), nil))
	}

	var spfs []string
	for _, t := range txts {
		if isSPF(t) {
			spfs = append(spfs, t)
		}
	}
	if len(spfs) == 0 {
		return "", append(findings, finding(CatSPF, SevWarning, CodeSPFMissing,
			"No SPF record published for "+domain, nil))
	}
	if len(spfs) > 1 {
		findings = append(findings, finding(CatSPF, SevCritical, CodeSPFMultiple,
			"Multiple SPF records published; receivers will treat this as permerror",
			map[string]any{"count": len(spfs)}))
	}
	record := spfs[0]

	visited := map[string]bool{}
	count, cycle, failed := spfLookups(ctx, r, domain, record, 0, visited)
	if cycle {
		findings = append(findings, finding(CatSPF, SevCritical, CodeSPFRecursive,
			"SPF include chain contains a loop", nil))
	}
	if failed {
		findings = append(findings, finding(CatSPF, SevWarning, CodeSPFLookupFailed,
			"One or more SPF include/redirect targets could not be resolved", nil))
	}
	if count > spfMaxLookups {
		findings = append(findings, finding(CatSPF, SevCritical, CodeSPFLookupLimit,
			"SPF record requires more than 10 DNS lookups (permerror)",
			map[string]any{"lookups": count, "limit": spfMaxLookups}))
	}

	if qual, present := allQualifier(record); present {
		if qual == "+" {
			findings = append(findings, finding(CatSPF, SevCritical, CodeSPFPlusAll,
				"SPF ends in +all, which authorizes any sender", nil))
		}
	} else if !hasRedirect(record) {
		findings = append(findings, finding(CatSPF, SevWarning, CodeSPFNoAll,
			"SPF record has no 'all' mechanism or redirect", nil))
	}

	return record, findings
}

// spfLookups counts DNS-querying mechanisms across the include/redirect chain, detecting
// loops. count = number of querying terms; cycle = a loop was found; failed = a target
// could not be resolved.
func spfLookups(ctx context.Context, r Resolver, domain, record string, depth int, visited map[string]bool) (count int, cycle, failed bool) {
	if depth > spfMaxLookups+1 {
		return count, true, failed
	}
	if visited[strings.ToLower(domain)] {
		return count, true, failed
	}
	visited[strings.ToLower(domain)] = true

	for _, raw := range strings.Fields(record) {
		term := strings.ToLower(stripQualifier(raw))
		switch {
		case term == "a" || strings.HasPrefix(term, "a:") || strings.HasPrefix(term, "a/"):
			count++
		case term == "mx" || strings.HasPrefix(term, "mx:") || strings.HasPrefix(term, "mx/"):
			count++
		case term == "ptr" || strings.HasPrefix(term, "ptr:"):
			count++
		case strings.HasPrefix(term, "exists:"):
			count++
		case strings.HasPrefix(term, "include:"):
			count++
			target := raw[strings.IndexByte(raw, ':')+1:]
			c, cyc, f := resolveSPFTarget(ctx, r, target, depth, visited)
			count += c
			cycle = cycle || cyc
			failed = failed || f
		case strings.HasPrefix(term, "redirect="):
			count++
			target := raw[strings.IndexByte(raw, '=')+1:]
			c, cyc, f := resolveSPFTarget(ctx, r, target, depth, visited)
			count += c
			cycle = cycle || cyc
			failed = failed || f
		}
		if count > 100 { // hard safety bound
			break
		}
	}
	return count, cycle, failed
}

func resolveSPFTarget(ctx context.Context, r Resolver, target string, depth int, visited map[string]bool) (int, bool, bool) {
	target = strings.TrimSuffix(target, ".")
	if target == "" {
		return 0, false, false
	}
	txts, err := r.TXT(ctx, target)
	if err != nil {
		return 0, false, true
	}
	for _, t := range txts {
		if isSPF(t) {
			return spfLookups(ctx, r, target, t, depth+1, visited)
		}
	}
	return 0, false, false
}

// stripQualifier removes a leading SPF qualifier (+ - ~ ?) from a term.
func stripQualifier(term string) string {
	if term == "" {
		return term
	}
	switch term[0] {
	case '+', '-', '~', '?':
		return term[1:]
	}
	return term
}

// allQualifier returns the qualifier of the 'all' mechanism and whether it is present. A
// bare "all" defaults to the "+" qualifier.
func allQualifier(record string) (string, bool) {
	for _, raw := range strings.Fields(record) {
		q := "+"
		t := raw
		switch raw[0] {
		case '+', '-', '~', '?':
			q = string(raw[0])
			t = raw[1:]
		}
		if strings.EqualFold(t, "all") {
			return q, true
		}
	}
	return "", false
}

func hasRedirect(record string) bool {
	for _, raw := range strings.Fields(record) {
		if strings.HasPrefix(strings.ToLower(stripQualifier(raw)), "redirect=") {
			return true
		}
	}
	return false
}
