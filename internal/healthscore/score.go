// Package healthscore computes a composite 0–100 Deliverability Health Score per domain
// (and per tenant) by fusing signals already collected elsewhere in MX Sentinel: DMARC
// alignment / auth pass rate, feedback-loop complaint volume, blocklist/reputation status
// (repd/rbld), bounce ratio, send-volume anomaly state, and Gmail Postmaster reputation.
//
// The scoring itself (Compute) is a PURE function of its Inputs — no clocks, no I/O, no
// external services — so it is exhaustively table-tested. Wiring to the real stores lives in
// collect.go (read-only) and cmd/scored snapshots the result into Postgres so trends exist.
//
// Graceful degradation is a first-class concern: any component whose input is missing is
// treated as NEUTRAL — it is excluded from the weighted average and the remaining weights are
// renormalized, rather than dragging the score toward zero. A domain with no signals at all
// yields HasData=false and Grade "N/A".
package healthscore

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Component identifiers. These are stable strings used in the JSON breakdown, the snapshot
// component_json column, and the AI/incident explanation, so do not rename casually.
const (
	ComponentDMARC      = "dmarc_alignment"
	ComponentComplaints = "complaint_rate"
	ComponentBlocklist  = "blocklist"
	ComponentBounce     = "bounce_rate"
	ComponentAnomaly    = "volume_anomaly"
	ComponentPostmaster = "postmaster_reputation"
)

// componentLabels are the human-readable names shown in the dashboard and AI text.
var componentLabels = map[string]string{
	ComponentDMARC:      "DMARC / auth alignment",
	ComponentComplaints: "Complaint volume (FBL)",
	ComponentBlocklist:  "Blocklist / reputation",
	ComponentBounce:     "Bounce & rejection ratio",
	ComponentAnomaly:    "Send-volume anomaly",
	ComponentPostmaster: "Gmail Postmaster reputation",
}

// Weights assigns the relative importance of each component. They need not sum to 1 — Compute
// renormalizes over whichever components are actually present.
type Weights struct {
	DMARC      float64
	Complaints float64
	Blocklist  float64
	Bounce     float64
	Anomaly    float64
	Postmaster float64
}

// DefaultWeights are the shipping weights. Auth alignment and bounce/rejection ratio dominate
// because they are the strongest first-order deliverability signals; anomaly and Postmaster are
// lighter because they are noisier / less universally available.
func DefaultWeights() Weights {
	return Weights{
		DMARC:      0.25,
		Bounce:     0.20,
		Blocklist:  0.20,
		Complaints: 0.15,
		Postmaster: 0.10,
		Anomaly:    0.10,
	}
}

func (w Weights) forComponent(name string) float64 {
	switch name {
	case ComponentDMARC:
		return w.DMARC
	case ComponentComplaints:
		return w.Complaints
	case ComponentBlocklist:
		return w.Blocklist
	case ComponentBounce:
		return w.Bounce
	case ComponentAnomaly:
		return w.Anomaly
	case ComponentPostmaster:
		return w.Postmaster
	}
	return 0
}

// ---- inputs ----------------------------------------------------------------

// Inputs is the raw signal bundle for one scoring target. A nil pointer means "no data for
// this component" — it is excluded from the score (neutral), never scored as zero.
type Inputs struct {
	DMARC      *DMARCInput
	Complaints *ComplaintInput
	Blocklist  *BlocklistInput
	Bounce     *BounceInput
	Anomaly    *AnomalyInput
	Postmaster *PostmasterInput
}

// DMARCInput carries aggregate DMARC alignment counts (from ClickHouse dmarc_records).
type DMARCInput struct {
	Total       uint64
	DKIMAligned uint64
	SPFAligned  uint64
}

// ComplaintInput carries the 24h feedback-loop complaint count for the sending domain.
type ComplaintInput struct {
	Complaints24h int
}

// BlocklistInput carries how many of the relay's monitored egress IPs are currently listed on
// at least one DNSBL. This is relay-global infrastructure state (repd/rbld), shared across a
// tenant's domains.
type BlocklistInput struct {
	TotalIPs  int
	ListedIPs int
}

// BounceInput carries outcome counts over the scoring window (from ClickHouse smtp_events).
// Deferred is transient and is NOT counted as a failure.
type BounceInput struct {
	Total    uint64
	Bounced  uint64
	Rejected uint64
	Deferred uint64
}

// AnomalyInput carries the domain's send-volume anomaly state. Ratio is observed/baseline of
// the most recent trip (anomalyd's factor); it is ignored when ActiveSpike is false.
type AnomalyInput struct {
	ActiveSpike bool
	Ratio       float64
}

// PostmasterInput carries the latest Gmail Postmaster grade and optional spam rate.
type PostmasterInput struct {
	Grade    string   // HIGH|MEDIUM|LOW|BAD (case-insensitive); "" means no data
	SpamRate *float64 // fraction 0..1, nil when unknown
}

