package mtasts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Resolver abstracts the single DNS lookup MTA-STS needs (the _mta-sts TXT record). It is
// satisfied by internal/dns.SystemResolver and by a fixture map in tests.
type Resolver interface {
	TXT(ctx context.Context, name string) ([]string, error)
}

// PolicyFetcher fetches the policy file body for a domain over HTTPS. Implementations must
// enforce their own timeout. A missing policy returns a non-nil error.
type PolicyFetcher interface {
	Fetch(ctx context.Context, domain string) (body string, err error)
}

// CertInfo is the TLS certificate result for one MX host.
type CertInfo struct {
	Host     string    `json:"host"`
	NotAfter time.Time `json:"not_after,omitempty"`
	Valid    bool      `json:"valid"`
	Err      string    `json:"error,omitempty"`
}

// CertChecker retrieves and validates the TLS certificate an MX host presents (typically
// via SMTP STARTTLS on :25). Implementations enforce their own timeout.
type CertChecker interface {
	Check(ctx context.Context, host string) CertInfo
}

// Deps bundles the three live-lookup dependencies so Inspect stays testable.
type Deps struct {
	Resolver    Resolver
	Fetcher     PolicyFetcher
	CertChecker CertChecker
}

// Options tunes an inspection.
type Options struct {
	// CertWarnDays is how many days before expiry a warning is raised (default 14).
	CertWarnDays int
}

// State is the parsed MTA-STS posture of a domain at one point in time. It is serialized to
// the mtasts_snapshots.state JSONB column; its checksum drives change/drift detection.
type State struct {
	Domain        string     `json:"domain"`
	TXTID         string     `json:"txt_id,omitempty"`
	PolicyPresent bool       `json:"policy_present"`
	Mode          Mode       `json:"mode,omitempty"`
	MX            []string   `json:"mx,omitempty"`
	MaxAge        int        `json:"max_age,omitempty"`
	Certs         []CertInfo `json:"certs,omitempty"`
}

// Snapshot is the result of inspecting a domain's MTA-STS posture.
type Snapshot struct {
	Domain    string
	State     State
	StateJSON []byte
	Checksum  string
	Findings  []contracts.DNSFinding
	Healthy   bool
	// CertExpiry is the earliest NotAfter across all checked MX certs (zero if none valid).
	CertExpiry time.Time
}

// Inspect resolves and validates a domain's MTA-STS posture and returns a Snapshot. It only
// returns an error for failures that prevent producing a snapshot at all; per-record
// problems are reported as findings.
func Inspect(ctx context.Context, d Deps, domain string, opts Options) (Snapshot, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	warnDays := opts.CertWarnDays
	if warnDays <= 0 {
		warnDays = 14
	}
	state := State{Domain: domain}
	var findings []contracts.DNSFinding

	// 1. _mta-sts TXT record.
	var rec STSRecord
	if d.Resolver != nil {
		txt := firstTXT(ctx, d.Resolver, "_mta-sts."+domain, "v=stsv1")
		if txt == "" {
			findings = append(findings, finding(SevWarning, CodeTXTMissing,
				"No _mta-sts TXT record published for "+domain, nil))
		} else if r, err := ParseTXT(txt); err != nil {
			findings = append(findings, finding(SevWarning, CodeTXTInvalid,
				"Invalid _mta-sts TXT record: "+err.Error(), nil))
		} else {
			rec = r
			state.TXTID = r.ID
		}
	}

	// 2. Fetch + parse the policy file.
	if d.Fetcher != nil {
		body, err := d.Fetcher.Fetch(ctx, domain)
		if err != nil {
			findings = append(findings, finding(SevWarning, CodePolicyUnreachable,
				"Could not fetch MTA-STS policy: "+err.Error(), nil))
		} else if pol, perr := ParsePolicy(body); perr != nil {
			findings = append(findings, finding(SevCritical, CodePolicyInvalid,
				"MTA-STS policy is malformed: "+perr.Error(), nil))
		} else {
			state.PolicyPresent = true
			state.Mode = pol.Mode
			state.MaxAge = pol.MaxAge
			state.MX = append(state.MX, pol.MX...)
			sort.Strings(state.MX)
			findings = append(findings, policyFindings(pol, rec)...)

			// 3. Certificate check for each concrete (non-wildcard) MX host.
			if d.CertChecker != nil {
				state.Certs, findings = checkCerts(ctx, d.CertChecker, pol.MX, warnDays, findings)
			}
		}
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(stateJSON)

	return Snapshot{
		Domain:     domain,
		State:      state,
		StateJSON:  stateJSON,
		Checksum:   hex.EncodeToString(sum[:]),
		Findings:   findings,
		Healthy:    worstSeverity(findings) != SevCritical,
		CertExpiry: earliestExpiry(state.Certs),
	}, nil
}

func policyFindings(pol Policy, _ STSRecord) []contracts.DNSFinding {
	var out []contracts.DNSFinding
	// (A missing/invalid STS TXT id is already flagged upstream; nothing to compare here, so
	// the record itself is intentionally unused in this function.)
	switch pol.Mode {
	case ModeNone:
		out = append(out, finding(SevWarning, CodeModeNotEnforced,
			"MTA-STS policy mode is 'none' — TLS is not being enforced", nil))
	case ModeTesting:
		out = append(out, finding(SevInfo, CodeModeNotEnforced,
			"MTA-STS policy is in 'testing' mode — failures are reported but mail still flows", nil))
	}
	if pol.Mode != ModeNone && len(pol.MX) == 0 {
		out = append(out, finding(SevCritical, CodeNoMXHosts,
			"MTA-STS policy lists no mx hosts", nil))
	}
	return out
}

func checkCerts(ctx context.Context, cc CertChecker, mx []string, warnDays int, findings []contracts.DNSFinding) ([]CertInfo, []contracts.DNSFinding) {
	var certs []CertInfo
	for _, host := range mx {
		if strings.HasPrefix(host, "*.") {
			continue // wildcard patterns are not directly dialable
		}
		info := cc.Check(ctx, host)
		certs = append(certs, info)
		switch {
		case info.Err != "":
			findings = append(findings, finding(SevWarning, CodeCertUnreachable,
				"Could not verify TLS certificate for "+host+": "+info.Err,
				map[string]any{"host": host}))
		case !info.Valid:
			findings = append(findings, finding(SevCritical, CodeCertInvalid,
				"MX host "+host+" presented an invalid TLS certificate",
				map[string]any{"host": host}))
		case !info.NotAfter.IsZero():
			days := time.Until(info.NotAfter).Hours() / 24
			if days < 0 {
				findings = append(findings, finding(SevCritical, CodeCertExpired,
					"TLS certificate for "+host+" has expired",
					map[string]any{"host": host, "not_after": info.NotAfter.UTC().Format(time.RFC3339)}))
			} else if days <= float64(warnDays) {
				findings = append(findings, finding(SevWarning, CodeCertExpiringSoon,
					"TLS certificate for "+host+" expires soon",
					map[string]any{"host": host, "not_after": info.NotAfter.UTC().Format(time.RFC3339)}))
			}
		}
	}
	return certs, findings
}

func earliestExpiry(certs []CertInfo) time.Time {
	var earliest time.Time
	for _, c := range certs {
		if c.NotAfter.IsZero() {
			continue
		}
		if earliest.IsZero() || c.NotAfter.Before(earliest) {
			earliest = c.NotAfter
		}
	}
	return earliest
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
