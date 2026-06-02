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
		skipDB  = flag.Bool("skip-db", false, "do not connect Postgres; requires -tenant for static resolution")
		drainEv = flag.Duration("drain-interval", 15*time.Second, "how often to retry the spool")
	)
	flag.Parse()

	if err := run(runOpts{
		replay: *replay, follow: *follow, nodeIP: *nodeIP, spool: *spool,
		tenant: *tenant, skipDB: *skipDB, drainEvery: *drainEv,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "telemetryd:", err)
		os.Exit(1)
	}
}

type runOpts struct {
	replay, follow, nodeIP, spool, tenant string
	skipDB                                bool
	drainEvery                            time.Duration
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
	bus, err := events.Connect(cfg.NATS.URL, "telemetryd", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	resolver, err := buildResolver(ctx, o, cfg, log)
	if err != nil {
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
func (c *collector) followFile(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
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
	return &pgResolver{pg: pg, fallback: o.tenant, log: log}, nil
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
