// Package obs provides shared observability bootstrap: structured logging (slog) and a
// small HTTP server exposing /healthz and /metrics (Prometheus). Every service wires
// these the same way.
package obs

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewLogger returns a JSON slog.Logger at the given level ("debug"|"info"|"warn"|
// "error"), annotated with the service name. Defaults to info on unknown levels.
func NewLogger(service, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h).With("service", service)
}

// Metrics holds a service-local Prometheus registry. Services register their own
// collectors on Registry.
type Metrics struct {
	Registry *prometheus.Registry
}

// NewMetrics creates a registry pre-loaded with the Go and process collectors.
func NewMetrics() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	return &Metrics{Registry: r}
}

// ready is flipped to 1 once a service declares itself ready (e.g. after connecting to
// its backing stores). /healthz reports 503 until then.
type healthState struct{ ready atomic.Bool }

// Server is the shared health+metrics HTTP server.
type Server struct {
	http   *http.Server
	health *healthState
	log    *slog.Logger
}

// NewServer builds the observability HTTP server bound to addr.
func NewServer(addr string, m *Metrics, log *slog.Logger) *Server {
	hs := &healthState{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if hs.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	return &Server{
		http:   &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		health: hs,
		log:    log,
	}
}

// SetReady marks the service ready (or not) for /healthz.
func (s *Server) SetReady(ready bool) { s.health.ready.Store(ready) }

// Start runs the HTTP server in a goroutine. Errors other than ErrServerClosed are logged.
func (s *Server) Start() {
	go func() {
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("obs server failed", "err", err)
		}
	}()
	s.log.Info("obs server listening", "addr", s.http.Addr)
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
