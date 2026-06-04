// Command ingestd persists SMTP telemetry into ClickHouse. telemetryd publishes
// smtp.* events to the bus (and spools to disk if the bus is down); ingestd is the
// consumer that durably lands them in the smtp_events table so the Message Explorer and
// per-account history are queryable. It buffers and batch-inserts (ClickHouse strongly
// prefers batches over per-row inserts), flushing on size or a short interval.
//
// Telemetry is best-effort analytics (not billing): messages are acked on receipt and a
// failed batch is retried a few times then dropped with a warning, rather than blocking
// the stream. See docs/data-model.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

var (
	flushEvery = flag.Duration("flush", 5*time.Second, "max time a buffered event waits before being written")
	batchSize  = flag.Int("batch", 500, "flush when this many events are buffered")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ingestd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("ingestd", cfg.LogLevel)

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

	var ch *chstore.Store
	if err := obs.Retry(ctx, log, "clickhouse", 20, 3*time.Second, func() (e error) {
		ch, e = chstore.New(ctx, cfg.ClickHouse)
		return
	}); err != nil {
		return err
	}
	defer ch.Close()

	validator, err := events.NewValidator()
	if err != nil {
		return err
	}
	var bus *events.Bus
	if err := obs.Retry(ctx, log, "nats", 20, 3*time.Second, func() (e error) {
		bus, e = events.Connect(cfg.NATS.URL, "ingestd", validator, log)
		return
	}); err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	w := &writer{log: log, ch: ch}

	cc, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "SMTP", Durable: "ingestd", FilterSubject: "mxs.smtp.>",
	}, w.onSMTP)
	if err != nil {
		return err
	}
	defer cc.Stop()

	srv.SetReady(true)
	log.Info("ingestd started", "batch", *batchSize, "flush", flushEvery.String())

	ticker := time.NewTicker(*flushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("ingestd shutting down; flushing")
			w.flush(context.Background())
			return nil
		case <-ticker.C:
			w.flush(ctx)
		}
	}
}

type writer struct {
	log *slog.Logger
	ch  *chstore.Store

	mu  sync.Mutex
	buf []chstore.SMTPEventRow
}

// onSMTP buffers a parsed SMTP event; a full buffer flushes immediately.
func (w *writer) onSMTP(ctx context.Context, ev *contracts.Envelope) error {
	row, ok := rowFromEnvelope(ev)
	if !ok {
		return nil // malformed: drop, don't redeliver forever
	}
	w.mu.Lock()
	w.buf = append(w.buf, row)
	full := len(w.buf) >= *batchSize
	w.mu.Unlock()
	if full {
		w.flush(ctx)
	}
	return nil
}

// flush writes the buffered rows to ClickHouse, retrying briefly before dropping (telemetry
// is best-effort analytics, so a persistent CH outage must not wedge the consumer).
func (w *writer) flush(ctx context.Context) {
	w.mu.Lock()
	if len(w.buf) == 0 {
		w.mu.Unlock()
		return
	}
	rows := w.buf
	w.buf = nil
	w.mu.Unlock()

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = w.ch.InsertSMTPEvents(ctx, rows); err == nil {
			return
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	w.log.Error("dropping smtp_events batch after retries", "rows", len(rows), "err", err)
}

// rowFromEnvelope maps an event envelope + SMTP payload to a ClickHouse row.
func rowFromEnvelope(ev *contracts.Envelope) (chstore.SMTPEventRow, bool) {
	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		return chstore.SMTPEventRow{}, false
	}
	r := chstore.SMTPEventRow{
		EventID:           ev.EventID,
		EventTime:         parseTime(ev.OccurredAt),
		IngestedAt:        parseTime(ev.IngestedAt),
		TenantID:          ev.TenantID,
		Source:            ev.Source,
		EventType:         p.Outcome, // 'received'|'delivered'|'deferred'|'bounced'|'rejected'
		BounceClass:       enumOrNone(p.BounceClass),
		MessageID:         p.MessageID,
		QueueID:           p.QueueID,
		SessionID:         p.SessionID,
		SourceIP:          net.ParseIP(p.SourceIP),
		RelayIP:           net.ParseIP(p.RelayIP),
		IPPool:            p.IPPool,
		DKIMSelector:      p.DKIMSelector,
		DKIMDomain:        p.DKIMDomain,
		EnvelopeFrom:      p.EnvelopeFrom,
		FromDomain:        p.FromDomain,
		RecipientDomain:   p.RecipientDomain,
		RecipientHash:     p.RecipientHash,
		Provider:          p.Provider,
		MXHost:            p.MXHost,
		SPFResult:         enumOrNone(p.SPFResult),
		DKIMResult:        enumOrNone(p.DKIMResult),
		DMARCResult:       enumOrNone(p.DMARCResult),
		SMTPCode:          uint16(p.SMTPCode),
		EnhancedStatus:    p.EnhancedStatus,
		ResponseText:      p.ResponseText,
		SASLUsername:      p.SASLUsername,
		SizeBytes:         uint32(p.SizeBytes),
		QueueTimeMS:       uint32(p.QueueTimeMS),
		DeliveryLatencyMS: uint32(p.DeliveryLatencyMS),
		Attrs:             p.Attrs,
	}
	if p.TLS != nil {
		r.TLSUsed = boolToU8(p.TLS.Used)
		r.TLSVersion = p.TLS.Version
		r.TLSCipher = p.TLS.Cipher
		r.TLSVerified = boolToU8(p.TLS.Verified)
	}
	if r.EventType == "" {
		return chstore.SMTPEventRow{}, false
	}
	return r, true
}

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

func boolToU8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// enumOrNone maps an empty string to "none" for ClickHouse Enum8 columns that default to
// 'none' (spf/dkim/dmarc results, bounce_class). Maillog-sourced telemetry has no
// SPF/DKIM/DMARC verdict, so those fields arrive empty — and "" is not a valid enum
// element, which would otherwise reject the whole insert batch.
func enumOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
