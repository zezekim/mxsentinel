package fbl

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// incidentOpener is the slice of *postgres.Store the rollup needs. Defining it as an
// interface keeps this package testable and documents exactly which existing exported
// methods we call (we add none).
type incidentOpener interface {
	ResolveTenantByDomain(ctx context.Context, name string) (string, bool, error)
	InsertIncident(ctx context.Context, in pgstore.IncidentInput) (string, bool, error)
}

// Rollup turns complaint counts and Postmaster grades into critical incidents, keyed on
// the SENDING domain (per-client attribution on a shared relay — never the SASL login).
type Rollup struct {
	store     incidentOpener
	log       *slog.Logger
	threshold int // 24h complaint count that trips an incident
}

// NewRollup builds a Rollup. Pass the *postgres.Store (it satisfies incidentOpener).
func NewRollup(store incidentOpener, log *slog.Logger, complaintThreshold int) *Rollup {
	if complaintThreshold <= 0 {
		complaintThreshold = 10
	}
	return &Rollup{store: store, log: log, threshold: complaintThreshold}
}

// Threshold exposes the configured 24h complaint threshold.
func (r *Rollup) Threshold() int { return r.threshold }

// OnComplaintThreshold opens a critical incident when a sending domain's 24h complaint
// count is at/over the threshold. sourceEventID is a stable key so InsertIncident dedupes
// repeated trips for the same domain on the same day (it is ON CONFLICT (tenant,event_id)).
func (r *Rollup) OnComplaintThreshold(ctx context.Context, domain string, count24h int, day string) {
	if domain == "" || count24h < r.threshold {
		return
	}
	tenant, ok := r.resolveTenant(ctx, domain)
	if !ok {
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"sending_domain": domain,
		"complaints_24h": count24h,
		"threshold":      r.threshold,
		"signal":         "feedback_loop_complaints",
		"reason":         "recipients filed spam complaints (ARF/FBL) above the 24h threshold for this sending domain",
		"remediation":    "investigate or suspend the sending account at the hosting server; mailbox providers are seeing this domain as spam",
	})
	r.open(ctx, tenant, "fbl-complaints:"+domain+":"+day, domain,
		fmt.Sprintf("High spam-complaint volume from sending domain %s", domain), detail)
}

// OnPostmasterReputation opens a critical incident when Gmail Postmaster grades a sending
// domain LOW or BAD. day pins the dedupe key to one alert per domain per day.
func (r *Rollup) OnPostmasterReputation(ctx context.Context, rep DomainReport, day string) {
	if !IsBadReputation(rep.Reputation) {
		return
	}
	tenant, ok := r.resolveTenant(ctx, rep.Domain)
	if !ok {
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"sending_domain":        rep.Domain,
		"postmaster_reputation": rep.Reputation,
		"spam_rate":             rep.SpamRate,
		"signal":                "gmail_postmaster_reputation",
		"reason":                "Gmail Postmaster Tools graded this sending domain's reputation as " + rep.Reputation,
		"remediation":           "Gmail is throttling/spam-foldering this domain; review authentication, list hygiene, and complaint sources",
	})
	r.open(ctx, tenant, "fbl-postmaster:"+rep.Domain+":"+day, rep.Domain,
		fmt.Sprintf("Gmail Postmaster reputation %s for sending domain %s", rep.Reputation, rep.Domain), detail)
}

func (r *Rollup) resolveTenant(ctx context.Context, domain string) (string, bool) {
	tenant, ok, err := r.store.ResolveTenantByDomain(ctx, domain)
	if err != nil {
		r.log.Error("resolve tenant for reputation incident", "domain", domain, "err", err)
		return "", false
	}
	if !ok {
		// Complaint about a domain we don't (yet) attribute to a tenant — log so the
		// operator can register it, but don't open an unattributable incident.
		r.log.Warn("complaint/reputation for unregistered sending domain; skipping incident", "domain", domain)
		return "", false
	}
	return tenant, true
}

func (r *Rollup) open(ctx context.Context, tenant, eventID, domain, title string, detail json.RawMessage) {
	_, created, err := r.store.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      tenant,
		SourceEventID: eventID,
		Kind:          "other",
		Severity:      "critical",
		Domain:        domain,
		Subject:       domain,
		Title:         title,
		Detail:        detail,
	})
	if err != nil {
		r.log.Error("open reputation incident", "domain", domain, "err", err)
		return
	}
	if created {
		r.log.Warn("opened reputation incident", "domain", domain, "title", title)
	}
}
