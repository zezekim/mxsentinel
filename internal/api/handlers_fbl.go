package api

import (
	"net/http"

	"github.com/zezekim/mxsentinel/internal/fbl"
)

// reputationRowJSON is one domain's complaint reputation in the /v1/reputation response.
type reputationRowJSON struct {
	Domain               string   `json:"domain"`
	Complaints24h        int      `json:"complaints_24h"`
	ComplaintsTotal      int      `json:"complaints_total"`
	PostmasterReputation string   `json:"postmaster_reputation"`
	SpamRate             *float64 `json:"spam_rate"`
	FetchedAt            *string  `json:"fetched_at"`
}

// handleReputation returns per-sending-domain complaint reputation: 24h and lifetime
// feedback-loop (ARF) complaint counts plus the latest Gmail Postmaster grade + spam rate
// (when GOOGLE_POSTMASTER_TOKEN was configured for the fbld daemon).
//
// Shared-relay caveat: complaints/reputation are attributed by SENDING DOMAIN, not the
// shared SASL login. This endpoint is NOT tenant-scoped at the store layer because the
// underlying FBL/Postmaster signals carry no tenant — the relay operates one credential
// across all client domains. (If multi-tenant isolation is needed later, join through the
// domains table.)
func (s *Server) handleReputation(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r, "limit", 100, 1000)

	rows, err := fbl.NewStore(s.pg.Pool).ListReputation(r.Context(), limit)
	if err != nil {
		s.log.Error("list reputation", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list reputation")
		return
	}

	out := make([]reputationRowJSON, 0, len(rows))
	for _, rr := range rows {
		var fetched *string
		if rr.FetchedAt != nil {
			ts := rr.FetchedAt.UTC().Format("2006-01-02T15:04:05Z")
			fetched = &ts
		}
		out = append(out, reputationRowJSON{
			Domain:               rr.Domain,
			Complaints24h:        rr.Complaints24h,
			ComplaintsTotal:      rr.ComplaintsTot,
			PostmasterReputation: rr.Reputation,
			SpamRate:             rr.SpamRate,
			FetchedAt:            fetched,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}
