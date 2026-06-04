package obs

import (
	"context"
	"log/slog"
	"time"
)

// Retry calls fn until it returns nil or attempts are exhausted, waiting delay between
// tries and honoring ctx cancellation. It's used at daemon startup so a transient
// dependency outage — e.g. ClickHouse or NATS not yet resolvable during a compose
// (re)start — is waited out instead of crashing the process into a restart loop. Returns
// the final error if every attempt fails (the caller can then exit and let the restart
// policy take over).
func Retry(ctx context.Context, log *slog.Logger, dependency string, attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := 1; i <= attempts; i++ {
		if err = fn(); err == nil {
			if i > 1 && log != nil {
				log.Info("dependency ready", "dependency", dependency, "after_attempts", i)
			}
			return nil
		}
		if log != nil {
			log.Warn("dependency not ready; retrying", "dependency", dependency, "attempt", i, "attempts", attempts, "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}
