// Package bimi implements BIMI / VMC readiness validation. For each monitored domain it
// resolves the default._bimi.<domain> TXT record, parses its tags, and — cross-checking the
// domain's existing DMARC posture and fetching the referenced logo (SVG Tiny P/S) and VMC
// certificate — produces a per-domain readiness state plus a "what's blocking BIMI" checklist.
//
// BIMI (Brand Indicators for Message Identification) is the visible payoff of reaching DMARC
// enforcement: mailbox providers display a brand's logo next to authenticated mail, but only
// once DMARC is at p=quarantine or p=reject. See docs/bimi.md.
//
// The parser, SVG-Tiny-PS validation, VMC expiry extraction, and readiness/checklist
// computation are pure functions (no DNS/HTTP) so they are exhaustively unit-tested against
// fixture strings. Live resolution/fetching lives behind the Resolver and Fetcher interfaces
// and is exercised only by cmd/bimid.
package bimi

import (
	"strings"
	"time"
)

// Readiness states, most-blocked to fully-ready.
const (
	StateNotConfigured = "not_configured" // no BIMI record published yet
	StateBlocked       = "blocked"        // record present but a hard prerequisite is unmet
	StatePartial       = "partial"        // logo works + DMARC enforced, but no valid VMC
	StateVMCExpired    = "vmc_expired"    // VMC present but the certificate has expired
	StateReady         = "ready"          // record + logo + DMARC enforcement + valid VMC
)

// Checklist item statuses.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// Stable machine codes for checklist items. The dashboard keys off these.
const (
	CheckRecord      = "BIMI_RECORD"
	CheckDMARC       = "DMARC_ENFORCEMENT"
	CheckLogo        = "BIMI_LOGO"
	CheckVMCPresent  = "VMC_PRESENT"
	CheckVMCValidity = "VMC_VALIDITY"
)

// Record is a parsed default._bimi TXT record.
type Record struct {
	Raw     string `json:"raw"`
	Version string `json:"version"`  // e.g. "BIMI1"
	LogoURL string `json:"logo_url"` // l= tag
	VMCURL  string `json:"vmc_url"`  // a= tag (optional)
	Valid   bool   `json:"valid"`    // parsed a v=BIMI1 record with a plausible shape
}

