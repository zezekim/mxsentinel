package cpanelplugin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ctxKey is the private context key under which the broker stashes the peer uid
// resolved at connection-accept time.
type ctxKey struct{}

// DomainView is one domain plus its health payload in a summary response.
type DomainView struct {
	DomainItem
	Health json.RawMessage `json:"health,omitempty"`
}

// Summary is the single payload the dashboard renders. Its shape depends on scope:
// admin (uid 0) gets the whole tenant plus relay-wide signals; a user scope gets only
// its own domains and incidents, and never the tenant-wide aggregates.
type Summary struct {
	Mode           string          `json:"mode"` // "admin" | "user"
	Username       string          `json:"username,omitempty"`
	GeneratedAt    string          `json:"generated_at"`
	APIBase        string          `json:"api_base"`
	Domains        []DomainView    `json:"domains"`
	Incidents      []Incident      `json:"incidents"`
	IPHealth       json.RawMessage `json:"ip_health,omitempty"`      // admin only
	Deliverability json.RawMessage `json:"deliverability,omitempty"` // admin only
	Notice         string          `json:"notice,omitempty"`
	Error          string          `json:"error,omitempty"`
}

// Broker is the root-owned daemon.
type Broker struct {
	cfg   Config
	log   *slog.Logger
	up    *upstream
	scope *scopeResolver
}

// RunDaemon loads config and runs the broker until the process is signalled.
func RunDaemon(configPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{})).With("service", "mxsentinel-plugin")
	b := &Broker{cfg: cfg, log: log, up: newUpstream(cfg), scope: newScopeResolver(cfg)}
	return b.Serve(context.Background())
}

// Serve binds the unix socket and serves requests until ctx is cancelled.
func (b *Broker) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(b.cfg.SocketPath), 0o755); err != nil {
		return err
	}
	// Replace any stale socket from a previous run.
	if err := os.Remove(b.cfg.SocketPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", b.cfg.SocketPath)
	if err != nil {
		return err
	}
	// World-connectable: scope is enforced by peer uid, not by who can dial.
	if err := os.Chmod(b.cfg.SocketPath, 0o666); err != nil {
		b.log.Warn("chmod socket", "err", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /summary", b.handleSummary)

	srv := &http.Server{
		Handler:     mux,
		ReadTimeout: 15 * time.Second,
		// Resolve the peer uid once per connection and carry it on the request ctx.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			uid, err := peerUID(c)
			if err != nil {
				b.log.Warn("peer uid", "err", err)
				return ctx
			}
			return context.WithValue(ctx, ctxKey{}, uid)
		},
	}

	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	b.log.Info("broker listening", "socket", b.cfg.SocketPath, "api_base", b.cfg.APIBase)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (b *Broker) handleSummary(w http.ResponseWriter, r *http.Request) {
	uid, ok := r.Context().Value(ctxKey{}).(uint32)
	if !ok {
		writeJSON(w, http.StatusForbidden, Summary{Error: "could not determine caller identity"})
		return
	}

	var sum Summary
	if uid == 0 {
		sum = b.adminSummary(r.Context())
	} else {
		sum = b.userSummary(r.Context(), uid)
	}
	sum.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	sum.APIBase = b.cfg.APIBase
	writeJSON(w, http.StatusOK, sum)
}

// adminSummary (uid 0 / WHM root) returns the whole tenant plus relay-wide signals.
func (b *Broker) adminSummary(ctx context.Context) Summary {
	sum := Summary{Mode: "admin"}

	domains, err := b.up.ListDomains(ctx)
	if err != nil {
		sum.Error = err.Error()
		return sum
	}
	sum.Domains = b.attachHealth(ctx, domains)

	if inc, err := b.up.ListIncidents(ctx, "open", 100); err != nil {
		b.log.Warn("admin incidents", "err", err)
	} else {
		sum.Incidents = inc
	}
	if ip, err := b.up.RBLStatus(ctx); err != nil {
		b.log.Warn("admin rbl status", "err", err)
	} else {
		sum.IPHealth = ip
	}
	if d, err := b.up.Deliverability(ctx); err != nil {
		b.log.Warn("admin deliverability", "err", err)
	} else {
		sum.Deliverability = d
	}
	return sum
}

// userSummary (any non-root uid) returns only the connecting account's domains and
// incidents. It never includes tenant-wide aggregates (deliverability / IP health).
func (b *Broker) userSummary(ctx context.Context, uid uint32) Summary {
	sum := Summary{Mode: "user"}

	username := usernameForUID(uid)
	sum.Username = username
	if username == "" {
		sum.Error = "could not map your session to a cPanel account"
		return sum
	}

	owned := b.scope.domainsForUser(username)
	if len(owned) == 0 {
		sum.Notice = "No domains on this account are being monitored by MX Sentinel yet."
		return sum
	}

	all, err := b.up.ListDomains(ctx)
	if err != nil {
		sum.Error = err.Error()
		return sum
	}
	mine := make([]DomainItem, 0, len(owned))
	monitored := map[string]bool{}
	for _, d := range all {
		if owned[strings.ToLower(d.Name)] {
			mine = append(mine, d)
			monitored[strings.ToLower(d.Name)] = true
		}
	}
	sum.Domains = b.attachHealth(ctx, mine)

	if len(mine) == 0 {
		sum.Notice = "Your domains are not yet onboarded into MX Sentinel. Ask your hosting provider to add them."
		return sum
	}

	// Incidents: fetch all and keep only those for domains this account owns.
	if inc, err := b.up.ListIncidents(ctx, "open", 200); err != nil {
		b.log.Warn("user incidents", "err", err)
	} else {
		for _, i := range inc {
			if monitored[strings.ToLower(i.Domain)] {
				sum.Incidents = append(sum.Incidents, i)
			}
		}
	}
	return sum
}

// attachHealth fetches per-domain health and pairs it with each domain item.
func (b *Broker) attachHealth(ctx context.Context, domains []DomainItem) []DomainView {
	views := make([]DomainView, 0, len(domains))
	for _, d := range domains {
		v := DomainView{DomainItem: d}
		if h, err := b.up.DomainHealth(ctx, d.ID); err != nil {
			b.log.Warn("domain health", "domain", d.Name, "err", err)
		} else {
			v.Health = h
		}
		views = append(views, v)
	}
	return views
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
