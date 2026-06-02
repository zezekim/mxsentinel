// Command dmarcd ingests DMARC aggregate reports. It watches a drop directory for report
// files (.xml/.xml.gz/.zip), archives each raw report to object storage, parses it, writes
// a pointer row (Postgres) and per-source records (ClickHouse), and quarantines malformed
// reports instead of crashing. See docs/phase-1-plan.md WS5.
//
// The drop-directory source is the M3 implementation; IMAP / S3-bucket sources are thin
// fetchers that can feed the same Ingestor later.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/dmarc"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	"github.com/zezekim/mxsentinel/internal/store/objectstore"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

const (
	subProcessed  = "processed"
	subQuarantine = "quarantine"
)

func main() {
	var (
		dir      = flag.String("dir", "", "drop directory to watch for report files")
		file     = flag.String("file", "", "ingest a single file once, then exit")
		interval = flag.Duration("interval", 30*time.Second, "drop-directory scan interval")
	)
	flag.Parse()

	if err := run(*dir, *file, *interval); err != nil {
		fmt.Fprintln(os.Stderr, "dmarcd:", err)
		os.Exit(1)
	}
}

func run(dir, file string, interval time.Duration) error {
	if dir == "" && file == "" {
		return fmt.Errorf("one of -dir or -file is required")
	}
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("dmarcd", cfg.LogLevel)

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

	pg, err := pgstore.New(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pg.Close()

	ch, err := chstore.New(ctx, cfg.ClickHouse)
	if err != nil {
		return err
	}
	defer ch.Close()

	obj, err := objectstore.New(ctx, cfg.ObjectStore)
	if err != nil {
		return err
	}
	if err := obj.EnsureBucket(ctx, cfg.ObjectStore.Region); err != nil {
		return err
	}

	ing := dmarc.NewIngestor(obj, pg, ch)
	d := &daemon{log: log, ing: ing}
	srv.SetReady(true)

	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		res, err := ing.IngestFile(ctx, filepath.Base(file), data)
		if err != nil {
			return err
		}
		log.Info("ingested file", resultAttrs(res)...)
		return nil
	}

	log.Info("dmarcd watching drop directory", "dir", dir, "interval", interval.String())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	d.scan(ctx, dir)
	for {
		select {
		case <-ctx.Done():
			log.Info("dmarcd shutting down")
			return nil
		case <-ticker.C:
			d.scan(ctx, dir)
		}
	}
}

type daemon struct {
	log *slog.Logger
	ing *dmarc.Ingestor
}

func (d *daemon) scan(ctx context.Context, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.log.Error("read drop dir", "dir", dir, "err", err)
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !isReportFile(e.Name()) {
			continue
		}
		d.processFile(ctx, dir, e.Name())
	}
}

func (d *daemon) processFile(ctx context.Context, dir, name string) {
	full := filepath.Join(dir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		d.log.Error("read report", "file", name, "err", err)
		return
	}

	res, err := d.ing.IngestFile(ctx, name, data)
	if err != nil {
		// Infrastructure error (DB/object store) — leave the file in place to retry.
		d.log.Error("ingest failed; will retry", "file", name, "err", err)
		return
	}

	dest := subProcessed
	if res.Status == dmarc.StatusQuarantined || res.Status == dmarc.StatusNoTenant {
		dest = subQuarantine
	}
	d.log.Info("processed report", append([]any{"file", name}, resultAttrs(res)...)...)
	if mErr := moveTo(dir, dest, name); mErr != nil {
		d.log.Warn("move processed file", "file", name, "err", mErr)
	}
}

func moveTo(dir, sub, name string) error {
	dstDir := filepath.Join(dir, sub)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, name), filepath.Join(dstDir, name))
}

func isReportFile(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".xml") || strings.HasSuffix(n, ".xml.gz") ||
		strings.HasSuffix(n, ".gz") || strings.HasSuffix(n, ".zip")
}

func resultAttrs(res dmarc.Result) []any {
	attrs := []any{"status", string(res.Status), "report_id", res.ReportID, "domain", res.Domain, "records", res.Records}
	if res.Err != nil {
		attrs = append(attrs, "reason", res.Err.Error())
	}
	return attrs
}
