// Command telemetryd is the relay telemetry collector. It reads a Postfix maillog,
// parses each SMTP transaction into a structured event, and publishes it to the bus.
// If the bus is unavailable, events are spooled to disk and replayed on recovery, so
// relay mail flow never depends on MX Sentinel (docs/phase-1-plan.md WS4).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/internal/telemetry"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func main() {
	var (
		replay  = flag.String("replay", "", "read this maillog file once to EOF, then exit")
		follow  = flag.String("follow", "", "tail this maillog file continuously")
		nodeIP  = flag.String("node-ip", "", "this relay node's outbound IP (recorded as relay_ip)")
		spool   = flag.String("spool", "telemetry-spool.ndjson", "path to the disk spool")
		tenant  = flag.String("tenant", "", "fallback/static tenant id (UUID)")
		fbSlug  = flag.String("fallback-tenant-slug", "", "attribute mail from unregistered domains to this tenant (by slug) instead of dropping it — for a shared relay")
		skipDB  = flag.Bool("skip-db", false, "do not connect Postgres; requires -tenant for static resolution")
		drainEv = flag.Duration("drain-interval", 15*time.Second, "how often to retry the spool")
	)
	flag.Parse()

	if err := run(runOpts{
		replay: *replay, follow: *follow, nodeIP: *nodeIP, spool: *spool,
		tenant: *tenant, fallbackSlug: *fbSlug, skipDB: *skipDB, drainEvery: *drainEv,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "telemetryd:", err)
		os.Exit(1)
	}
}

type runOpts struct {
	replay, follow, nodeIP, spool, tenant, fallbackSlug string
	skipDB                                              bool
	drainEvery                                          time.Duration
}

func run(o runOpts) error {
	if o.replay == "" && o.follow == "" {
		return fmt.Errorf("one of -replay or -follow is required")
	}
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("telemetryd", cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := obs.NewMetrics()
	srv := obs.NewServer(cfg.HTTPAddr, metrics, log)
	srv.Start()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	validator, err := events.NewValidator()
	if err != nil {
		return err
	}
	var bus *events.Bus
	if err := obs.Retry(ctx, log, "nats", 20, 3*time.Second, func() (e error) {
		bus, e = events.Connect(cfg.NATS.URL, "telemetryd", validator, log)
		return
	}); err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	var resolver tenantResolver
	if err := obs.Retry(ctx, log, "postgres", 20, 3*time.Second, func() (e error) {
		resolver, e = buildResolver(ctx, o, cfg, log)
		return
	}); err != nil {
		return err
	}
	defer resolver.Close()

	c := &collector{
		log:      log,
		bus:      bus,
		spool:    telemetry.NewSpool(o.spool),
		resolver: resolver,
		node:     hostnameOr("relay"),
		parser:   telemetry.NewParser(time.Now().UTC().Year(), o.nodeIP, hashKey()),
	}
	srv.SetReady(true)

	// Background spool drainer.
	go c.drainLoop(ctx, o.drainEvery)

	if o.replay != "" {
		f, err := os.Open(o.replay)
		if err != nil {
			return err
		}
		defer f.Close()
		log.Info("replaying maillog", "file", o.replay)
		c.process(ctx, bufio.NewScanner(f))
		// Final spool drain so a replay leaves nothing buffered.
		c.drainOnce(ctx)
		log.Info("replay complete")
		return nil
	}

	log.Info("following maillog", "file", o.follow)
	return c.followFile(ctx, o.follow)
}

type collector struct {
	log      *slog.Logger
	bus      *events.Bus
	spool    *telemetry.Spool
	resolver tenantResolver
	node     string
	parser   *telemetry.Parser
}

// process feeds scanned lines through the parser and publishes resulting events.
func (c *collector) process(ctx context.Context, sc *bufio.Scanner) {
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		for _, ev := range c.parser.Parse(sc.Text()) {
			c.handle(ctx, ev)
		}
	}
}

func (c *collector) handle(ctx context.Context, ev telemetry.Event) {
	tenantID, ok := c.resolver.Resolve(ctx, ev.FromDomain)
	if !ok {
		c.log.Warn("no tenant for sending domain; dropping event", "from_domain", ev.FromDomain)
		return
	}
	env, err := events.NewEnvelope(ev.Type, tenantID, "telemetryd@"+c.node, ev.Correlation, ev.OccurredAt, ev.Payload)
	if err != nil {
		c.log.Error("build envelope", "err", err)
		return
	}
	if err := c.bus.Publish(ctx, env); err != nil {
		// Bus down or rejected: spool and keep mail flow telemetry durable.
		raw, merr := json.Marshal(env)
		if merr != nil {
			c.log.Error("marshal for spool", "err", merr)
			return
		}
		if serr := c.spool.Append(raw); serr != nil {
			c.log.Error("spool append failed", "err", serr)
		}
	}
}

