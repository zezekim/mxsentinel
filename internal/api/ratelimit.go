package api

import (
	"context"
	"net/http"
)

// Limiter throttles requests by key (we key on tenant). *ratelimit.Limiter satisfies it.
type Limiter interface {
	Allow(ctx context.Context, key string) (allowed bool, count int64, err error)
}

// rateLimit throttles per authenticated tenant. It runs after requireAuth (needs the
// tenant) and is a no-op when no limiter is configured. On limiter errors it fails open.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		a, ok := authFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r) // unauthenticated requests are handled by requireAuth
			return
		}
		allowed, _, err := s.limiter.Allow(r.Context(), a.TenantID)
		if err != nil {
			s.log.Warn("rate limiter error; allowing request", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded; retry later")
			return
		}
		next.ServeHTTP(w, r)
	})
}