// ---- per-component scoring (each returns 0..100) ---------------------------

func clamp100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func (in DMARCInput) score() (float64, string) {
	if in.Total == 0 {
		return 100, "no DMARC records in window"
	}
	dkim := float64(in.DKIMAligned) / float64(in.Total)
	spf := float64(in.SPFAligned) / float64(in.Total)
	// A message passes DMARC when EITHER identifier aligns; the aggregate summary reports the
	// two rates separately, so max() is the tightest lower bound on the true pass rate.
	pass := math.Max(dkim, spf)
	return clamp100(pass * 100), fmt.Sprintf("%.1f%% of %d reported messages DMARC-aligned", pass*100, in.Total)
}

func (in ComplaintInput) score() (float64, string) {
	// Each complaint in the last 24h costs 6 points; ~17 complaints floors the component.
	score := clamp100(100 - float64(in.Complaints24h)*6)
	return score, fmt.Sprintf("%d feedback-loop complaint(s) in last 24h", in.Complaints24h)
}

func (in BlocklistInput) score() (float64, string, bool) {
	if in.TotalIPs == 0 {
		return 0, "no monitored egress IPs", false // not present
	}
	frac := float64(in.ListedIPs) / float64(in.TotalIPs)
	score := clamp100(100 * (1 - frac))
	// Any active listing is serious: cap the component at 50 even if only one of many IPs is
	// listed, so a single Spamhaus hit can't be diluted to a passing grade.
	if in.ListedIPs > 0 && score > 50 {
		score = 50
	}
	return score, fmt.Sprintf("%d of %d egress IP(s) blocklisted", in.ListedIPs, in.TotalIPs), true
}

func (in BounceInput) score() (float64, string, bool) {
	if in.Total == 0 {
		return 0, "no send events in window", false // not present
	}
	rate := float64(in.Bounced+in.Rejected) / float64(in.Total)
	// <=2% bounce+reject is perfect; degrades linearly to 0 at 20%.
	score := clamp100(100 * (1 - (rate-0.02)/0.18))
	return score, fmt.Sprintf("%.2f%% bounce+reject over %d messages", rate*100, in.Total), true
}

func (in AnomalyInput) score() (float64, string) {
	if !in.ActiveSpike {
		return 100, "no active volume anomaly"
	}
	ratio := in.Ratio
	if ratio < 1 {
		ratio = 1
	}
	// A 2x spike costs 25 points, 5x floors the component.
	score := clamp100(100 - (ratio-1)*25)
	return score, fmt.Sprintf("active volume spike at %.1fx baseline", ratio)
}

// postmasterGradeScore maps a Gmail Postmaster grade to a base score.
func postmasterGradeScore(grade string) (float64, bool) {
	switch grade {
	case "HIGH", "GOOD":
		return 100, true
	case "MEDIUM":
		return 60, true
	case "LOW":
		return 30, true
	case "BAD":
		return 0, true
	default:
		return 0, false
	}
}

func (in PostmasterInput) score() (float64, string, bool) {
	base, ok := postmasterGradeScore(upper(in.Grade))
	if !ok && in.SpamRate == nil {
		return 0, "no Postmaster data", false // not present
	}
	if !ok {
		base = 100 // no grade but we do have a spam rate; let the rate decide
	}
	detail := fmt.Sprintf("Postmaster grade %s", nonEmpty(upper(in.Grade), "unknown"))
	if in.SpamRate != nil {
		// Gmail flags a domain at 0.3% user-reported spam; map 0→100, 0.3%→0.
		rateScore := clamp100(100 * (1 - *in.SpamRate/0.003))
		if rateScore < base {
			base = rateScore
		}
		detail = fmt.Sprintf("%s, spam rate %.3f%%", detail, *in.SpamRate*100)
	}
	return base, detail, true
}

// ---- composite -------------------------------------------------------------

// ComponentScore is one weighted component of the final score.
type ComponentScore struct {
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Present bool    `json:"present"`
	Score   float64 `json:"score"`  // 0..100 (0 and Present=false means "no data")
	Weight  float64 `json:"weight"` // normalized weight among present components (0 if absent)
	Impact  float64 `json:"impact"` // points this component subtracts from a perfect 100
	Detail  string  `json:"detail"`
}

// Result is the composite score plus its component breakdown.
type Result struct {
	Score      float64          `json:"score"`    // 0..100, rounded to 1 decimal
	Grade      string           `json:"grade"`    // A|B|C|D|F, or "N/A" when HasData is false
	HasData    bool             `json:"has_data"` // false when every component is absent
	Coverage   float64          `json:"coverage"` // fraction of total weight that had data (0..1)
	Components []ComponentScore `json:"components"`
}

// Grade maps a 0..100 score to a letter grade.
func Grade(score float64) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}