func (c *collector) drainLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.drainOnce(ctx)
		}
	}
}

func (c *collector) drainOnce(ctx context.Context) {
	drained, remaining, err := c.spool.Drain(func(raw []byte) error {
		var env contracts.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil // unparseable spool line: drop it rather than loop forever
		}
		return c.bus.Publish(ctx, &env)
	})
	if err != nil {
		c.log.Error("spool drain failed", "err", err)
		return
	}
	if drained > 0 || remaining > 0 {
		c.log.Info("spool drained", "drained", drained, "remaining", remaining)
	}
}

// followFile tails a file, reading newly appended complete lines until ctx is cancelled.
// followFile tails a maillog with `tail -F` semantics: it reads newly appended lines and
// reopens the file when it is rotated or truncated. This matters because logrotate (and
// Postfix's postlogd on reload) replaces the log with a new inode — a plain tail of the
// original handle would then silently read nothing. For this to work the log's *directory*
// must be mounted into the container (not the single file), so reopening the path resolves
// the new inode (see deploy/docker-compose.yml).
func (c *collector) followFile(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { f.Close() }()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	openFI, _ := f.Stat()
	reader := bufio.NewReader(f)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var partial string
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for {
				line, err := reader.ReadString('\n')
				if err == io.EOF {
					partial += line // hold incomplete trailing line
					break
				}
				if err != nil {
					return err
				}
				full := partial + line
				partial = ""
				for _, ev := range c.parser.Parse(full) {
					c.handle(ctx, ev)
				}
			}
			// Rotation/truncation check: if the path now points at a different file, or the
			// file shrank below our read offset, reopen and read the new file from the start.
			st, serr := os.Stat(path)
			if serr != nil {
				continue // file briefly absent mid-rotation; retry next tick
			}
			pos, _ := f.Seek(0, io.SeekCurrent)
			if (openFI != nil && !os.SameFile(openFI, st)) || st.Size() < pos {
				nf, oerr := os.Open(path)
				if oerr != nil {
					continue
				}
				f.Close()
				f = nf
				openFI, _ = f.Stat()
				reader = bufio.NewReader(f)
				partial = ""
				c.log.Info("maillog rotated; reopened", "file", path)
			}
		}
	}
}

// ---- tenant resolution -----------------------------------------------------

type tenantResolver interface {
	Resolve(ctx context.Context, domain string) (string, bool)
	Close()
}

func buildResolver(ctx context.Context, o runOpts, cfg config.Config, log *slog.Logger) (tenantResolver, error) {
	if o.skipDB {
		if o.tenant == "" {
			return nil, fmt.Errorf("-skip-db requires -tenant")
		}
		return staticResolver{id: o.tenant}, nil
	}
	pg, err := pgstore.New(ctx, cfg.Postgres)
	if err != nil {
		return nil, err
	}
	fallback := o.tenant
	if o.fallbackSlug != "" {
		t, terr := pg.GetTenantBySlug(ctx, o.fallbackSlug)
		if terr != nil {
			pg.Close()
			return nil, fmt.Errorf("resolve fallback tenant slug %q: %w", o.fallbackSlug, terr)
		}
		fallback = t.ID
		log.Info("telemetry fallback tenant set", "slug", o.fallbackSlug, "tenant_id", t.ID)
	}
	return &pgResolver{pg: pg, fallback: fallback, log: log}, nil
}

type staticResolver struct{ id string }

func (s staticResolver) Resolve(context.Context, string) (string, bool) { return s.id, s.id != "" }
func (s staticResolver) Close()                                         {}

type pgResolver struct {
	pg       *pgstore.Store
	fallback string
	log      *slog.Logger
}

func (p *pgResolver) Resolve(ctx context.Context, domain string) (string, bool) {
	if domain != "" {
		if id, ok, err := p.pg.ResolveTenantByDomain(ctx, domain); err != nil {
			p.log.Warn("tenant lookup failed", "domain", domain, "err", err)
		} else if ok {
			return id, true
		}
	}
	if p.fallback != "" {
		return p.fallback, true
	}
	return "", false
}

func (p *pgResolver) Close() { p.pg.Close() }

func hashKey() []byte {
	if k := os.Getenv("MXS_TELEMETRY_HASHKEY"); k != "" {
		return []byte(k)
	}
	return nil
}

func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}
