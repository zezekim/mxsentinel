package cpanelplugin

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// renew.go keeps the broker's API credential alive. Since self-enrollment the plugin
// holds a narrow read+relay key that expires after a year instead of a never-expiring
// admin token, and nothing else on the box renews it — without this loop a server would
// silently start 401ing on its own anniversary, long after whoever installed it moved on.

// renewPolicy holds the loop's timings. Defaults are the production values; tests
// compress them.
type renewPolicy struct {
	interval    time.Duration // how often to re-check the expiry
	threshold   time.Duration // renew once less than this is left
	firstJitter time.Duration // spread the first check across a fleet
	retryBase   time.Duration // first delay after a transient failure (doubles)
	retryMax    time.Duration // ceiling for that doubling
	writeRetry  time.Duration // delay after a failed config write (see renewWriteFailed)
	deadBackoff time.Duration // floor for a token the server has rejected outright
	timeout     time.Duration // per-attempt HTTP budget
}

func defaultRenewPolicy() renewPolicy {
	return renewPolicy{
		interval:    12 * time.Hour,
		threshold:   30 * 24 * time.Hour,
		firstJitter: 15 * time.Minute,
		retryBase:   5 * time.Minute,
		retryMax:    6 * time.Hour,
		// Must stay comfortably inside the server's 15-minute grace window: each retry
		// mints a fresh token and re-arms that window, so a slow retry is a wasted one.
		writeRetry:  90 * time.Second,
		deadBackoff: 6 * time.Hour,
		timeout:     30 * time.Second,
	}
}

// renewOutcome is what one check learned, expressed as a scheduling decision.
type renewOutcome int

const (
	renewOK          renewOutcome = iota // checked (and renewed if it was due)
	renewStop                            // nothing to renew, ever — the loop should exit
	renewRetry                           // transient failure, back off and try again
	renewWriteFailed                     // renewed upstream but could not persist it
	renewDead                            // the server rejected the token; an operator must act
)

func (b *Broker) renewalPolicy() renewPolicy {
	if b.renew.interval <= 0 {
		return defaultRenewPolicy()
	}
	return b.renew
}

// runTokenRenewal keeps the API credential from expiring. Start it with `go` from Serve:
// it shares the server's context and stops on shutdown — or permanently, the moment it
// learns the credential has no expiry at all.
func (b *Broker) runTokenRenewal(ctx context.Context) {
	p := b.renewalPolicy()

	// Jitter the first check. cPanel fleets are routinely cloned from one image, so
	// without this every server on it would wake and call apid in the same second.
	var wait time.Duration
	if p.firstJitter > 0 {
		wait = rand.N(p.firstJitter)
	}
	t := time.NewTimer(wait)
	defer t.Stop()

	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		var next time.Duration
		switch b.renewOnce(ctx) {
		case renewStop:
			return
		case renewOK:
			fails = 0
			next = p.interval
		case renewWriteFailed:
			// Retry soon and without backing off: the rotation already happened upstream,
			// so the token we are still using is inside its grace window. Retrying re-runs
			// the whole renew, which mints another token and re-arms the window; waiting
			// is the only move that can actually lose.
			next = p.writeRetry
		case renewDead:
			// A rejected token cannot be un-rejected by asking again. Stay in the loop
			// (an operator may fix the config under us) but at a crawl.
			fails++
			next = max(backoffDelay(p, fails), p.deadBackoff)
		default: // renewRetry
			fails++
			next = backoffDelay(p, fails)
		}
		t.Reset(next)
	}
}