// ChecklistItem is one line of the "what's blocking BIMI" checklist.
type ChecklistItem struct {
	Code   string `json:"code"`
	Label  string `json:"label"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

// Artifact is a fetched (or attempted) HTTP resource — the logo SVG or the VMC PEM.
// A nil *Artifact means the fetch was never attempted (e.g. no URL in the record).
type Artifact struct {
	Fetched bool   // an HTTP attempt was made
	Body    []byte // response body when Fetched && Err == ""
	Err     string // non-empty when the fetch failed
}

// Report is the full BIMI assessment of a domain at one point in time.
type Report struct {
	Domain        string          `json:"domain"`
	Record        Record          `json:"record"`
	DMARCEnforced bool            `json:"dmarc_enforced"`
	DMARCPolicy   string          `json:"dmarc_policy"`
	LogoURL       string          `json:"logo_url"`
	VMCURL        string          `json:"vmc_url"`
	VMCExpiry     *time.Time      `json:"vmc_expiry"`
	Readiness     string          `json:"readiness_state"`
	Checklist     []ChecklistItem `json:"checklist"`
}

// ParseRecord parses a BIMI TXT record string. It returns a Record with Valid=false when the
// string is empty or is not a v=BIMI1 record; malformed-but-recognizable records still parse
// so the checklist can explain the specific problem.
func ParseRecord(txt string) Record {
	rec := Record{Raw: strings.TrimSpace(txt)}
	if rec.Raw == "" {
		return rec
	}
	tags := parseTags(rec.Raw)
	rec.Version = strings.ToUpper(strings.TrimSpace(tags["v"]))
	rec.LogoURL = strings.TrimSpace(tags["l"])
	rec.VMCURL = strings.TrimSpace(tags["a"])
	rec.Valid = rec.Version == "BIMI1"
	return rec
}

// parseTags splits a "k=v; k=v" record into a lower-cased-key map. Values keep their case
// (URLs are case-sensitive). A bare key with no '=' is ignored.
func parseTags(record string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(part[:eq]))
		val := strings.TrimSpace(part[eq+1:])
		if key == "" {
			continue
		}
		out[key] = val
	}
	return out
}

// DMARCEnforced reports whether a DMARC record string is at enforcement (p=quarantine or
// p=reject), which BIMI requires. It also returns the normalized policy value ("none",
// "quarantine", "reject", or "" when absent/unparseable).
func DMARCEnforced(dmarcRecord string) (bool, string) {
	r := strings.TrimSpace(dmarcRecord)
	if r == "" || !strings.HasPrefix(strings.ToLower(r), "v=dmarc1") {
		return false, ""
	}
	policy := strings.ToLower(strings.TrimSpace(parseTags(r)["p"]))
	return policy == "quarantine" || policy == "reject", policy
}

// Assess is the pure core: given the raw BIMI TXT record, the domain's DMARC record, and the
// already-fetched logo/VMC artifacts (nil when not applicable), it computes the readiness
// state and checklist. `now` is injected so expiry evaluation is deterministic in tests.
func Assess(domain, recordTXT, dmarcRecord string, logo, vmc *Artifact, now time.Time) Report {
	rec := ParseRecord(recordTXT)
	enforced, policy := DMARCEnforced(dmarcRecord)

	rep := Report{
		Domain:        domain,
		Record:        rec,
		DMARCEnforced: enforced,
		DMARCPolicy:   policy,
		LogoURL:       rec.LogoURL,
		VMCURL:        rec.VMCURL,
	}

	// No record at all → nothing else matters.
	if !rec.Valid {
		rep.Readiness = StateNotConfigured
		rep.Checklist = []ChecklistItem{
			{Code: CheckRecord, Label: "BIMI DNS record", Status: StatusFail,
				Detail: "No valid v=BIMI1 record found at default._bimi." + domain},
			dmarcItem(enforced, policy),
		}
		return rep
	}

	var items []ChecklistItem
	items = append(items, ChecklistItem{
		Code: CheckRecord, Label: "BIMI DNS record", Status: StatusOK,
		Detail: "Published a valid v=BIMI1 record at default._bimi." + domain,
	})

	dmarc := dmarcItem(enforced, policy)
	items = append(items, dmarc)

	// Logo.
	logoItem, logoOK := assessLogo(rec.LogoURL, logo)
	items = append(items, logoItem)

	// VMC (optional but required by Gmail/Apple).
	vmcItem, vmcExpiry, vmcPresent, vmcValid := assessVMC(rec.VMCURL, vmc, now)
	items = append(items, vmcItem)
	rep.VMCExpiry = vmcExpiry

	rep.Checklist = items
	rep.Readiness = readiness(enforced, logoOK, vmcPresent, vmcValid, vmcExpiry, now)
	return rep
}

func dmarcItem(enforced bool, policy string) ChecklistItem {
	if enforced {
		return ChecklistItem{Code: CheckDMARC, Label: "DMARC at enforcement", Status: StatusOK,
			Detail: "DMARC policy is p=" + policy + " (BIMI requires quarantine or reject)"}
	}
	detail := "DMARC is not at enforcement; BIMI requires p=quarantine or p=reject"
	if policy == "none" {
		detail = "DMARC policy is p=none (monitoring only); move to p=quarantine or p=reject to enable BIMI"
	} else if policy == "" {
		detail = "No enforced DMARC policy found; publish p=quarantine or p=reject to enable BIMI"
	}
	return ChecklistItem{Code: CheckDMARC, Label: "DMARC at enforcement", Status: StatusFail, Detail: detail}
}

// assessLogo validates the l= logo. Returns the checklist item and whether the logo is usable.
func assessLogo(url string, logo *Artifact) (ChecklistItem, bool) {
	item := ChecklistItem{Code: CheckLogo, Label: "SVG Tiny P/S logo"}
	if url == "" {
		item.Status = StatusFail
		item.Detail = "BIMI record has no l= logo URL"
		return item, false
	}
	if logo == nil || !logo.Fetched {
		item.Status = StatusFail
		item.Detail = "Logo not fetched: " + url
		return item, false
	}
	if logo.Err != "" {
		item.Status = StatusFail
		item.Detail = "Logo could not be retrieved: " + logo.Err
		return item, false
	}
	if problems := ValidateSVG(logo.Body); len(problems) > 0 {
		item.Status = StatusFail
		item.Detail = "Logo is not valid SVG Tiny P/S: " + strings.Join(problems, "; ")
		return item, false
	}
	item.Status = StatusOK
	item.Detail = "Logo serves valid SVG Tiny P/S"
	return item, true
}

// assessVMC validates the a= VMC certificate. Returns the item, parsed expiry (nil if none),
// whether a VMC was present, and whether it is present-and-valid (fetched + not expired).
func assessVMC(url string, vmc *Artifact, now time.Time) (item ChecklistItem, expiry *time.Time, present, valid bool) {
	item = ChecklistItem{Code: CheckVMCValidity, Label: "Verified Mark Certificate"}
	if url == "" {
		item.Status = StatusWarn
		item.Detail = "No VMC (a=) published. Gmail and Apple Mail require a VMC; some providers show the logo without one."
		return item, nil, false, false
	}
	if vmc == nil || !vmc.Fetched {
		item.Status = StatusFail
		item.Detail = "VMC not fetched: " + url
		return item, nil, true, false
	}
	if vmc.Err != "" {
		item.Status = StatusFail
		item.Detail = "VMC could not be retrieved: " + vmc.Err
		return item, nil, true, false
	}
	exp, err := ParseVMCExpiry(vmc.Body)
	if err != nil {
		item.Status = StatusFail
		item.Detail = "VMC certificate could not be parsed: " + err.Error()
		return item, nil, true, false
	}
	expiry = &exp
	if !exp.After(now) {
		item.Status = StatusFail
		item.Detail = "VMC certificate expired on " + exp.UTC().Format("2006-01-02")
		return item, expiry, true, false
	}
	item.Status = StatusOK
	item.Detail = "VMC certificate valid until " + exp.UTC().Format("2006-01-02")
	return item, expiry, true, true
}

// readiness derives the overall state from the individual prerequisites.
func readiness(dmarcEnforced, logoOK, vmcPresent, vmcValid bool, expiry *time.Time, now time.Time) string {
	if !dmarcEnforced || !logoOK {
		return StateBlocked
	}
	if vmcPresent && !vmcValid {
		// Distinguish an expired cert (actionable renewal) from an otherwise unusable one.
		if expiry != nil && !expiry.After(now) {
			return StateVMCExpired
		}
		return StateBlocked
	}
	if vmcValid {
		return StateReady
	}
	// DMARC + logo good, no VMC published: logo shows in providers that don't require a VMC.
	return StatePartial
}
