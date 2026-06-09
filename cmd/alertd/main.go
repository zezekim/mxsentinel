// Command alertd evaluates alert rules for every tenant and fires alert events when
// thresholds are breached. It runs a poll loop every 60 seconds and dispatches
// notifications to configured channels (webhook, Slack, email stub). Duplicate
// firing is suppressed by checking for an existing open alert_event before inserting.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

const evalInterval = 60 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "alertd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("alertd", cfg.LogLevel)

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

	w := &worker{
		log:    log,
		pg:     pg,
		ch:     ch,
		client: &http.Client{Timeout: 10 * time.Second},
	}

	srv.SetReady(true)
	log.Info("alertd started", "eval_interval", evalInterval.String())

	// Run one evaluation immediately, then on every tick.
	w.evaluate(ctx)

	tick := time.NewTicker(evalInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("alertd shutting down")
			return nil
		case <-tick.C:
			w.evaluate(ctx)
		}
	}
}

// worker holds shared state for the evaluation loop.
type worker struct {
	log    *slog.Logger
	pg     *pgstore.Store
	ch     *chstore.Store
	client *http.Client
}

// evaluate iterates over all tenants (via monitored domains) and evaluates their
// alert rules once per cycle.
func (w *worker) evaluate(ctx context.Context) {
	domains, err := w.pg.ListMonitoredDomains(ctx)
	if err != nil {
		w.log.Error("list monitored domains", "err", err)
		return
	}

	// Deduplicate: one evaluation pass per tenant.
	seen := map[string]bool{}
	for _, d := range domains {
		if ctx.Err() != nil {
			return
		}
		if seen[d.TenantID] {
			continue
		}
		seen[d.TenantID] = true
		w.evaluateTenant(ctx, d.TenantID)
	}
}

// evaluateTenant evaluates all enabled alert rules for a single tenant.
func (w *worker) evaluateTenant(ctx context.Context, tenantID string) {
	rules, err := w.pg.ListAlertRules(ctx, tenantID)
	if err != nil {
		w.log.Error("list alert rules", "tenant", tenantID, "err", err)
		return
	}

	channels, err := w.pg.ListNotificationChannels(ctx, tenantID)
	if err != nil {
		w.log.Error("list notification channels", "tenant", tenantID, "err", err)
		return
	}
	channelsByID := make(map[string]pgstore.NotificationChannel, len(channels))
	for _, c := range channels {
		channelsByID[c.ID] = c
	}

	// Fetch existing open events for this tenant once to avoid per-rule queries.
	openEvents, err := w.pg.ListAlertEvents(ctx, tenantID, 500)
	if err != nil {
		w.log.Error("list alert events", "tenant", tenantID, "err", err)
		return
	}
	openByRule := map[string]bool{}
	for _, e := range openEvents {
		if e.State == "firing" || e.State == "open" {
			openByRule[e.RuleID] = true
		}
	}

	now := time.Now()
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if openByRule[rule.ID] {
			w.log.Debug("alert rule already firing, skipping", "rule", rule.ID, "signal", rule.Signal, "tenant", tenantID)
			continue
		}
		triggered, triggeredValue, threshold, err := w.checkRule(ctx, tenantID, rule, now)
		if err != nil {
			w.log.Error("check rule", "rule", rule.ID, "signal", rule.Signal, "tenant", tenantID, "err", err)
			continue
		}
		if !triggered {
			continue
		}
		w.fire(ctx, tenantID, rule, triggeredValue, threshold, channelsByID)
	}
}

