package correlate

import (
	"sync"
	"time"
)

// Key identifies a deliverability stream to watch for spikes.
type Key struct {
	TenantID string
	Domain   string
	Provider string
}

// bucket holds one minute of counts for a key.
type bucket struct {
	total      int
	rejections int
	reasons    map[ReasonCategory]int
}

// Aggregator maintains per-key, per-minute delivery/rejection counts in memory so the
// streaming engine (cmd/correld) can detect rejection spikes over sliding windows. It is
// safe for concurrent use.
type Aggregator struct {
	mu   sync.Mutex
	data map[Key]map[int64]*bucket // key -> unix-minute -> bucket
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{data: make(map[Key]map[int64]*bucket)}
}

func unixMinute(t time.Time) int64 { return t.Unix() / 60 }

// Observe records one delivery outcome for a key at time t. rejected marks failures
// (bounced/rejected/deferred); reason is its classified category (ignored when delivered).
func (a *Aggregator) Observe(k Key, t time.Time, rejected bool, reason ReasonCategory) {
	a.mu.Lock()
	defer a.mu.Unlock()

	mins := a.data[k]
	if mins == nil {
		mins = make(map[int64]*bucket)
		a.data[k] = mins
	}
	m := unixMinute(t)
	b := mins[m]
	if b == nil {
		b = &bucket{reasons: make(map[ReasonCategory]int)}
		mins[m] = b
	}
	b.total++
	if rejected {
		b.rejections++
		if reason != "" {
			b.reasons[reason]++
		}
	}
}

// Evaluate scans all keys and returns a Spike for each that is currently spiking. The
// "recent" window is the last recentMin minutes ending at now; the baseline is the
// baselineMin minutes immediately before that.
func (a *Aggregator) Evaluate(now time.Time, recentMin, baselineMin int, cfg SpikeConfig) []Spike {
	a.mu.Lock()
	defer a.mu.Unlock()

	nowMin := unixMinute(now)
	recentFloor := nowMin - int64(recentMin)
	baseFloor := recentFloor - int64(baselineMin)

	var spikes []Spike
	for k, mins := range a.data {
		var recent, baseline Window
		reasons := make(map[ReasonCategory]int)
		for m, b := range mins {
			switch {
			case m > recentFloor && m <= nowMin:
				recent.Total += b.total
				recent.Rejections += b.rejections
				for r, c := range b.reasons {
					reasons[r] += c
				}
			case m > baseFloor && m <= recentFloor:
				baseline.Total += b.total
				baseline.Rejections += b.rejections
			}
		}
		if res := DetectSpike(recent, baseline, cfg); res.IsSpike {
			spikes = append(spikes, Spike{
				TenantID:       k.TenantID,
				Domain:         k.Domain,
				Provider:       k.Provider,
				WindowStart:    time.Unix(recentFloor*60, 0).UTC(),
				WindowEnd:      now.UTC(),
				DominantReason: dominantReason(reasons),
				Rejections:     recent.Rejections,
			})
		}
	}
	return spikes
}

// Prune drops buckets older than keepMin minutes to bound memory.
func (a *Aggregator) Prune(now time.Time, keepMin int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	floor := unixMinute(now) - int64(keepMin)
	for k, mins := range a.data {
		for m := range mins {
			if m < floor {
				delete(mins, m)
			}
		}
		if len(mins) == 0 {
			delete(a.data, k)
		}
	}
}

func dominantReason(reasons map[ReasonCategory]int) ReasonCategory {
	best := ReasonUnknown
	bestN := 0
	for r, n := range reasons {
		if n > bestN {
			best, bestN = r, n
		}
	}
	return best
}
