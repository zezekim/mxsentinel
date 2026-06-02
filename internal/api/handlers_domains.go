package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// ---- response shapes -------------------------------------------------------

type categoryStatuses struct {
	SPF   string `json:"spf"`
	DKIM  string `json:"dkim"`
	DMARC string `json:"dmarc"`
	MX    string `json:"mx"`
}

type domainListItem struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Status        string           `json:"status"`
	Categories    categoryStatuses `json:"categories"`
	Overall       string           `json:"overall"`
	LastCheckedAt *string          `json:"last_checked_at"`
	FindingCount  int              `json:"finding_count"`
}

type domainRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type snapshotJSON struct {
	ID         string `json:"id"`
	CapturedAt string `json:"captured_at"`
	Checksum   string `json:"checksum"`
	Healthy    bool   `json:"healthy"`
}

type findingJSON struct {
	Category string          `json:"category"`
	Severity string          `json:"severity"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Detail   json.RawMessage `json:"detail,omitempty"`
}

type healthResponse struct {
	Domain     domainRef        `json:"domain"`
	Snapshot   *snapshotJSON    `json:"snapshot"`
	Categories categoryStatuses `json:"categories"`
	Overall    string           `json:"overall"`
	Findings   []findingJSON    `json:"findings"`
}

type recheckResponse struct {
	Changed bool `json:"changed"`
	healthResponse
}

// ---- handlers --------------------------------------------------------------

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	list, err := s.pg.ListDomainHealth(r.Context(), tenant)
	if err != nil {
		s.log.Error("list domain health", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list domains")
		return
	}

	items := make([]domainListItem, 0, len(list))
	for _, dh := range list {
		hasSnap := dh.SnapshotID != ""
		var cf []catFinding
		if dh.FindingCount > 0 {
			if full, found, ferr := s.pg.GetDomainHealth(r.Context(), tenant, dh.DomainID); ferr == nil && found {
				cf = toCatFindings(full.Findings)
			}
		}
		cats, overall := categorize(hasSnap, cf)
		items = append(items, domainListItem{
			ID: dh.DomainID, Name: dh.Name, Status: dh.Status,
			Categories: cats, Overall: overall,
			LastCheckedAt: rfc3339Ptr(dh.LastCheckedAt), FindingCount: dh.FindingCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": items})
}

func (s *Server) handleDomainHealth(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	dh, found, err := s.pg.GetDomainHealth(r.Context(), tenant, r.PathValue("id"))
	if err != nil {
		s.log.Error("get domain health", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read domain")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}
	cats, overall := categorize(dh.SnapshotID != "", toCatFindings(dh.Findings))
	writeJSON(w, http.StatusOK, healthResponse{
		Domain:     domainRef{ID: dh.DomainID, Name: dh.Name, Status: dh.Status},
		Snapshot:   snapshotFromHealth(dh),
		Categories: cats,
		Overall:    overall,
		Findings:   findingsFromRows(dh.Findings),
	})
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	limit := parseIntParam(r, "limit", 50, 500)
	snaps, found, err := s.pg.ListSnapshots(r.Context(), tenant, r.PathValue("id"), limit)
	if err != nil {
		s.log.Error("list snapshots", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list snapshots")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}
	type snapItem struct {
		ID           string `json:"id"`
		CapturedAt   string `json:"captured_at"`
		Checksum     string `json:"checksum"`
		Healthy      bool   `json:"healthy"`
		FindingCount int    `json:"finding_count"`
	}
	items := make([]snapItem, 0, len(snaps))
	for _, sn := range snaps {
		items = append(items, snapItem{
			ID: sn.ID, CapturedAt: sn.CapturedAt.UTC().Format(time.RFC3339),
			Checksum: sn.Checksum, Healthy: sn.Healthy, FindingCount: sn.FindingCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": items})
}

func (s *Server) handleRecheck(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	dh, found, err := s.pg.GetDomainHealth(r.Context(), tenant, id)
	if err != nil {
		s.log.Error("get domain for recheck", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read domain")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "domain not found")
		return
	}

	snap, err := dnsx.Inspect(r.Context(), s.resolver, dh.Name, dnsx.Options{})
	if err != nil {
		s.log.Error("inspect", "domain", dh.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "inspect_failed", "DNS inspection failed")
		return
	}

	prior, err := s.pg.LatestSnapshotChecksum(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read prior snapshot")
		return
	}
	changed := snap.Checksum != prior
	snapID := dh.SnapshotID
	if changed || prior == "" {
		if snapID, err = s.pg.InsertSnapshot(r.Context(), id, snap.StateJSON, snap.Checksum, snap.Healthy, snap.Findings); err != nil {
			s.log.Error("insert snapshot", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to persist snapshot")
			return
		}
	}
	_ = s.pg.MarkDomainChecked(r.Context(), id)

	cats, overall := categorize(true, toCatFindingsContracts(snap.Findings))
	writeJSON(w, http.StatusOK, recheckResponse{
		Changed: changed,
		healthResponse: healthResponse{
			Domain: domainRef{ID: dh.DomainID, Name: dh.Name, Status: dh.Status},
			Snapshot: &snapshotJSON{
				ID: snapID, CapturedAt: time.Now().UTC().Format(time.RFC3339),
				Checksum: snap.Checksum, Healthy: snap.Healthy,
			},
			Categories: cats,
			Overall:    overall,
			Findings:   findingsFromContracts(snap.Findings),
		},
	})
}

// ---- category derivation ---------------------------------------------------

type catFinding struct{ Category, Severity string }

func toCatFindings(fs []pgstore.FindingRow) []catFinding {
	out := make([]catFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, catFinding{f.Category, f.Severity})
	}
	return out
}

func toCatFindingsContracts(fs []contracts.DNSFinding) []catFinding {
	out := make([]catFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, catFinding{f.Category, f.Severity})
	}
	return out
}

// categorize maps findings to per-category and overall statuses. Categories shown:
// spf, dkim, dmarc, mx. With no snapshot everything is "unknown".
func categorize(hasSnapshot bool, fs []catFinding) (categoryStatuses, string) {
	if !hasSnapshot {
		return categoryStatuses{SPF: "unknown", DKIM: "unknown", DMARC: "unknown", MX: "unknown"}, "unknown"
	}
	m := map[string]string{"spf": "ok", "dkim": "ok", "dmarc": "ok", "mx": "ok"}
	for _, f := range fs {
		cur, tracked := m[f.Category]
		if !tracked {
			continue
		}
		if st := sevToStatus(f.Severity); statusRank(st) > statusRank(cur) {
			m[f.Category] = st
		}
	}
	cats := categoryStatuses{SPF: m["spf"], DKIM: m["dkim"], DMARC: m["dmarc"], MX: m["mx"]}
	return cats, overallStatus(m)
}

func sevToStatus(sev string) string {
	switch sev {
	case "critical":
		return "critical"
	case "warning":
		return "warning"
	default:
		return "ok"
	}
}

func statusRank(st string) int {
	switch st {
	case "critical":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}

func overallStatus(m map[string]string) string {
	worst := 0
	for _, st := range m {
		if statusRank(st) > worst {
			worst = statusRank(st)
		}
	}
	switch worst {
	case 2:
		return "critical"
	case 1:
		return "warning"
	default:
		return "healthy"
	}
}

// ---- small converters ------------------------------------------------------

func snapshotFromHealth(dh pgstore.DomainHealth) *snapshotJSON {
	if dh.SnapshotID == "" {
		return nil
	}
	captured := ""
	if dh.CapturedAt != nil {
		captured = dh.CapturedAt.UTC().Format(time.RFC3339)
	}
	return &snapshotJSON{ID: dh.SnapshotID, CapturedAt: captured, Checksum: dh.Checksum, Healthy: dh.Healthy}
}

func findingsFromRows(fs []pgstore.FindingRow) []findingJSON {
	out := make([]findingJSON, 0, len(fs))
	for _, f := range fs {
		out = append(out, findingJSON{Category: f.Category, Severity: f.Severity, Code: f.Code, Message: f.Message, Detail: f.Detail})
	}
	return out
}

func findingsFromContracts(fs []contracts.DNSFinding) []findingJSON {
	out := make([]findingJSON, 0, len(fs))
	for _, f := range fs {
		fj := findingJSON{Category: f.Category, Severity: f.Severity, Code: f.Code, Message: f.Message}
		if len(f.Detail) > 0 {
			if b, err := json.Marshal(f.Detail); err == nil {
				fj.Detail = b
			}
		}
		out = append(out, fj)
	}
	return out
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func parseIntParam(r *http.Request, name string, def, max int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
