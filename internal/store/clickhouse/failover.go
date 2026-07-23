package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// ProviderDeferStats holds relay-wide (all-tenant) delivery-attempt counts to a single
// receiver provider over a time window, used by relayfailoverd to decide whether that
// provider is sustainedly throttling us. Failover is a relay-level decision — the Postfix
// transport map it drives is global to the box — so these counts deliberately span every
// tenant rather than one.
type ProviderDeferStats struct {
	Provider     string
	Attempts     uint64 // delivered + deferred + bounced + rejected (i.e. real delivery attempts)
	Deferred4xx  uint64 // deferred events with a transient 4xx SMTP code
	SpamRepBlock uint64 // deferred/bounced/rejected attributed to spam/reputation (context only)
}

// DeferRate returns the fraction of attempts that were transient 4xx defers (0 when there
// were no attempts).
func (s ProviderDeferStats) DeferRate() float64 {
	if s.Attempts == 0 {
		return 0
	}
	return float64(s.Deferred4xx) / float64(s.Attempts)
}

// ProviderDeferStats returns relay-wide delivery-attempt counts to provider since the given
// time. "Transient 4xx defers" are the only signal that safely warrants rerouting to a
// fallback relay: a persistent 5xx spam/reputation block would just launder the same
// reputation problem through the fallback's IPs, so those are counted separately (for
// context/incident detail) and never drive a failover.
func (s *Store) ProviderDeferStats(ctx context.Context, provider string, since time.Time) (ProviderDeferStats, error) {
	const q = `
		SELECT
		    countIf(event_type IN ('delivered','deferred','bounced','rejected'))          AS attempts,
		    countIf(event_type = 'deferred' AND smtp_code >= 400 AND smtp_code < 500)      AS deferred_4xx,
		    countIf(event_type IN ('deferred','bounced','rejected')
		            AND bounce_class IN ('spam','reputation'))                             AS spam_rep
		FROM smtp_events
		WHERE provider = ? AND event_time >= ?`
	var out ProviderDeferStats
	out.Provider = provider
	row := s.conn.QueryRow(ctx, q, provider, since)
	if err := row.Scan(&out.Attempts, &out.Deferred4xx, &out.SpamRepBlock); err != nil {
		return ProviderDeferStats{}, fmt.Errorf("provider defer stats (%s): %w", provider, err)
	}
	return out, nil
}
