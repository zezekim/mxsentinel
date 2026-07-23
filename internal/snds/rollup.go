package snds

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// incidentOpener is the slice of *postgres.Store the rollup needs. Defining it as an interface
// keeps this package testable and documents that we add no new store methods for incidents —
// we reuse the existing InsertIncident (idempotent on (tenant_id, source_event_id)).
type incidentOpener interface {
	InsertIncident(ctx context.Context, in pgstore.IncidentInput) (string, bool, error)
}

// Rollup turns SNDS filter results and JMRP complaint volume into critical incidents. It
// mirrors internal/fbl.Rollup. Attribution differs by signal: SNDS is keyed by SENDING IP
// (the relay egress IP, resolved to its owning tenant by the daemon), JMRP by SENDING DOMAIN
// (per-client on a shared relay), matching fbl.
type Rollup struct {
	store     incidentOpener
	log       *slog.Logger
	threshold int
}

// NewRollup builds a Rollup. Pass the *postgres.Store (it satisfies incidentOpener).
func NewRollup(store incidentOpener, log *slog.Logger, complaintThreshold int) *Rollup {
	if complaintThreshold <= 0 {
		complaintThreshold = DefaultComplaintThreshold
	}
	return &Rollup{store: store, log: log, threshold: complaintThreshold}
}

// Threshold exposes the configured 24h JMRP complaint threshold.
func (r *Rollup) Threshold() int { return r.threshold }

// OnFilterResult opens a critical incident when SNDS grades a sending IP RED (actively
// junked/blocked at Outlook/Hotmail) or reports spam-trap hits. day pins the dedupe key to one
// alert per IP per day. tenantID is resolved by the daemon from the relay/pool IP inventory.
func (r *Rollup) OnFilterResult(ctx context.Context, tenantID, ip, filterResult string, trapHits int, day string) {
	if tenantID == "" || ip == "" {
		return
	}
	red := IsBadFilterResult(filterResult)
	if !red && trapHits <= 0 {
		return
	}
	reason := "Microsoft SNDS reports spam-trap hits from this sending IP"
	title := fmt.Sprintf("Microsoft SNDS spam-trap hits from IP %s", ip)
	if red {
		reason = "Microsoft SNDS graded this sending IP RED (mail is being junked/blocked at Outlook/Hotmail)"
		title = fmt.Sprintf("Microsoft SNDS filter result RED for IP %s", ip)
	}
	detail, _ := json.Marshal(map[string]any{
		"sending_ip":    ip,
		"filter_result": NormalizeFilterResult(filterResult),
		"trap_hits":     trapHits,
		"signal":        "microsoft_snds_filter_result",
		"reason":        reason,
		"remediation":   "review authentication (SPF/DKIM/DMARC), list hygiene, and complaint sources; consider pausing this IP until SNDS returns GREEN",
	})
	r.open(ctx, tenantID, "snds-filter:"+ip+":"+day, "", ip, title, detail)
}

// OnJMRPComplaintThreshold opens a critical incident when a sending domain's 24h JMRP complaint
// count is at/over the threshold. Mirrors fbl.Rollup.OnComplaintThreshold.
func (r *Rollup) OnJMRPComplaintThreshold(ctx context.Context, tenantID, domain, ip string, count24h int, day string) {
	if tenantID == "" || domain == "" || count24h < r.threshold {
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"sending_domain": domain,
		"sending_ip":     ip,
		"complaints_24h": count24h,
		"threshold":      r.threshold,
		"signal":         "microsoft_jmrp_complaints",
		"reason":         "Outlook/Hotmail recipients filed junk complaints (JMRP/ARF) above the 24h threshold for this sending domain",
		"remediation":    "investigate or suspend the sending account; Microsoft is seeing this domain as spam",
	})
	r.open(ctx, tenantID, "jmrp-complaints:"+domain+":"+day, domain, domain,
		fmt.Sprintf("High JMRP complaint volume from sending domain %s", domain), detail)
}

func (r *Rollup) open(ctx context.Context, tenant, eventID, domain, subject, title string, detail json.RawMessage) {
	_, created, err := r.store.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      tenant,
		SourceEventID: eventID,
		Kind:          "other",
		Severity:      "critical",
		Domain:        domain,
		Subject:       subject,
		Title:         title,
		Detail:        detail,
	})
	if err != nil {
		r.log.Error("open microsoft incident", "subject", subject, "err", err)
		return
	}
	if created {
		r.log.Warn("opened microsoft incident", "tenant_id", tenant, "subject", subject, "title", title)
	}
}
