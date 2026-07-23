package alertchannels

// NewRegistry builds the production set of channel drivers wired to real HTTP/SMTP
// transports, keyed by channel type. Shared by the API "send test" handler and notifyd so
// both deliver identically.
func NewRegistry(cfg Config) map[string]Notifier {
	http := NewHTTPDoer(cfg.HTTPTimeout)
	mailer := NewSMTPMailer(cfg.SMTPAddr, cfg.SMTPUsername, cfg.SMTPPassword)
	return map[string]Notifier{
		TypeSlack:     SlackNotifier{HTTP: http},
		TypeWebhook:   WebhookNotifier{HTTP: http},
		TypePagerDuty: PagerDutyNotifier{HTTP: http},
		TypeEmail:     EmailNotifier{Mailer: mailer, From: cfg.SMTPFrom},
	}
}
