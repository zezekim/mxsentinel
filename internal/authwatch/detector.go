package authwatch

import (
	"sync"
	"time"
)

// Config tunes the behavioral detector. All durations/counts are env-overridable in the
// daemon; the defaults are conservative so a busy-but-legitimate credential isn't flagged
// just for sending a lot.
type Config struct {
	Window   time.Duration // rolling window for per-credential accounting
	Cooldown time.Duration // minimum time between trips for the same credential

	// recipient-domain burst (list-blasting): trip contribution when the count of DISTINCT
	// recipient domains in the window meets/exceeds this.
	DistinctRcptThreshold int

	// bounce-rate spike: once at least MinVolume messages are in the window, a
	// spam/block/reputation bounce fraction >= BounceRate contributes to the score.
	MinVolume  int
	BounceRate float64

	// volume spike: current windowed volume >= VolumeFactor * recent baseline (and above a
	// floor) contributes to the score. Baseline is an EWMA of past windows' volume.
	VolumeFactor float64
	VolumeFloor  int

	// off-hours concentration (optional): fraction of windowed messages sent during
	// [OffHoursStart,OffHoursEnd) (server-local UTC hours) >= OffHoursRate contributes. Set
	// OffHoursWeight to 0 to disable its contribution entirely.
	OffHoursStart int // inclusive hour [0,24)
	OffHoursEnd   int // exclusive hour [0,24)
	OffHoursRate  float64

	// scoring weights per signal; Threshold is the trip line on the summed score.
	RcptWeight     float64
	BounceWeight   float64
	VolumeWeight   float64
	OffHoursWeight float64
	Threshold      float64

	// EWMA smoothing factor for the volume baseline (0..1, higher = faster adaptation).
	BaselineAlpha float64
}

// DefaultConfig returns conservative defaults.
func DefaultConfig() Config {
	return Config{
		Window:                time.Hour,
		Cooldown:              6 * time.Hour,
		DistinctRcptThreshold: 50,
		MinVolume:             50,
		BounceRate:            0.30,
		VolumeFactor:          5.0,
		VolumeFloor:           200,
		OffHoursStart:         1,
		OffHoursEnd:           5,
		OffHoursRate:          0.80,
		RcptWeight:            1.0,
		BounceWeight:          1.0,
		VolumeWeight:          1.0,
		OffHoursWeight:        0.5,
		Threshold:             1.0,
		BaselineAlpha:         0.3,
	}
}

// sample is one observed SMTP event for a credential.
type sample struct {
	t        time.Time
	rcpt     string // recipient domain (may be "")
	abuse    bool   // spam/block/reputation bounce
	offHours bool
}

// credWindow is the per-credential rolling state.
type credWindow struct {
	tenant   string
	domain   string // most-recent sending domain seen (context for the incident)
	samples  []sample
	baseline float64 // EWMA of per-window volume, updated each time the window rolls
	lastRoll time.Time
}

// Signal labels (also used as the credential_auth_signal.signal value).
const (
	SignalRecipientBurst = "recipient_burst"
	SignalBounceSpike    = "bounce_spike"
	SignalVolumeSpike    = "volume_spike"
	SignalOffHours       = "off_hours"
	SignalComposite      = "composite_trip"
)

// Observation is one event handed to the detector.
type Observation struct {
	TenantID        string
	SASLUsername    string
	FromDomain      string
	RecipientDomain string
	Abuse           bool // outcome==bounced && reputation-class bounce
	At              time.Time
}

// TripDetail captures the evidence for a composite trip, serialized into the signal/incident.
type TripDetail struct {
	Window        string             `json:"window"`
	Messages      int                `json:"messages"`
	DistinctRcpts int                `json:"distinct_recipient_domains"`
	AbuseBounces  int                `json:"abuse_bounces"`
	BounceRate    float64            `json:"bounce_rate"`
	Volume        int                `json:"volume"`
	Baseline      float64            `json:"baseline_volume"`
	OffHoursRate  float64            `json:"off_hours_rate"`
	Score         float64            `json:"score"`
	Threshold     float64            `json:"threshold"`
	Contributors  []string           `json:"contributors"` // which signals fired
	Weights       map[string]float64 `json:"weights"`
	SendingDomain string             `json:"sending_domain,omitempty"`
	GeoChecked    bool               `json:"geo_checked"` // always false until telemetry extension lands
	GeoCaveat     string             `json:"geo_caveat,omitempty"`
}

