package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

// timelineEventJSON is one row of the per-message delivery timeline (the "Log entries" panel).
type timelineEventJSON struct {
	EventTime       string `json:"event_time"`
	EventType       string `json:"event_type"`
	Provider        string `json:"provider"`
	MXHost          string `json:"mx_host"`
	RecipientDomain string `json:"recipient_domain"`
	SMTPCode        uint16 `json:"smtp_code"`
	EnhancedStatus  string `json:"enhanced_status"`
	BounceClass     string `json:"bounce_class"`
	ResponseText    string `json:"response_text"`
}

type spamSymbolJSON struct {
	Name  string  `json:"name"`
	Score float32 `json:"score"`
}

type headerJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// handleMessageDetail (GET /v1/messages/{queueID}) is the per-message drill-down backing the
// operator's per-email view: envelope + auth results, the delivery timeline, and — when rspamd
// capture is enabled — the spam verdict/symbols. The subject and full raw headers are message
// CONTENT and are only returned to callers holding the admin scope; a ScopeRead caller sees
// content_available=true but not the content itself.
func (s *Server) handleMessageDetail(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	queueID := strings.TrimSpace(r.PathValue("queueID"))
	if queueID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "queueID is required")
		return
	}

	env, ok, err := s.ch.QueryMessageEnvelope(r.Context(), tenantID, queueID)
	if err != nil {
		s.log.Error("query message envelope", "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load message")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown message")
		return
	}

	trace, err := s.ch.QueryMessageTrace(r.Context(), tenantID, queueID)
	if err != nil {
		s.log.Error("query message trace", "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load timeline")
		return
	}
	timeline := make([]timelineEventJSON, 0, len(trace.Events))
	for _, e := range trace.Events {
		timeline = append(timeline, timelineEventJSON{
			EventTime: e.EventTime.UTC().Format(time.RFC3339), EventType: e.EventType,
			Provider: e.Provider, MXHost: e.MXHost, RecipientDomain: e.RecipientDomain,
			SMTPCode: e.SMTPCode, EnhancedStatus: e.EnhancedStatus, BounceClass: e.BounceClass,
			ResponseText: e.ResponseText,
		})
	}

	resp := map[string]any{
		"envelope": env,
		"timeline": timeline,
	}

	content, hasContent, err := s.ch.QueryMessageContent(r.Context(), tenantID, queueID)
	if err != nil {
		s.log.Error("query message content", "queue_id", queueID, "err", err)
		// Non-fatal: still return envelope + timeline without the spam/headers panels.
	}
	resp["content_captured"] = hasContent

	if hasContent {
		// Spam verdict + symbols are CLASSIFICATION METADATA — safe for any read caller.
		symbols := make([]spamSymbolJSON, 0, len(content.SymbolNames))
		for i, name := range content.SymbolNames {
			var sc float32
			if i < len(content.SymbolScores) {
				sc = content.SymbolScores[i]
			}
			symbols = append(symbols, spamSymbolJSON{Name: name, Score: sc})
		}
		resp["spam"] = map[string]any{
			"score":   content.SpamScore,
			"action":  content.SpamAction,
			"is_spam": content.IsSpam == 1,
			"symbols": symbols,
		}

		// Subject + raw headers are message CONTENT — gate behind the admin scope.
		if a, okAuth := authFromContext(r.Context()); okAuth && hasScope(a.Scopes, ScopeAdmin) {
			resp["content"] = map[string]any{
				"subject":        content.Subject,
				"raw_headers":    content.RawHeaders,
				"parsed_headers": parseHeaders(content.RawHeaders),
			}
		} else {
			resp["content_restricted"] = true // exists, but the caller lacks the admin scope
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// messageContentIn is the ingest payload posted by the rspamd Lua plugin
// (deploy/rspamd/mxs_trace.lua). The tenant is taken from the API token, never the body.
type messageContentIn struct {
	QueueID    string  `json:"queue_id"`
	MessageID  string  `json:"message_id"`
	MailFrom   string  `json:"mail_from"`
	Subject    string  `json:"subject"`
	RawHeaders string  `json:"raw_headers"`
	SpamScore  float32 `json:"spam_score"`
	SpamAction string  `json:"spam_action"`
	IsSpam     bool    `json:"is_spam"`
	Symbols    []struct {
		Name  string  `json:"name"`
		Score float32 `json:"score"`
	} `json:"symbols"`
}

// handleIngestMessageContent (POST /v1/ingest/message-content, ScopeWrite) accepts one
// message's captured content + spam verdict from the relay's rspamd plugin and upserts it into
// ClickHouse. This is the single content-bearing ingest path; it writes only to message_content
// (30-day TTL), which the AI layer never reads.
func (s *Server) handleIngestMessageContent(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	var in messageContentIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if strings.TrimSpace(in.QueueID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "queue_id is required")
		return
	}

	c := chstore.MessageContent{
		ReceivedAt: time.Now().UTC(),
		TenantID:   tenantID,
		QueueID:    in.QueueID,
		MessageID:  in.MessageID,
		MailFrom:   in.MailFrom,
		Subject:    in.Subject,
		RawHeaders: in.RawHeaders,
		SpamScore:  in.SpamScore,
		SpamAction: in.SpamAction,
	}
	if in.IsSpam {
		c.IsSpam = 1
	}
	for _, sym := range in.Symbols {
		c.SymbolNames = append(c.SymbolNames, sym.Name)
		c.SymbolScores = append(c.SymbolScores, sym.Score)
	}

	if err := s.ch.InsertMessageContent(r.Context(), c); err != nil {
		s.log.Error("insert message content", "queue_id", in.QueueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to store content")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reclassifyIn is the body for the "Not Spam" / "Spam" override.
type reclassifyIn struct {
	Spam bool `json:"spam"`
}

// handleReclassifyMessage (POST /v1/messages/{queueID}/reclassify, ScopeWrite) flips the
// operator's spam judgement for one message in the report. NOTE: this is a reporting override
// only — MX Sentinel does not store message bodies, so it cannot retrain rspamd's Bayes/fuzzy
// from here; true ham/spam learning must be done against the message on the mail store.
func (s *Server) handleReclassifyMessage(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	queueID := strings.TrimSpace(r.PathValue("queueID"))
	if queueID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "queueID is required")
		return
	}
	var in reclassifyIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	content, ok, err := s.ch.QueryMessageContent(r.Context(), tenantID, queueID)
	if err != nil {
		s.log.Error("query message content", "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load message")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no captured content for this message")
		return
	}

	content.IsSpam = 0
	if in.Spam {
		content.IsSpam = 1
	}
	content.ReceivedAt = time.Now().UTC() // newer row wins under ReplacingMergeTree
	if err := s.ch.InsertMessageContent(r.Context(), content); err != nil {
		s.log.Error("reclassify message", "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to reclassify")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue_id": queueID, "is_spam": content.IsSpam == 1})
}

// parseHeaders turns a raw RFC 5322 header block into ordered name/value pairs, unfolding
// multi-line continuations (leading space/tab). Order is preserved so the UI can render the
// header table in wire order, like the reference per-email view.
func parseHeaders(raw string) []headerJSON {
	if raw == "" {
		return []headerJSON{}
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := make([]headerJSON, 0, 32)
	for _, line := range lines {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// Folded continuation of the previous header value.
			if len(out) > 0 {
				out[len(out)-1].Value += " " + strings.TrimSpace(line)
			}
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		out = append(out, headerJSON{
			Name:  strings.TrimSpace(line[:idx]),
			Value: strings.TrimSpace(line[idx+1:]),
		})
	}
	return out
}