// checkRule evaluates a single alert rule and returns (triggered, triggeredValue, threshold, error).
func (w *worker) checkRule(ctx context.Context, tenantID string, rule pgstore.AlertRule, now time.Time) (bool, float64, float64, error) {
	var cond struct {
		Threshold float64 `json:"threshold"`
	}
	if len(rule.Condition) > 0 {
		if err := json.Unmarshal(rule.Condition, &cond); err != nil {
			return false, 0, 0, fmt.Errorf("parse condition: %w", err)
		}
	}

	switch rule.Signal {
	case "rejection_spike":
		return w.checkRejectionSpike(ctx, tenantID, cond.Threshold, now)

	case "blacklist_hit":
		return w.checkBlacklistHit(ctx, tenantID, cond.Threshold)

	case "bounce_rate":
		return w.checkBounceRate(ctx, tenantID, cond.Threshold, now)

	default:
		w.log.Warn("unknown signal, skipping", "signal", rule.Signal, "rule", rule.ID)
		return false, 0, 0, nil
	}
}

// checkRejectionSpike queries ClickHouse for the last 1h and checks if the overall
// rejection rate across all providers exceeds threshold.
func (w *worker) checkRejectionSpike(ctx context.Context, tenantID string, threshold float64, now time.Time) (bool, float64, float64, error) {
	since := now.Add(-1 * time.Hour)
	rows, err := w.ch.ProviderHeatmap(ctx, tenantID, since, time.Time{})
	if err != nil {
		return false, 0, 0, fmt.Errorf("provider heatmap: %w", err)
	}

	var totalDelivered, totalRejected, total uint64
	for _, r := range rows {
		totalDelivered += r.Delivered
		totalRejected += r.Rejected
		total += r.Total
	}

	if total == 0 {
		return false, 0, threshold, nil
	}

	rejectionRate := float64(totalRejected) / float64(total)
	if rejectionRate > threshold {
		return true, rejectionRate, threshold, nil
	}
	return false, rejectionRate, threshold, nil
}

// checkBlacklistHit checks whether there are any open blacklist incidents for this tenant.
func (w *worker) checkBlacklistHit(ctx context.Context, tenantID string, threshold float64) (bool, float64, float64, error) {
	incidents, err := w.pg.ListIncidents(ctx, tenantID, "open", "", 100, 0)
	if err != nil {
		return false, 0, 0, fmt.Errorf("list incidents: %w", err)
	}

	var count int
	for _, inc := range incidents {
		if inc.Kind == "blacklist_hit" || inc.Kind == "reputation_blacklist" {
			count++
		}
	}

	if count > 0 {
		return true, float64(count), threshold, nil
	}
	return false, 0, threshold, nil
}

// checkBounceRate checks if any SMTP user's bounce rate exceeds the threshold in the last 1h.
func (w *worker) checkBounceRate(ctx context.Context, tenantID string, threshold float64, now time.Time) (bool, float64, float64, error) {
	since := now.Add(-1 * time.Hour)
	stats, err := w.ch.PerUserStats(ctx, tenantID, since, time.Time{})
	if err != nil {
		return false, 0, 0, fmt.Errorf("per user stats: %w", err)
	}

	var maxBounceRate float64
	for _, s := range stats {
		if s.BounceRate > maxBounceRate {
			maxBounceRate = s.BounceRate
		}
	}

	if maxBounceRate > threshold {
		return true, maxBounceRate, threshold, nil
	}
	return false, maxBounceRate, threshold, nil
}

// alertPayload is the JSON payload stored in the alert_event and sent to notification channels.
type alertPayload struct {
	RuleID         string  `json:"rule_id"`
	Signal         string  `json:"signal"`
	TenantID       string  `json:"tenant_id"`
	TriggeredValue float64 `json:"triggered_value"`
	Threshold      float64 `json:"threshold"`
	FiredAt        string  `json:"fired_at"`
}

