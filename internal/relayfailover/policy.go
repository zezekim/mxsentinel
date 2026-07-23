package relayfailover

import (
	"fmt"
	"time"
)

// State is the circuit-breaker state for one provider.
type State string

const (
	// StateClosed is normal operation: mail delivers DIRECTLY to the provider. We watch the
	// transient-defer rate here.
	StateClosed State = "closed"
	// StateOpen is failover: the provider's mail is routed to the fallback smarthost. While
	// open we no longer send directly, so we get NO direct-defer telemetry — recovery is
	// therefore purely time-based (hold for Policy.HoldFor), never rate-based.
	StateOpen State = "open"
)

// Policy holds the circuit-breaker thresholds. All are env-tunable via LoadPolicy.
type Policy struct {
	// MinAttempts is the minimum number of delivery attempts in the window before the rate
	// is trusted — prevents a 1-of-1 defer from tripping the breaker on trivial volume.
	MinAttempts uint64
	// MinDefers is an absolute floor on 4xx defers in the window (belt-and-braces with rate).
	MinDefers uint64
	// TripRate is the transient-4xx-defer fraction of attempts that trips the breaker.
	TripRate float64
	// HoldFor is how long the breaker stays OPEN before auto-reverting to DIRECT to re-probe
	// the provider with real traffic (half-open). If it's still throttling, the next windows
	// trip it again.
	HoldFor time.Duration
	// MaxDomains caps how many recipient domains may be in failover at once — a blunt safety
	// valve so a misconfiguration can't dump unbounded traffic onto the fallback relay.
	MaxDomains int
}

// DefaultPolicy returns conservative defaults: needs sustained, high-rate transient defers
// on non-trivial volume before it trips, and reverts after 30m to re-probe.
func DefaultPolicy() Policy {
	return Policy{
		MinAttempts: 50,
		MinDefers:   20,
		TripRate:    0.60,
		HoldFor:     30 * time.Minute,
		MaxDomains:  25,
	}
}

// LoadPolicy reads Policy from the environment, falling back to DefaultPolicy per field.
func LoadPolicy() Policy {
	d := DefaultPolicy()
	return Policy{
		MinAttempts: parseUint("MXS_FAILOVER_MIN_ATTEMPTS", d.MinAttempts),
		MinDefers:   parseUint("MXS_FAILOVER_MIN_DEFERS", d.MinDefers),
		TripRate:    parseFloat("MXS_FAILOVER_TRIP_RATE", d.TripRate),
		HoldFor:     parseDuration("MXS_FAILOVER_HOLD", d.HoldFor),
		MaxDomains:  int(parseUint("MXS_FAILOVER_MAX_DOMAINS", uint64(d.MaxDomains))),
	}
}

// Sample is one window's worth of relay-wide delivery counts to the provider.
type Sample struct {
	Attempts    uint64
	Deferred4xx uint64
}

// DeferRate is the transient-4xx-defer fraction of attempts (0 when no attempts).
func (s Sample) DeferRate() float64 {
	if s.Attempts == 0 {
		return 0
	}
	return float64(s.Deferred4xx) / float64(s.Attempts)
}

// Breaker is the per-provider circuit-breaker state machine. It is pure: Evaluate takes the
// current time and a sample and returns the (possibly changed) state plus whether a
// transition occurred, with no side effects — the daemon owns I/O (state file, incidents).
type Breaker struct {
	State     State
	OpenedAt  time.Time // when State last became Open
	trippedBy Sample    // the sample that tripped it (for incident detail)
}

// NewBreaker returns a closed breaker.
func NewBreaker() *Breaker { return &Breaker{State: StateClosed} }

// Transition describes the result of an Evaluate call.
type Transition struct {
	From, To State
	Changed  bool
	Reason   string
}

// Evaluate advances the breaker one tick.
//
//   - CLOSED: trips to OPEN when the sample shows a sustained, high-rate transient-defer
//     storm (attempts >= MinAttempts AND defers >= MinDefers AND rate >= TripRate).
//   - OPEN: auto-reverts to CLOSED once HoldFor has elapsed since OpenedAt (half-open probe).
//     The sample is ignored while open because failed-over mail produces no direct telemetry.
func (b *Breaker) Evaluate(now time.Time, s Sample, p Policy) Transition {
	from := b.State
	switch b.State {
	case StateOpen:
		if now.Sub(b.OpenedAt) >= p.HoldFor {
			b.State = StateClosed
			b.trippedBy = Sample{}
			return Transition{From: from, To: StateClosed, Changed: true,
				Reason: fmt.Sprintf("held failover for %s; reverting to direct to re-probe provider", p.HoldFor)}
		}
		return Transition{From: from, To: StateOpen, Changed: false,
			Reason: fmt.Sprintf("failover held %s/%s", now.Sub(b.OpenedAt).Round(time.Second), p.HoldFor)}

	default: // StateClosed
		if s.Attempts >= p.MinAttempts && s.Deferred4xx >= p.MinDefers && s.DeferRate() >= p.TripRate {
			b.State = StateOpen
			b.OpenedAt = now
			b.trippedBy = s
			return Transition{From: from, To: StateOpen, Changed: true,
				Reason: fmt.Sprintf("transient 4xx defers %d/%d (%.0f%%) exceed trip rate %.0f%%",
					s.Deferred4xx, s.Attempts, s.DeferRate()*100, p.TripRate*100)}
		}
		return Transition{From: from, To: StateClosed, Changed: false, Reason: "healthy"}
	}
}

// TrippedBy returns the sample that opened the breaker (zero value when closed).
func (b *Breaker) TrippedBy() Sample { return b.trippedBy }
