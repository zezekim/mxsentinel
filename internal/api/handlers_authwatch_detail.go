package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/authwatch"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

// handleAuthUserStats handles GET /v1/auth-security/{user}/stats.
// Returns detailed per-user send statistics for a specific SASL user.
//
// Query params:
//
//	?window=24h|7d|30d  (default 7d)
//
// Response shape:
//
//	{
//	  "sasl_username": str,
//	  "stats": { sent, delivered, bounced, rejected, unique_recipients, bounce_rate },
//	  "signals": [ { signal, detail, detected_at }, ... ],
//	  "locked": bool,
//	  "locked_at": str|null
//	}
func (s *Server) handleAuthUserStats(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("user")
	if username == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing credential username")
		return
	}

	// Parse ?window= param.
	window := r.URL.Query().Get("window")
	var since time.Time
	now := time.Now().UTC()
	switch window {
	case "24h":
		since = now.Add(-24 * time.Hour)
	case "30d":
		since = now.Add(-30 * 24 * time.Hour)
	default:
		// "7d" or anything else falls through to the default.
		since = now.Add(-7 * 24 * time.Hour)
	}

	ctx := r.Context()
	tenantID := s.tenant(r)

	// Fetch per-user stats from ClickHouse.
	chRows, err := s.ch.PerUserStats(ctx, tenantID, since, time.Time{})
	if err != nil {
		s.log.Error("per-user stats from clickhouse", "user", username, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to fetch send statistics")
		return
	}

	// Find the row matching this username (zero-value if not found).
	var stat chstore.PerUserStat
	for _, row := range chRows {
		if row.SASLUsername == username {
			stat = row
			break
		}
	}

	// Fetch credential signals and lock state from authwatch store.
	creds, err := authwatch.NewStore(s.pg.Pool).ListCredentials(ctx, 200)
	if err != nil {
		s.log.Error("list credentials for auth-user-stats", "user", username, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to fetch credential data")
		return
	}

	type signalJSON struct {
		Signal     string          `json:"signal"`
		Detail     json.RawMessage `json:"detail"`
		DetectedAt string          `json:"detected_at"`
	}

	var signals []signalJSON
	locked := false
	var lockedAt *string

	for _, c := range creds {
		if c.SASLUsername != username {
			continue
		}
		locked = c.Locked
		if c.LockedAt != nil {
			ts := c.LockedAt.UTC().Format(time.RFC3339)
			lockedAt = &ts
		}
		signals = make([]signalJSON, 0, len(c.RecentSignals))
		for _, sig := range c.RecentSignals {
			signals = append(signals, signalJSON{
				Signal:     sig.Signal,
				Detail:     sig.Detail,
				DetectedAt: sig.DetectedAt.UTC().Format(time.RFC3339),
			})
		}
		break
	}

	if signals == nil {
		signals = []signalJSON{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sasl_username": username,
		"stats": map[string]any{
			"sent":              stat.Sent,
			"delivered":         stat.Delivered,
			"bounced":           stat.Bounced,
			"rejected":          stat.Rejected,
			"unique_recipients": stat.UniqueRecipients,
			"bounce_rate":       stat.BounceRate,
		},
		"signals":   signals,
		"locked":    locked,
		"locked_at": lockedAt,
	})
}
