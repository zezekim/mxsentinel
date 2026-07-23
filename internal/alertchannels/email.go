package alertchannels

import (
	"context"
	"fmt"
	"strings"
)

// EmailNotifier delivers an alert as a plain-text email. The SMTP connection details live
// in the Mailer (daemon-level config); the per-channel config only carries recipients, so
// no per-channel secret is stored for email.
type EmailNotifier struct {
	Mailer Mailer
	From   string // default From address (channel config may override)
}

func (n EmailNotifier) Type() string { return TypeEmail }

func (n EmailNotifier) Send(ctx context.Context, note Notification, cfg map[string]any) error {
	msg, err := buildEmailMessage(note, cfg, n.From)
	if err != nil {
		return err
	}
	return n.Mailer.Send(ctx, msg)
}

// buildEmailMessage renders a Notification into a plain-text EmailMessage. Pure: no I/O.
// Config: {"to": ["ops@example.com", ...], "from": "alerts@..." (optional)}.
func buildEmailMessage(note Notification, cfg map[string]any, defaultFrom string) (*EmailMessage, error) {
	to := cfgStringSlice(cfg, "to")
	if len(to) == 0 {
		return nil, fmt.Errorf("email: at least one recipient (to) is required")
	}
	from := cfgString(cfg, "from")
	if from == "" {
		from = defaultFrom
	}
	if from == "" {
		return nil, fmt.Errorf("email: no from address configured")
	}

	prefix := "[MX Sentinel] "
	if note.Test {
		prefix = "[MX Sentinel][TEST] "
	}
	subject := prefix + note.Title

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", note.Title)
	if note.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", note.Summary)
	}
	fmt.Fprintf(&b, "Severity: %s\n", strings.ToUpper(note.Severity))
	if note.Kind != "" {
		fmt.Fprintf(&b, "Kind:     %s\n", note.Kind)
	}
	if note.Domain != "" {
		fmt.Fprintf(&b, "Domain:   %s\n", note.Domain)
	}
	if !note.OccurredAt.IsZero() {
		fmt.Fprintf(&b, "When:     %s\n", note.OccurredAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	if note.LinkURL != "" {
		fmt.Fprintf(&b, "\nDetails:  %s\n", note.LinkURL)
	}
	b.WriteString("\n-- \nThis is an automated alert from MX Sentinel.\n")

	return &EmailMessage{
		From:    from,
		To:      to,
		Subject: subject,
		Body:    b.String(),
	}, nil
}
