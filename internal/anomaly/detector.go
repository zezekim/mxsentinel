package anomaly

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Incidenter opens an incident. Satisfied by *postgres.Store (InsertIncident). Declared
// as an interface so the detector doesn't import the postgres package's input type
// directly and stays unit-testable.
type Incidenter interface {
	InsertIncident(ctx context.Context, in IncidentInput) (id string, created bool, err error)
}

// IncidentInput mirrors postgres.IncidentInput's fields used here. cmd/anomalyd adapts the
// real store to this shape (the field set/types match exactly, so the adapter is trivial).
type IncidentInput struct {
	TenantID      string
	SourceEventID string
	Kind          string
	Severity      string
	Domain        string
	Subject       string
	Title         string
	Detail        json.RawMessage
	Confidence    *float64
}

// counter is the in-memory state for one (tenant, sender_domain): the hour bucket it is
// currently accumulating, that hour's running count, the cached EWMA baseline (loaded
// lazily from PG), and a flag so we trip at most once per hour per domain.
type counter struct {
	tenant   string
	hour     time.Time // truncated to the hour
	count    int64
	baseline Baseline
	loaded   bool      // baseline pulled from PG yet?
	tripped  bool      // already tripped for the current hour bucket?
	lastTrip time.Time // cooldown anchor (zero = never)
}

// Detector folds SMTP events into per-domain hourly counters, persists EWMA baselines at
// each hour boundary, and trips spikes. It is safe for concurrent Record calls.
type Detector struct {
	cfg   Config
	store *Store
	inc   Incidenter
	log   *slog.Logger

	mu       sync.Mutex
	counters map[string]*counter // key: tenant\x00domain
}

// NewDetector builds a detector. store persists baselines/anomalies; inc opens incidents.
func NewDetector(cfg Config, store *Store, inc Incidenter, log *slog.Logger) *Detector {
	return &Detector{
		cfg:      cfg,
		store:    store,
		inc:      inc,
		log:      log,
		counters: map[string]*counter{},
	}
}

func key(tenant, domain string) string { return tenant + "\x00" + domain }

// Record folds one outbound message (already attributed to tenant + sender domain) into
// the current hour and trips a spike if the threshold is crossed. now is the event time
// (caller passes time.Now()). Persistence + incident raising happen synchronously so a
// trip can't be lost on shutdown; failures are logged, never returned (we always ack).
func (d *Detector) Record(ctx context.Context, tenant, domain string, now time.Time) {
	if domain == "" {
		return
	}
	hour := now.Truncate(time.Hour)

	d.mu.Lock()
	k := key(tenant, domain)
	c := d.counters[k]
	if c == nil {
		c = &counter{tenant: tenant, hour: hour}
		d.counters[k] = c
	}

	// Lazily load the persisted baseline once per process lifetime per domain.
	if !c.loaded {
		// Release the lock for the DB read; another Record for the same key would just
		// re-load harmlessly. Re-acquire and only adopt if still unloaded.
		d.mu.Unlock()
		b, _, err := d.store.GetBaseline(ctx, tenant, domain)
		if err != nil {
			d.log.Warn("load baseline", "tenant", tenant, "domain", domain, "err", err)
		}
		d.mu.Lock()
		c = d.counters[k]
		if c == nil { // pruned in between; recreate
			c = &counter{tenant: tenant, hour: hour}
			d.counters[k] = c
		}
		if !c.loaded {
			c.baseline = b
			c.loaded = true
		}
	}

	// Hour rollover: the previous hour is complete — fold it into the EWMA and reset.
	if hour.After(c.hour) {
		d.rollover(ctx, domain, c)
		c.hour = hour
		c.count = 0
		c.tripped = false
	}

	c.count++
	observed := c.count
	baseline := c.baseline.EWMA
	samples := c.baseline.Samples

	threshold := d.threshold(baseline)
	warmed := samples >= d.cfg.MinSamples
	cooling := !c.lastTrip.IsZero() && now.Sub(c.lastTrip) < d.cfg.Cooldown
	trip := warmed && !c.tripped && !cooling && float64(observed) > threshold
	if trip {
		c.tripped = true
		c.lastTrip = now
	}
	d.mu.Unlock()

	if trip {
		d.fire(ctx, tenant, domain, observed, baseline)
	}
}

