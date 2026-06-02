package dns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// State is the full parsed DNS posture of a domain at one point in time. It is serialized
// to the dns_snapshots.state JSONB column; its checksum drives change detection.
type State struct {
	Domain string            `json:"domain"`
	MX     []string          `json:"mx,omitempty"`
	SPF    string            `json:"spf,omitempty"`
	DMARC  string            `json:"dmarc,omitempty"`
	DKIM   map[string]string `json:"dkim,omitempty"`
	MTASTS string            `json:"mta_sts,omitempty"`
	TLSRPT string            `json:"tls_rpt,omitempty"`
	BIMI   string            `json:"bimi,omitempty"`
}

// Snapshot is the result of inspecting a domain.
type Snapshot struct {
	Domain    string
	State     State
	StateJSON []byte
	Checksum  string
	Findings  []contracts.DNSFinding
	Healthy   bool
}

// Options tunes an inspection.
type Options struct {
	// Selectors are the DKIM selectors to check (from config / observed mail). DKIM is
	// not checked when empty, because selectors are not discoverable from DNS.
	Selectors []string
}

// Inspect resolves and validates a domain's mail DNS posture and returns a Snapshot.
// It only returns an error for failures that prevent producing a snapshot at all;
// per-record problems are reported as findings.
func Inspect(ctx context.Context, r Resolver, domain string, opts Options) (Snapshot, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	state := State{Domain: domain}
	var findings []contracts.DNSFinding

	// MX
	mxs, err := r.MX(ctx, domain)
	if err != nil {
		findings = append(findings, finding(CatMX, SevWarning, CodeMXMissing,
			"MX lookup failed: "+err.Error(), nil))
	} else {
		for _, mx := range mxs {
			state.MX = append(state.MX, strings.TrimSuffix(strings.ToLower(mx.Host), "."))
		}
		sort.Strings(state.MX)
		if len(state.MX) == 0 {
			findings = append(findings, finding(CatMX, SevCritical, CodeMXMissing,
				"No MX records published for "+domain, nil))
		}
	}

	// SPF
	spf, spfFindings := evaluateSPF(ctx, r, domain)
	state.SPF = spf
	findings = append(findings, spfFindings...)

	// DMARC
	dmarc, dmarcFindings := evaluateDMARC(ctx, r, domain)
	state.DMARC = dmarc
	findings = append(findings, dmarcFindings...)

	// DKIM (only when selectors are known)
	if len(opts.Selectors) > 0 {
		dkim, dkimFindings := evaluateDKIM(ctx, r, domain, opts.Selectors)
		if len(dkim) > 0 {
			state.DKIM = dkim
		}
		findings = append(findings, dkimFindings...)
	}

	// Presence-only modern standards (recorded for drift detection; not yet validated).
	state.MTASTS = firstTXT(ctx, r, "_mta-sts."+domain, "v=stsv1")
	state.TLSRPT = firstTXT(ctx, r, "_smtp._tls."+domain, "v=tlsrptv1")
	state.BIMI = firstTXT(ctx, r, "default._bimi."+domain, "v=bimi1")

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(stateJSON)

	return Snapshot{
		Domain:    domain,
		State:     state,
		StateJSON: stateJSON,
		Checksum:  hex.EncodeToString(sum[:]),
		Findings:  findings,
		Healthy:   worstSeverity(findings) != SevCritical,
	}, nil
}

// firstTXT returns the first TXT at name with the given (case-insensitive) version prefix,
// or "".
func firstTXT(ctx context.Context, r Resolver, name, versionPrefix string) string {
	txts, err := r.TXT(ctx, name)
	if err != nil {
		return ""
	}
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), versionPrefix) {
			return t
		}
	}
	return ""
}