// Compute fuses Inputs into a composite Result using the given Weights. It is pure and
// deterministic. Missing components are excluded (neutral) and the surviving weights are
// renormalized; when nothing is present the Result has HasData=false and Grade "N/A".
func Compute(in Inputs, w Weights) Result {
	type raw struct {
		name    string
		present bool
		score   float64
		detail  string
	}
	var raws []raw

	add := func(name string, present bool, score float64, detail string) {
		raws = append(raws, raw{name: name, present: present, score: score, detail: detail})
	}

	if in.DMARC != nil {
		sc, d := in.DMARC.score()
		add(ComponentDMARC, true, sc, d)
	} else {
		add(ComponentDMARC, false, 0, "no DMARC aggregate data")
	}
	if in.Complaints != nil {
		sc, d := in.Complaints.score()
		add(ComponentComplaints, true, sc, d)
	} else {
		add(ComponentComplaints, false, 0, "no feedback-loop data")
	}
	if in.Blocklist != nil {
		sc, d, present := in.Blocklist.score()
		add(ComponentBlocklist, present, sc, d)
	} else {
		add(ComponentBlocklist, false, 0, "no blocklist data")
	}
	if in.Bounce != nil {
		sc, d, present := in.Bounce.score()
		add(ComponentBounce, present, sc, d)
	} else {
		add(ComponentBounce, false, 0, "no send telemetry")
	}
	if in.Anomaly != nil {
		sc, d := in.Anomaly.score()
		add(ComponentAnomaly, true, sc, d)
	} else {
		add(ComponentAnomaly, false, 0, "no anomaly baseline")
	}
	if in.Postmaster != nil {
		sc, d, present := in.Postmaster.score()
		add(ComponentPostmaster, present, sc, d)
	} else {
		add(ComponentPostmaster, false, 0, "no Postmaster data")
	}

	// Total weight over present components, for renormalization.
	var totalPresentWeight float64
	for _, r := range raws {
		if r.present {
			totalPresentWeight += w.forComponent(r.name)
		}
	}

	comps := make([]ComponentScore, 0, len(raws))
	var weighted float64
	for _, r := range raws {
		cs := ComponentScore{
			Name:    r.name,
			Label:   componentLabels[r.name],
			Present: r.present,
			Score:   round1(r.score),
			Detail:  r.detail,
		}
		if r.present && totalPresentWeight > 0 {
			nw := w.forComponent(r.name) / totalPresentWeight
			cs.Weight = round3(nw)
			cs.Impact = round1(nw * (100 - r.score))
			weighted += nw * r.score
		}
		comps = append(comps, cs)
	}

	res := Result{Components: comps}
	if totalPresentWeight == 0 {
		res.HasData = false
		res.Grade = "N/A"
		res.Score = 0
		res.Coverage = 0
		return res
	}

	res.HasData = true
	res.Score = round1(weighted)
	res.Grade = Grade(res.Score)

	var totalWeight float64
	for _, r := range raws {
		totalWeight += w.forComponent(r.name)
	}
	if totalWeight > 0 {
		res.Coverage = round3(totalPresentWeight / totalWeight)
	}
	return res
}

// Drags returns the present components sorted by how many points they subtract from a perfect
// score (largest drag first) — the "what's dragging the score down" breakdown. Components with
// zero impact are omitted.
func (r Result) Drags() []ComponentScore {
	out := make([]ComponentScore, 0, len(r.Components))
	for _, c := range r.Components {
		if c.Present && c.Impact > 0 {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Impact > out[j].Impact })
	return out
}

// Explain renders a compact, body-free summary suitable for feeding into an incident's AI
// context or a human-facing tooltip. It never contains message content — only aggregate metrics.
func (r Result) Explain() string {
	if !r.HasData {
		return "Deliverability health score: N/A (insufficient signal data collected yet)."
	}
	s := fmt.Sprintf("Deliverability health score %.0f/100 (grade %s), computed over %.0f%% of weighted signals.",
		r.Score, r.Grade, r.Coverage*100)
	drags := r.Drags()
	if len(drags) == 0 {
		return s + " No component is materially dragging the score down."
	}
	s += " Top drags: "
	for i, d := range drags {
		if i > 0 {
			s += "; "
		}
		if i >= 3 {
			s += fmt.Sprintf("and %d more", len(drags)-i)
			break
		}
		s += fmt.Sprintf("%s (-%.0f, %s)", d.Label, d.Impact, d.Detail)
	}
	return s + "."
}

// MarshalComponents serializes a Result's component breakdown to JSON for storage in the
// health_score_snapshots.components JSONB column. It always returns a valid JSON array.
func MarshalComponents(r Result) (json.RawMessage, error) {
	if len(r.Components) == 0 {
		return json.RawMessage("[]"), nil
	}
	b, err := json.Marshal(r.Components)
	if err != nil {
		return nil, fmt.Errorf("marshal components: %w", err)
	}
	return b, nil
}

// ---- small helpers ---------------------------------------------------------

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