// threshold is max(baseline*Factor, MinAbsolute).
func (d *Detector) threshold(baseline float64) float64 {
	t := baseline * d.cfg.Factor
	if min := float64(d.cfg.MinAbsolute); t < min {
		t = min
	}
	return t
}

// rollover folds a completed hour's count into the domain's EWMA and persists it. Called
// under d.mu. Persistence is best-effort (logged on failure) so a transient DB blip can't
// stall event processing.
func (d *Detector) rollover(ctx context.Context, domain string, c *counter) {
	completed := float64(c.count)
	if c.baseline.Samples == 0 {
		c.baseline.EWMA = completed // seed the EWMA with the first completed hour
	} else {
		a := d.cfg.EWMAAlpha
		c.baseline.EWMA = a*completed + (1-a)*c.baseline.EWMA
	}
	c.baseline.Samples++
	if err := d.store.UpsertBaseline(ctx, c.tenant, domain, c.baseline.EWMA, c.baseline.Samples); err != nil {
		d.log.Warn("persist baseline", "tenant", c.tenant, "domain", domain, "err", err)
	}
}

// fire records the anomaly row and opens a critical incident. The incident is deduped by
// (tenant, source_event_id) in InsertIncident; we synthesize a stable per-hour event id so
// the same spike opens at most one incident even across a process restart in the same hour.
func (d *Detector) fire(ctx context.Context, tenant, domain string, observed int64, baseline float64) {
	factor := 0.0
	if baseline > 0 {
		factor = float64(observed) / baseline
	}
	d.log.Warn("send-volume spike detected",
		"tenant", tenant, "domain", domain, "observed_hour", observed, "baseline", baseline, "factor", factor)

	if err := d.store.InsertAnomaly(ctx, tenant, domain, observed, baseline, factor); err != nil {
		d.log.Error("record volume anomaly", "tenant", tenant, "domain", domain, "err", err)
	}

	detail, _ := json.Marshal(map[string]any{
		"sender_domain":       domain,
		"observed_hour_count": observed,
		"baseline_hourly":     baseline,
		"factor":              factor,
		"threshold_factor":    d.cfg.Factor,
		"min_absolute":        d.cfg.MinAbsolute,
		"reason":              "current-hour send volume exceeded the learned baseline by the configured factor",
		"remediation":         "investigate the sending account/domain for a compromised credential or runaway script before the shared IP pool's reputation is harmed",
	})
	hourKey := time.Now().Truncate(time.Hour).UTC().Format("2006010215")
	if _, _, err := d.inc.InsertIncident(ctx, IncidentInput{
		TenantID:      tenant,
		SourceEventID: "anomaly:" + domain + ":" + hourKey,
		Kind:          "other",
		Severity:      "critical",
		Domain:        domain,
		Subject:       domain,
		Title:         "Send-volume spike for " + domain,
		Detail:        detail,
	}); err != nil {
		d.log.Error("open volume-spike incident", "tenant", tenant, "domain", domain, "err", err)
	}
}

// Sweep rolls over any counters whose current hour is now complete (so a quiet domain's
// completed hour still folds into its baseline even with no new events), and prunes idle
// counters. Call it on a ticker from the daemon's main loop.
func (d *Detector) Sweep(ctx context.Context, now time.Time) {
	hour := now.Truncate(time.Hour)
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, c := range d.counters {
		if hour.After(c.hour) {
			if c.count > 0 {
				d.rollover(ctx, domainOf(k), c)
			}
			c.hour = hour
			c.count = 0
			c.tripped = false
		}
		// Prune domains idle for a full day with no pending cooldown — frees memory on a
		// relay with churny one-off sender domains. Their baseline persists in PG.
		idle := now.Sub(c.hour) > 24*time.Hour
		coolingDone := c.lastTrip.IsZero() || now.Sub(c.lastTrip) > d.cfg.Cooldown
		if idle && c.count == 0 && coolingDone {
			delete(d.counters, k)
		}
	}
}

func domainOf(k string) string {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[i+1:]
		}
	}
	return k
}
