package alertchannels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// WebhookNotifier delivers to a generic HTTP endpoint as a JSON POST, optionally signed
// with an HMAC-SHA256 signature over the raw request body.
type WebhookNotifier struct {
	HTTP HTTPDoer
}

func (n WebhookNotifier) Type() string { return TypeWebhook }

func (n WebhookNotifier) Send(ctx context.Context, note Notification, cfg map[string]any) error {
	req, err := buildWebhookRequest(note, cfg)
	if err != nil {
		return err
	}
	return n.HTTP.Do(ctx, req)
}

// DefaultWebhookSigHeader is the header used to carry the HMAC signature when the config
// does not override it.
const DefaultWebhookSigHeader = "X-MXS-Signature"

// signBody returns the hex-encoded HMAC-SHA256 of body under secret, prefixed "sha256=".
// Exposed (unexported) helper kept pure so callers can verify signatures the same way.
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// buildWebhookRequest renders a Notification into a generic JSON webhook POST. Pure: no I/O.
// Config:
//
//	{
//	  "url": "https://example.com/hook",   // required
//	  "signing_secret": "...",              // optional; enables HMAC signature
//	  "signature_header": "X-Sig"           // optional; defaults to X-MXS-Signature
//	}
func buildWebhookRequest(note Notification, cfg map[string]any) (*HTTPRequest, error) {
	url := cfgString(cfg, "url")
	if url == "" {
		return nil, fmt.Errorf("webhook: url is required")
	}

	payload := map[string]any{
		"alert_ref": note.AlertRef,
		"title":     note.Title,
		"kind":      note.Kind,
		"severity":  note.Severity,
		"domain":    note.Domain,
		"summary":   note.Summary,
		"link_url":  note.LinkURL,
		"test":      note.Test,
	}
	if !note.OccurredAt.IsZero() {
		payload["occurred_at"] = note.OccurredAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webhook: marshal payload: %w", err)
	}

	header := map[string]string{"Content-Type": "application/json"}
	if secret := cfgString(cfg, "signing_secret"); secret != "" {
		sigHeader := cfgString(cfg, "signature_header")
		if sigHeader == "" {
			sigHeader = DefaultWebhookSigHeader
		}
		header[sigHeader] = signBody(secret, body)
	}

	return &HTTPRequest{
		Method: "POST",
		URL:    url,
		Header: header,
		Body:   body,
	}, nil
}
