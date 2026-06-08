// Command rbld is the RBL/DNSBL self-monitor for the relay's own egress IPs. On a fixed
// interval it checks every configured egress IP against every configured DNSBL zone (a
// reversed-octet A lookup — IP 1.2.3.4 on zen.spamhaus.org resolves 4.3.2.1.zen.spamhaus.org;
// an A answer means LISTED, then a TXT lookup yields the reason) and upserts the result into
// ip_blocklist_status.
//
// On a clean->listed transition it opens a critical incident so the operator can start
// delisting before the shared pool's deliverability collapses. It also computes the
// healthy-IP set (IPs listed on zero zones) and writes it to a file (RBL_HEALTHY_IPS_FILE)
// that a HOST-SIDE hook reads to rebuild the Postfix randmap source and run `postfix reload`
// — rbld never touches host Postfix from inside the container (see docs/deploy-relay.md §4.6
// and the install_notes in the integration manifest).
//
// Configuration (env):
//
//	RELAY_EGRESS_IPS      comma-separated egress IPs to monitor (falls back to RELAY_NODE_IP)
//	RBL_ZONES             comma-separated DNSBL zones (default: zen.spamhaus.org,
//	                      b.barracudacentral.org, bl.spamcop.net, dnsbl.sorbs.net, cbl.abuseat.org)
//	RBL_INTERVAL          tick interval (default 15m)
//	RBL_HEALTHY_IPS_FILE  output path for the healthy-IP list (default /var/lib/mxsentinel/healthy-ips)
//	RBL_LOOKUP_TIMEOUT    per-DNS-lookup timeout (default 5s)
//
// Shared-relay caveat: Spamhaus and several other zones rate-limit (or refuse) queries from
// cloud/shared resolvers; a free public DNS resolver may return useless or throttled answers,
// so production deployments should point the box's resolver at a paid DQS key per the zone's
// terms. rbld surfaces lookup errors in its logs and never mistakes a query error for a clean
// result (a transient error neither sets nor clears a listing).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/rbl"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// rblNamespace is a fixed UUIDv5 namespace so a given (ip, zone, listed-episode) always maps
// to the same source_event_id — InsertIncident's UNIQUE(tenant_id, source_event_id) then
// dedupes repeated listings within one episode (we key on listed_since, set on the edge).
var rblNamespace = uuid.MustParse("6f4d1b2e-7a3c-5e8f-9b0d-1c2e3f4a5b6c")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rbld:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("rbld", cfg.LogLevel)

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

	var pg *pgstore.Store
	if err := obs.Retry(ctx, log, "postgres", 30, 2*time.Second, func() error {
		p, e := pgstore.New(ctx, cfg.Postgres)
		if e != nil {
			return e
		}
		pg = p
		return nil
	}); err != nil {
		return err
	}
	defer pg.Close()

	rcfg := rbl.LoadConfig()
	w := &worker{
		log:     log,
		pg:      pg,
		store:   rbl.NewStore(pg),
		checker: rbl.NewChecker(nil, rcfg.LookupTimeout),
		cfg:     rcfg,
	}

	if len(rcfg.EgressIPs) == 0 {
		// Degenerate but valid: no IPs to monitor. We still run (health endpoint up, status
		// API serves an empty set) so misconfiguration is visible rather than crashing.
		log.Warn("no egress IPs configured; set RELAY_EGRESS_IPS (or RELAY_NODE_IP). rbld will idle.")
	} else {
		log.Info("rbld started",
			"egress_ips", rcfg.EgressIPs, "zones", rcfg.Zones,
			"interval", rcfg.Interval.String(), "healthy_ips_file", rcfg.HealthyIPsFile,
			"used_node_ip_fallback", rcfg.UsedNodeIPFallback)
	}

	srv.SetReady(true)

	// Run one sweep immediately so status is populated without waiting a full interval.
	w.sweep(ctx)

	ticker := time.NewTicker(rcfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("rbld shutting down")
			return nil
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

type worker struct {
	log     *slog.Logger
	pg      *pgstore.Store
	store   *rbl.Store
	checker *rbl.Checker
	cfg     rbl.Config
}

// tenantForIPs builds an egress-IP -> tenant-id map from the registered relay nodes and IP
// pools (the same source repd uses). A relay egress IP is infrastructure, but incidents
// require a tenant (incidents.tenant_id is NOT NULL with an FK), so we attribute a listing to
// the tenant that registered the IP. IPs with no registration are returned absent and their
// incidents are skipped (logged) — honest about the shared-relay case where the egress pool
// may not be tenant-attributed.
func (w *worker) tenantForIPs(ctx context.Context) map[string]string {
	out := map[string]string{}
	targets, err := w.pg.ListReputationTargets(ctx)
	if err != nil {
		w.log.Warn("list reputation targets for incident attribution", "err", err)
		return out
	}
	for _, t := range targets {
		if _, exists := out[t.IP]; !exists && t.TenantID != "" {
			out[t.IP] = t.TenantID
		}
	}
	return out
}

// sweep checks every egress IP x zone, upserts results, opens incidents on clean->listed
// transitions, then recomputes the healthy-IP set and writes it to the export file.
func (w *worker) sweep(ctx context.Context) {
	if len(w.cfg.EgressIPs) == 0 {
		return
	}
	checkedAt := time.Now().UTC()
	checks, listedCount, errCount := 0, 0, 0
	tenantByIP := w.tenantForIPs(ctx)

	for _, ip := range w.cfg.EgressIPs {
		for _, zone := range w.cfg.Zones {
			if ctx.Err() != nil {
				return
			}
			checks++
			listing, err := w.checker.Check(ctx, ip, zone)
			if err != nil {
				// Indeterminate lookup: do NOT write a (possibly wrong) clean/listed result.
				errCount++
				w.log.Warn("dnsbl lookup error (skipping write)", "ip", ip, "zone", zone, "err", err)
				continue
			}
			transition, nowListed, uerr := w.store.Upsert(ctx, listing, checkedAt)
			if uerr != nil {
				w.log.Error("upsert blocklist status", "ip", ip, "zone", zone, "err", uerr)
				continue
			}
			if nowListed {
				listedCount++
			}
			if transition && nowListed {
				w.openIncident(ctx, listing, checkedAt, tenantByIP[ip])
			} else if transition && !nowListed {
				w.log.Info("egress IP delisted", "ip", ip, "zone", zone)
			}
		}
	}

	// Best-effort prune of rows for IPs/zones removed from config, then export healthy set.
	if err := w.store.PruneStale(ctx, w.cfg.EgressIPs, w.cfg.Zones); err != nil {
		w.log.Warn("prune stale blocklist rows", "err", err)
	}
	healthy, err := w.store.HealthyIPs(ctx, w.cfg.EgressIPs)
	if err != nil {
		w.log.Error("compute healthy ips", "err", err)
	} else if ferr := rbl.WriteHealthyIPs(w.cfg.HealthyIPsFile, healthy); ferr != nil {
		w.log.Error("write healthy ips file", "file", w.cfg.HealthyIPsFile, "err", ferr)
	}

	w.log.Info("rbl sweep complete",
		"checks", checks, "listings", listedCount, "lookup_errors", errCount,
		"healthy_ips", len(healthy))
}

// openIncident opens a critical incident for a newly-listed egress IP, attributed to the
// tenant that registered the IP (relay node / IP pool). incidents.tenant_id is NOT NULL with
// an FK, so an unattributed egress IP (not registered to any tenant — common on a shared
// relay) cannot get an incident: we log and skip, but the listing is still tracked in
// ip_blocklist_status and surfaced by the API/dashboard. source_event_id is a deterministic
// UUIDv5 of (ip, zone, listed-since) so InsertIncident's UNIQUE(tenant, source) dedupes
// re-checks within one listed episode.
func (w *worker) openIncident(ctx context.Context, l rbl.Listing, checkedAt time.Time, tenantID string) {
	w.log.Warn("egress IP newly listed on DNSBL", "ip", l.IP, "zone", l.Zone, "reason", l.Reason)

	if tenantID == "" {
		w.log.Warn("listed egress IP is not registered to a tenant; incident skipped (status still tracked). "+
			"Register the IP via `mxctl relay-node add` / an IP pool to get incidents.",
			"ip", l.IP, "zone", l.Zone)
		return
	}

	detail, _ := json.Marshal(map[string]any{
		"egress_ip":  l.IP,
		"zone":       l.Zone,
		"reason":     l.Reason,
		"codes":      l.Codes,
		"checked_at": checkedAt.Format(time.RFC3339),
		"remediation": "Investigate outbound spam/abuse from this IP, then request delisting at " +
			"the zone's removal page (see the reason/TXT for the URL). The host-side healthy-IP " +
			"hook will pull this IP from the Postfix rotation on the next run; restore it after delisting.",
	})

	source := uuid.NewSHA1(rblNamespace, []byte(l.IP+"|"+l.Zone+"|"+checkedAt.Format(time.RFC3339))).String()
	if _, _, err := w.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      tenantID,
		SourceEventID: source,
		Kind:          "other",
		Severity:      "critical",
		Domain:        l.IP,
		Subject:       l.IP,
		Title:         "Egress IP " + l.IP + " listed on " + l.Zone,
		Detail:        detail,
	}); err != nil {
		w.log.Error("open rbl incident", "ip", l.IP, "zone", l.Zone, "err", err)
	}
}
