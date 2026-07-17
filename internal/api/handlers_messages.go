package api

import (
	"net/http"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

type messageJSON struct {
	EventID         string `json:"event_id"`
	EventTime       string `json:"event_time"`
	EventType       string `json:"event_type"`
	Outcome         string `json:"outcome"`
	MessageID       string `json:"message_id"`
	QueueID         string `json:"queue_id"`
	FromDomain      string `json:"from_domain"`
	RecipientDomain string `json:"recipient_domain"`
	Provider        string `json:"provider"`
	RelayIP         string `json:"relay_ip"`
	SMTPCode        uint16 `json:"smtp_code"`
	EnhancedStatus  string `json:"enhanced_status"`
	BounceClass     string `json:"bounce_class"`
	ResponseText    string `json:"response_text"`
	SASLUsername    string `json:"sasl_username"`
}

// handleMessages is the message explorer: filterable, tenant-scoped queries over the
// ClickHouse smtp_events table.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.MessageFilter{
		TenantID:  s.tenant(r),
		Domain:    q.Get("domain"),
		Sender:    q.Get("sender"),
		MessageID: q.Get("message_id"),
		Provider:  q.Get("provider"),
		Outcome:   q.Get("outcome"),
		SASLUser:  q.Get("user"),
		Limit:     parseIntParam(r, "limit", 100, 1000),
		Offset:    parseIntParam(r, "offset", 0, 1_000_000),
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}

	rows, err := s.ch.QueryMessages(r.Context(), f)
	if err != nil {
		s.log.Error("query messages", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to query messages")
		return
	}

	msgs := make([]messageJSON, 0, len(rows))
	for _, m := range rows {
		msgs = append(msgs, messageJSON{
			EventID: m.EventID, EventTime: m.EventTime.UTC().Format(time.RFC3339),
			EventType: m.EventType, Outcome: m.Outcome, MessageID: m.MessageID, QueueID: m.QueueID,
			FromDomain: m.FromDomain, RecipientDomain: m.RecipientDomain,
			Provider: m.Provider, RelayIP: m.RelayIP, SMTPCode: m.SMTPCode,
			EnhancedStatus: m.EnhancedStatus, BounceClass: m.BounceClass, ResponseText: m.ResponseText,
			SASLUsername: m.SASLUsername,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": msgs, "count": len(msgs),
		"limit": f.Limit, "offset": f.Offset,
	})
}
