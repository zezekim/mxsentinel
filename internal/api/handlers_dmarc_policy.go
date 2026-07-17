package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type dmarcPolicyAdviceJSON struct {
	Domain            string  `json:"domain"`
	TotalMessages     uint64  `json:"total_messages"`
	PassRate          float64 `json:"pass_rate"`
	PublishedPolicy   string  `json:"published_policy"`
	RecommendedPolicy string  `json:"recommended_policy"`
	Rationale         string  `json:"rationale"`
}

// handleDMARCPolicyAdvice recommends a DMARC enforcement policy per domain based on the
// observed alignment pass rate over dmarc_records, alongside the currently published
// policy (best-effort, from the latest DNS snapshot). It degrades gracefully: if the
// published policy cannot be discovered, published_policy is "".
func (s *Server) handleDMARCPolicyAdvice(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	since := parseTimeParam(r, "since")
	until := parseTimeParam(r, "until")

	domains, err := s.ch.DMARCPolicyObserved(r.Context(), tenant, since, until)
	if err != nil {
		s.log.Error("dmarc policy advice", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to compute policy advice")
		return
	}

	// Best-effort lookup of published policies; failure must not break the response.
	published := s.publishedDMARCPolicies(r.Context(), tenant)

	out := make([]dmarcPolicyAdviceJSON, 0, len(domains))
	for _, d := range domains {
		passRate := 0.0
		if d.TotalMessages > 0 {
			passRate = float64(d.PassMessages) / float64(d.TotalMessages)
		}

		recommended := "none"
		switch {
		case passRate >= 0.99 && d.TotalMessages >= 100:
			recommended = "reject"
		case passRate >= 0.95:
			recommended = "quarantine"
		}

		rationale := fmt.Sprintf("%.0f%% aligned over %d messages", passRate*100, d.TotalMessages)

		out = append(out, dmarcPolicyAdviceJSON{
			Domain:            d.Domain,
			TotalMessages:     d.TotalMessages,
			PassRate:          passRate,
			PublishedPolicy:   published[strings.ToLower(d.Domain)],
			RecommendedPolicy: recommended,
			Rationale:         rationale,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

// publishedDMARCPolicies returns a map of domain name -> published DMARC policy
// (none|quarantine|reject) derived from the latest DNS snapshot per domain. The published
// record is stored as the raw "dmarc" string inside dns_snapshots.state (JSONB), captured
// by the DNS validator. This is best-effort: any error or missing data yields an empty
// map, and individual domains without a discoverable policy are simply absent.
func (s *Server) publishedDMARCPolicies(ctx context.Context, tenantID string) map[string]string {
	out := map[string]string{}
	if s.pg == nil {
		return out
	}

	// Latest snapshot per domain (by captured_at), pulling the raw DMARC record string.
	const q = `SELECT d.name, s.state ->> 'dmarc'
	           FROM domains d
	           JOIN LATERAL (
	               SELECT state FROM dns_snapshots
	               WHERE domain_id = d.id
	               ORDER BY captured_at DESC
	               LIMIT 1
	           ) s ON true
	           WHERE d.tenant_id = $1`

	rows, err := s.pg.Pool.Query(ctx, q, tenantID)
	if err != nil {
		s.log.Warn("dmarc published policy lookup", "err", err)
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var record *string
		if err := rows.Scan(&name, &record); err != nil {
			s.log.Warn("dmarc published policy scan", "err", err)
			return out
		}
		if record == nil {
			continue
		}
		if p := dmarcPolicyTag(*record); p != "" {
			out[strings.ToLower(name)] = p
		}
	}
	if err := rows.Err(); err != nil {
		s.log.Warn("dmarc published policy rows", "err", err)
	}
	return out
}

// dmarcPolicyTag extracts the p= policy value (none|quarantine|reject) from a raw DMARC
// record string such as "v=DMARC1; p=reject; rua=...". Returns "" if absent or invalid.
func dmarcPolicyTag(record string) string {
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(part[:eq]), "p") {
			switch v := strings.ToLower(strings.TrimSpace(part[eq+1:])); v {
			case "none", "quarantine", "reject":
				return v
			default:
				return ""
			}
		}
	}
	return ""
}
