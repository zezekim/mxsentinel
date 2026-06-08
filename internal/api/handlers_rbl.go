package api

import (
	"net/http"

	"github.com/zezekim/mxsentinel/internal/rbl"
)

// handleRBLStatus reports the RBL/DNSBL self-monitoring state of the relay's egress IPs.
//
// GET /v1/rbl/status ->
//
//	{
//	  "checked_at": "<RFC3339 | null>",
//	  "summary": {"total_ips": N, "healthy": N, "listed": N},
//	  "ips": [
//	    {"ip": "...", "healthy": bool, "checked": bool,
//	     "listings": [{"zone": "...", "listed": bool, "reason": "...", "listed_since": "<RFC3339>"}]}
//	  ]
//	}
//
// The egress-IP universe comes from the same env vars cmd/rbld uses (RELAY_EGRESS_IPS, or
// RELAY_NODE_IP as a fallback), so an IP that has never been checked still appears (marked
// unchecked/unhealthy). This is relay-wide infrastructure state, not tenant-scoped, but it's
// served behind ScopeRead like every other read.
func (s *Server) handleRBLStatus(w http.ResponseWriter, r *http.Request) {
	cfg := rbl.LoadConfig() // reads RELAY_EGRESS_IPS / RELAY_NODE_IP / RBL_* from the API env

	store := rbl.NewStore(s.pg)
	status, err := rbl.BuildStatus(r.Context(), store, cfg.EgressIPs)
	if err != nil {
		s.log.Error("rbl status", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read RBL status")
		return
	}

	// The Status type's json tags produce the documented shape directly (time.Time fields
	// marshal to RFC3339).
	writeJSON(w, http.StatusOK, status)
}