// fire inserts an alert event and dispatches notifications.
func (w *worker) fire(ctx context.Context, tenantID string, rule pgstore.AlertRule, triggeredValue, threshold float64, channelsByID map[string]pgstore.NotificationChannel) {
	payload := alertPayload{
		RuleID:         rule.ID,
		Signal:         rule.Signal,
		TenantID:       tenantID,
		TriggeredValue: triggeredValue,
		Threshold:      threshold,
		FiredAt:        time.Now().UTC().Format(time.RFC3339),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		w.log.Error("marshal alert payload", "rule", rule.ID, "err", err)
		return
	}

	id, err := w.pg.InsertAlertEvent(ctx, tenantID, rule.ID, payloadBytes)
	if err != nil {
		w.log.Error("insert alert event", "rule", rule.ID, "tenant", tenantID, "err", err)
		return
	}

	w.log.Info("alert fired",
		"event_id", id,
		"rule", rule.ID,
		"rule_name", rule.Name,
		"signal", rule.Signal,
		"tenant", tenantID,
		"triggered_value", triggeredValue,
		"threshold", threshold,
	)

	for _, channelID := range rule.ChannelIDs {
		ch, ok := channelsByID[channelID]
		if !ok || !ch.Enabled {
			continue
		}
		w.notify(ctx, ch, rule, payload)
	}
}

// notify dispatches a notification to a single channel.
func (w *worker) notify(ctx context.Context, ch pgstore.NotificationChannel, rule pgstore.AlertRule, payload alertPayload) {
	var cfg map[string]string
	if len(ch.Config) > 0 {
		if err := json.Unmarshal(ch.Config, &cfg); err != nil {
			w.log.Error("parse channel config", "channel", ch.ID, "kind", ch.Kind, "err", err)
			return
		}
	}

	switch ch.Kind {
	case "webhook":
		url, ok := cfg["url"]
		if !ok || url == "" {
			w.log.Warn("webhook channel missing url", "channel", ch.ID)
			return
		}
		w.sendWebhook(ctx, ch.ID, url, payload)

	case "slack":
		webhookURL, ok := cfg["webhook_url"]
		if !ok || webhookURL == "" {
			w.log.Warn("slack channel missing webhook_url", "channel", ch.ID)
			return
		}
		w.sendSlack(ctx, ch.ID, webhookURL, rule, payload)

	case "email":
		to, _ := cfg["to"]
		w.log.Info("would send email",
			"channel", ch.ID,
			"to", to,
			"rule", rule.Name,
			"signal", payload.Signal,
			"triggered_value", payload.TriggeredValue,
			"threshold", payload.Threshold,
		)

	default:
		w.log.Warn("unknown channel kind, skipping", "channel", ch.ID, "kind", ch.Kind)
	}
}

// sendWebhook POSTs the full alert payload to a generic webhook URL.
func (w *worker) sendWebhook(ctx context.Context, channelID, url string, payload alertPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.log.Error("marshal webhook body", "channel", channelID, "err", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		w.log.Error("build webhook request", "channel", channelID, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		w.log.Error("webhook delivery failed", "channel", channelID, "url", url, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		w.log.Warn("webhook returned non-2xx", "channel", channelID, "url", url, "status", resp.StatusCode)
		return
	}
	w.log.Info("webhook delivered", "channel", channelID, "status", resp.StatusCode)
}

// sendSlack posts a formatted message to a Slack Incoming Webhook URL.
func (w *worker) sendSlack(ctx context.Context, channelID, webhookURL string, rule pgstore.AlertRule, payload alertPayload) {
	text := fmt.Sprintf("[MX Sentinel] Alert *%s* fired (signal: %s) — value %.4f exceeded threshold %.4f (tenant: %s)",
		rule.Name, payload.Signal, payload.TriggeredValue, payload.Threshold, payload.TenantID)

	slackBody, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		w.log.Error("marshal slack body", "channel", channelID, "err", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(slackBody))
	if err != nil {
		w.log.Error("build slack request", "channel", channelID, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		w.log.Error("slack delivery failed", "channel", channelID, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		w.log.Warn("slack returned non-2xx", "channel", channelID, "status", resp.StatusCode)
		return
	}
	w.log.Info("slack notification delivered", "channel", channelID)
}
