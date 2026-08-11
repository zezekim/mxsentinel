package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/internal/auth"
)

// chain applies middleware in order (first listed runs outermost).
func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// recoverer turns panics into 500s instead of crashing the server.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "err", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs each request with method, path, status, and duration.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"dur_ms", time.Since(start).Milliseconds())
	})
}

// cors applies permissive (configurable-origin) CORS and short-circuits preflights.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth resolves the tenant from the Bearer API token and attaches it to context.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}

		// User session token (from login) — scopes derive from the user's role.
		if strings.HasPrefix(token, auth.SessionPrefix) {
			if s.sessions == nil {
				writeError(w, http.StatusUnauthorized, "invalid_token", "sessions not enabled")
				return
			}
			sess, found, err := s.sessions.Resolve(r.Context(), token)
			if err != nil {
				s.log.Error("session lookup failed", "err", err)
				writeError(w, http.StatusInternalServerError, "internal", "auth lookup failed")
				return
			}
			if !found {
				writeError(w, http.StatusUnauthorized, "invalid_token", "invalid or expired session")
				return
			}
			ctx := withAuth(r.Context(), AuthInfo{
				TenantID: sess.TenantID, UserID: sess.UserID, Role: sess.Role,
				Scopes: auth.ScopesForRole(sess.Role),
			})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Otherwise an API credential token.
		prefix := PrefixOf(token)
		if prefix == "" {
			writeError(w, http.StatusUnauthorized, "invalid_token", "malformed token")
			return
		}
		cred, found, err := s.pg.GetAPICredentialByPrefix(r.Context(), prefix)
		if err != nil {
			s.log.Error("credential lookup failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "auth lookup failed")
			return
		}
		if !found || !tokenMatches(token, cred.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid_token", "invalid token")
			return
		}
		_ = s.pg.TouchAPICredential(r.Context(), cred.ID) // best-effort last-used stamp
		ctx := withAuth(r.Context(), AuthInfo{
			TenantID: cred.TenantID, CredID: cred.ID, CredName: cred.Name,
			CredExpiresAt: cred.ExpiresAt, Scopes: cred.Scopes,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearer(header string) (string, bool) {
	const p = "Bearer "
	if len(header) > len(p) && strings.EqualFold(header[:len(p)], p) {
		return strings.TrimSpace(header[len(p):]), true
	}
	return "", false
}

// statusWriter captures the response status for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