// renewOnce performs one expiry check, renewing if the credential is close enough to the
// end of its life. It returns no error: every failure maps to a scheduling decision.
func (b *Broker) renewOnce(ctx context.Context) renewOutcome {
	p := b.renewalPolicy()
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	me, err := b.up.Me(cctx)
	if err != nil {
		if statusOf(err) == http.StatusUnauthorized {
			b.logDeadToken("expiry check", err)
			return renewDead
		}
		b.log.Warn("token renewal: expiry check failed", "err", err)
		return renewRetry
	}

	exp, ok, err := me.Expiry()
	if err != nil {
		b.log.Warn("token renewal: unreadable expires_at", "err", err)
		return renewRetry
	}
	if !ok {
		// A pre-enrollment install pasted a never-expiring admin token. There is nothing
		// to renew, so say so once and stop rather than waking up twice a day forever.
		b.log.Info("token renewal: credential never expires, renewal disabled", "credential", me.CredentialName)
		return renewStop
	}

	remaining := time.Until(exp)
	if remaining > p.threshold {
		b.log.Debug("token renewal: not due yet", "credential", me.CredentialName,
			"expires_at", exp.UTC().Format(time.RFC3339), "days_left", int(remaining.Hours()/24))
		return renewOK
	}
	if remaining <= 0 {
		b.log.Warn("token renewal: credential has already expired, attempting renewal anyway",
			"credential", me.CredentialName, "expires_at", exp.UTC().Format(time.RFC3339))
	}

	key, err := b.up.RenewToken(cctx)
	if err != nil {
		switch statusOf(err) {
		case http.StatusUnauthorized:
			b.logDeadToken("renew", err)
			return renewDead
		case http.StatusBadRequest:
			// The server says this credential cannot be renewed even though /v1/me
			// reported an expiry. A disagreement like that is not something a retry
			// resolves, so treat it as operator-visible and stop hammering.
			b.log.Error("token renewal: server refused to renew this credential — re-enrol this server: install.sh --enroll-token <provision token>",
				"credential", me.CredentialName, "err", err)
			return renewDead
		}
		b.log.Warn("token renewal: renew call failed", "credential", me.CredentialName, "err", err)
		return renewRetry
	}
	if key.Token == "" {
		b.log.Warn("token renewal: server returned no token", "credential", key.Name)
		return renewRetry
	}

	// ORDER IS THE WHOLE GAME: persist to disk first, swap into memory second.
	//
	// The renew call has already replaced the secret upstream; the old one now works for
	// a 15-minute grace window and no longer. Swapping first and then failing to write
	// would leave the running process healthy but plugin.conf holding the old secret — and
	// the next restart or crash would load a token that dies within minutes, locking the
	// server out with no way back but a manual re-enrol. Persisting first inverts that:
	// the worst case is disk-new/memory-old, where a restart picks up the new token and,
	// failing that, the next tick renews again inside the grace window. One ordering has a
	// recoverable failure mode, the other has a permanent one.
	if err := WriteToken(b.cfg.Path, key.Token); err != nil {
		b.log.Error("token renewal: could not persist the new token, keeping the old one",
			"path", b.cfg.Path, "prefix", key.Prefix, "err", err)
		return renewWriteFailed
	}
	b.up.SetToken(key.Token)

	b.log.Info("token renewal: rotated API credential", "credential", key.Name,
		"prefix", key.Prefix, "expires_at", key.ExpiresAt)
	return renewOK
}

// backoffDelay doubles retryBase per consecutive failure, capped at retryMax.
func backoffDelay(p renewPolicy, fails int) time.Duration {
	if fails < 1 {
		fails = 1
	}
	d := p.retryBase
	for i := 1; i < fails && d < p.retryMax; i++ {
		d *= 2
	}
	return min(d, p.retryMax)
}

// logDeadToken reports a token the server no longer accepts. This is the one failure a
// retry cannot fix, so it has to be loud enough to find in a log: the box is locked out
// until someone re-enrols it.
func (b *Broker) logDeadToken(stage string, err error) {
	b.log.Error("token renewal: API token rejected — this server is locked out until it is re-enrolled: re-run plugins/cpanel/install.sh with --enroll-token",
		"stage", stage, "prefix", tokenPrefix(b.up.Token()), "err", err)
}

// tokenPrefix reduces a token to its public "mxs_xxxxxxxx" half. The secret must never
// reach a log line, but the prefix is exactly what identifies the key in apid.
func tokenPrefix(token string) string {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "_" + parts[1]
	}
	return "unknown"
}