// Detector keeps per-credential rolling stats and decides when a credential trips.
type Detector struct {
	cfg Config

	mu      sync.Mutex
	creds   map[string]*credWindow
	alerted map[string]time.Time // username -> last trip time (cooldown)
}

// NewDetector builds a detector with the given config.
func NewDetector(cfg Config) *Detector {
	return &Detector{
		cfg:     cfg,
		creds:   map[string]*credWindow{},
		alerted: map[string]time.Time{},
	}
}

// IsOffHours reports whether hour h (0..23) falls in the configured off-hours band, handling
// a band that wraps past midnight (e.g. start=22, end=5).
func (c Config) IsOffHours(h int) bool {
	if c.OffHoursStart == c.OffHoursEnd {
		return false
	}
	if c.OffHoursStart < c.OffHoursEnd {
		return h >= c.OffHoursStart && h < c.OffHoursEnd
	}
	// wraps midnight
	return h >= c.OffHoursStart || h < c.OffHoursEnd
}

// Observe folds one event into its credential's window and returns a non-nil *TripDetail
// (plus tenant/domain context) exactly when the credential trips the composite threshold and
// is past its cooldown. Callers persist the signal + open the incident on a non-nil return.
func (d *Detector) Observe(o Observation) (detail *TripDetail, tenant, domain string, tripped bool) {
	if o.SASLUsername == "" {
		return nil, "", "", false // not attributable to a credential
	}
	now := o.At
	if now.IsZero() {
		now = time.Now()
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	cw := d.creds[o.SASLUsername]
	if cw == nil {
		cw = &credWindow{lastRoll: now}
		d.creds[o.SASLUsername] = cw
	}
	cw.tenant = o.TenantID
	if o.FromDomain != "" {
		cw.domain = o.FromDomain
	}

	cutoff := now.Add(-d.cfg.Window)
	// Roll the volume baseline once per window span: when the oldest retained sample falls
	// out, fold the just-expired window's count into the EWMA baseline.
	d.maybeRollBaseline(cw, now)

	kept := cw.samples[:0]
	for _, s := range cw.samples {
		if s.t.After(cutoff) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, sample{
		t:        now,
		rcpt:     o.RecipientDomain,
		abuse:    o.Abuse,
		offHours: d.cfg.IsOffHours(now.UTC().Hour()),
	})
	cw.samples = kept

	det := d.score(cw, now)
	if det == nil {
		return nil, "", "", false
	}
	if last, seen := d.alerted[o.SASLUsername]; seen && now.Sub(last) < d.cfg.Cooldown {
		return nil, "", "", false // already alerted recently
	}
	d.alerted[o.SASLUsername] = now
	det.SendingDomain = cw.domain
	return det, cw.tenant, cw.domain, true
}

// score computes the composite score over the current window and returns a TripDetail iff it
// crosses the threshold. Returns nil otherwise. Caller holds d.mu.
func (d *Detector) score(cw *credWindow, _ time.Time) *TripDetail {
	total := len(cw.samples)
	if total == 0 {
		return nil
	}
	distinct := map[string]struct{}{}
	abuseN, offN := 0, 0
	for _, s := range cw.samples {
		if s.rcpt != "" {
			distinct[s.rcpt] = struct{}{}
		}
		if s.abuse {
			abuseN++
		}
		if s.offHours {
			offN++
		}
	}
	distinctN := len(distinct)
	bounceRate := 0.0
	if total > 0 {
		bounceRate = float64(abuseN) / float64(total)
	}
	offRate := 0.0
	if total > 0 {
		offRate = float64(offN) / float64(total)
	}

	var score float64
	var contributors []string

	if distinctN >= d.cfg.DistinctRcptThreshold {
		score += d.cfg.RcptWeight
		contributors = append(contributors, SignalRecipientBurst)
	}
	if total >= d.cfg.MinVolume && bounceRate >= d.cfg.BounceRate {
		score += d.cfg.BounceWeight
		contributors = append(contributors, SignalBounceSpike)
	}
	if cw.baseline > 0 && total >= d.cfg.VolumeFloor && float64(total) >= d.cfg.VolumeFactor*cw.baseline {
		score += d.cfg.VolumeWeight
		contributors = append(contributors, SignalVolumeSpike)
	}
	if d.cfg.OffHoursWeight > 0 && total >= d.cfg.MinVolume && offRate >= d.cfg.OffHoursRate {
		score += d.cfg.OffHoursWeight
		contributors = append(contributors, SignalOffHours)
	}

	if score < d.cfg.Threshold || len(contributors) == 0 {
		return nil
	}

	return &TripDetail{
		Window:        d.cfg.Window.String(),
		Messages:      total,
		DistinctRcpts: distinctN,
		AbuseBounces:  abuseN,
		BounceRate:    bounceRate,
		Volume:        total,
		Baseline:      cw.baseline,
		OffHoursRate:  offRate,
		Score:         score,
		Threshold:     d.cfg.Threshold,
		Contributors:  contributors,
		Weights: map[string]float64{
			SignalRecipientBurst: d.cfg.RcptWeight,
			SignalBounceSpike:    d.cfg.BounceWeight,
			SignalVolumeSpike:    d.cfg.VolumeWeight,
			SignalOffHours:       d.cfg.OffHoursWeight,
		},
		GeoChecked: false,
		GeoCaveat:  "source-IP geo/ASN anomaly not evaluated: submitting client IP is not on the event bus in this topology (needs a telemetry extension + GeoIP DB)",
	}
}

// maybeRollBaseline advances the EWMA volume baseline once per full window span. It uses the
// number of samples currently in the just-elapsed window as the period's volume. This is a
// cheap, allocation-free approximation suitable for an in-memory single-node detector.
func (d *Detector) maybeRollBaseline(cw *credWindow, now time.Time) {
	if cw.lastRoll.IsZero() {
		cw.lastRoll = now
		return
	}
	if now.Sub(cw.lastRoll) < d.cfg.Window {
		return
	}
	// Count samples that belonged to the elapsed window (still within one window of lastRoll).
	periodCutoff := cw.lastRoll.Add(-d.cfg.Window)
	vol := 0
	for _, s := range cw.samples {
		if s.t.After(periodCutoff) && !s.t.After(cw.lastRoll) {
			vol++
		}
	}
	alpha := d.cfg.BaselineAlpha
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	if cw.baseline == 0 {
		cw.baseline = float64(vol)
	} else {
		cw.baseline = alpha*float64(vol) + (1-alpha)*cw.baseline
	}
	cw.lastRoll = now
}

// Prune drops idle per-credential windows and stale cooldown entries so memory tracks only
// recently-active credentials. Call periodically.
func (d *Detector) Prune(now time.Time) {
	cutoff := now.Add(-d.cfg.Window)
	d.mu.Lock()
	defer d.mu.Unlock()
	for user, cw := range d.creds {
		kept := cw.samples[:0]
		for _, s := range cw.samples {
			if s.t.After(cutoff) {
				kept = append(kept, s)
			}
		}
		// Keep windows with a live baseline even if momentarily idle, but drop fully cold ones.
		if len(kept) == 0 && now.Sub(cw.lastRoll) > 2*d.cfg.Window {
			delete(d.creds, user)
		} else {
			cw.samples = kept
		}
	}
	for user, t := range d.alerted {
		if now.Sub(t) > d.cfg.Cooldown {
			delete(d.alerted, user)
		}
	}
}
