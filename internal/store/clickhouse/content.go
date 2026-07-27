package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// MessageContent is the per-message content + spam verdict captured by the rspamd Lua plugin
// (deploy/rspamd/mxs_trace.lua) and stored in the message_content table. It is the backing for
// the "Spam Tests" and "Headers" tabs of the per-message drill-down.
//
// This is the one place MX Sentinel holds message content (subject + raw headers). It is stored
// apart from smtp_events, carries a 30-day TTL, and the AI layer never reads it. See
// migrations/clickhouse/00009_message_content.sql for the full privacy note.
type MessageContent struct {
	ReceivedAt   time.Time `json:"received_at"`
	TenantID     string    `json:"-"`
	QueueID      string    `json:"queue_id"`
	MessageID    string    `json:"message_id"`
	MailFrom     string    `json:"mail_from"`
	Subject      string    `json:"subject"`
	RawHeaders   string    `json:"raw_headers"`
	SpamScore    float32   `json:"spam_score"`
	SpamAction   string    `json:"spam_action"`
	IsSpam       uint8     `json:"is_spam"`
	SymbolNames  []string  `json:"symbol_names"`
	SymbolScores []float32 `json:"symbol_scores"`
}

const messageContentInsertStmt = `INSERT INTO message_content (
	received_at, tenant_id, queue_id, message_id, mail_from, subject, raw_headers,
	spam_score, spam_action, is_spam, symbol_names, symbol_scores
)`

// InsertMessageContent writes (upserts, via ReplacingMergeTree) one message's captured content
// and spam verdict. Re-ingesting the same (tenant_id, queue_id) replaces the prior row.
func (s *Store) InsertMessageContent(ctx context.Context, c MessageContent) error {
	if c.ReceivedAt.IsZero() {
		c.ReceivedAt = time.Now().UTC()
	}
	batch, err := s.conn.PrepareBatch(ctx, messageContentInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare message_content batch: %w", err)
	}
	names := c.SymbolNames
	if names == nil {
		names = []string{}
	}
	scores := c.SymbolScores
	if scores == nil {
		scores = []float32{}
	}
	if err := batch.Append(
		c.ReceivedAt, c.TenantID, c.QueueID, c.MessageID, c.MailFrom, c.Subject, c.RawHeaders,
		c.SpamScore, c.SpamAction, c.IsSpam, names, scores,
	); err != nil {
		return fmt.Errorf("append message_content row: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send message_content batch: %w", err)
	}
	return nil
}

// QueryMessageContent returns the captured content for one message (tenant_id + queue_id), or
// ok=false if none was captured (e.g. the message predates rspamd capture, or content has aged
// out past the 30-day TTL). FINAL collapses ReplacingMergeTree duplicates to the latest row.
func (s *Store) QueryMessageContent(ctx context.Context, tenantID, queueID string) (MessageContent, bool, error) {
	const q = `SELECT received_at, queue_id, message_id, mail_from, subject, raw_headers,
	                  spam_score, spam_action, is_spam, symbol_names, symbol_scores
	           FROM message_content FINAL
	           WHERE tenant_id = ? AND queue_id = ?
	           LIMIT 1`

	rows, err := s.conn.Query(ctx, q, tenantID, queueID)
	if err != nil {
		return MessageContent{}, false, fmt.Errorf("query message_content: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return MessageContent{}, false, fmt.Errorf("query message_content: %w", err)
		}
		return MessageContent{}, false, nil
	}
	var c MessageContent
	c.TenantID = tenantID
	if err := rows.Scan(
		&c.ReceivedAt, &c.QueueID, &c.MessageID, &c.MailFrom, &c.Subject, &c.RawHeaders,
		&c.SpamScore, &c.SpamAction, &c.IsSpam, &c.SymbolNames, &c.SymbolScores,
	); err != nil {
		return MessageContent{}, false, fmt.Errorf("scan message_content: %w", err)
	}
	return c, true, nil
}
