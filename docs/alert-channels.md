# Alert delivery channels

MX Sentinel's alert **rules** decide *when* something is wrong; alert **channels** decide
*where the notification goes*. This feature adds configurable outbound notification
destinations per tenant, plus a daemon that fans a firing alert/incident out to the tenant's
enabled channels with per-channel throttling and dedup.

Supported channel types:

| Type | Transport | Secret (encrypted at rest) |
|---|---|---|
| `slack` | Slack incoming webhook (`POST` JSON) | `webhook_url` |
| `webhook` | Generic `POST` JSON, optional HMAC-SHA256 signature | `signing_secret` |
| `pagerduty` | PagerDuty Events API v2 (`trigger`) | `routing_key` |
| `email` | SMTP relay (plain-text message) | — (SMTP creds are daemon-level) |

## Data model

Two Postgres tables (migration `00025_alert_channels.sql`):

- **`alert_channels`** — one row per destination: `tenant_id`, `type`, `name`,
  `config_json` (JSONB; secret fields encrypted), `enabled`. Unique on `(tenant_id, name)`.
- **`alert_deliveries`** — append-only audit + dedup/throttle ledger: `channel_id`,
  `alert_ref` (incident id, or `test:<uuid>`), `status`
  (`sent` / `failed` / `skipped_throttle` / `skipped_dedup`), `error`, `sent_at`.

### Secret handling

Sensitive config fields are encrypted with the same AES-256-GCM `Encryptor`
(`internal/crypto`) used for cPanel/WHMCS credentials, keyed by `MXS_ENCRYPTION_KEY`. Values
are sealed on write (`SealConfig`) and opened on read for dispatch (`OpenConfig`). API
responses redact secrets to `***` (`RedactConfig`). Secrets are never logged. With no key
set, values are stored as plaintext with a startup warning (passthrough mode), matching the
existing integrations behaviour.

## Config JSON per type

```jsonc
// slack
{ "webhook_url": "https://hooks.slack.com/services/…" }

// webhook
{ "url": "https://example.com/hook",
  "signing_secret": "…",          // optional; enables HMAC signature
  "signature_header": "X-Sig" }   // optional; default X-MXS-Signature

// pagerduty
{ "routing_key": "<32-char integration key>" }

// email
{ "to": ["ops@example.com", "oncall@example.com"], "from": "alerts@…" }  // from optional
```

The generic webhook body is:

```json
{ "alert_ref": "…", "title": "…", "kind": "…", "severity": "…",
  "domain": "…", "summary": "…", "link_url": "…", "test": false,
  "occurred_at": "2024-01-01T00:00:00Z" }
```

When `signing_secret` is set, the request carries
`X-MXS-Signature: sha256=<hex hmac of the raw body>` (header name overridable).

## API

All routes are tenant-scoped and behind the standard scopes.

| Method & path | Scope | Purpose |
|---|---|---|
| `GET /v1/alert-channels` | read | list channels (secrets redacted) |
| `POST /v1/alert-channels` | write | create a channel |
| `PATCH /v1/alert-channels/{id}` | write | update name / enabled / config |
| `DELETE /v1/alert-channels/{id}` | admin | delete a channel |
| `POST /v1/alert-channels/{id}/test` | write | send a test notification now |

The test action bypasses dedup/throttle, sends synthetic metadata (never real message
content), and records the outcome in `alert_deliveries`.

## Dispatch: throttle & dedup

The dispatcher (`internal/alertchannels.Dispatcher`) fans a `Notification` out to a set of
channels. For each enabled channel:

1. **Dedup** — if this exact `alert_ref` was already delivered (`sent`) to the channel
   within the dedup window, skip (`skipped_dedup`).
2. **Throttle** — else if *any* alert was delivered (`sent`) to the channel within the
   throttle window, skip (`skipped_throttle`). This is what stops a flapping alert from
   spamming a channel.
3. Otherwise send via the matching driver and record the result.

Test notifications bypass both. Every decision (including skips) is written to the delivery
log. All non-determinism (clock via the DB `now()`, persistence, network) is injected, so
the logic is unit-tested with fakes and no live network.

### Driver design

Each driver builds its request payload with a **pure** function
(`buildSlackRequest`, `buildWebhookRequest`, `buildPagerDutyRequest`, `buildEmailMessage`) —
table-driven-tested. The actual send is behind the `HTTPDoer` / `Mailer` interfaces, faked
in tests; `make test` never touches the network.

## Daemon: `notifyd`

`cmd/notifyd` polls the `incidents` table for open (firing) incidents and dispatches each to
the affected tenant's enabled channels. Overlapping scans are safe — the delivery-log dedup
prevents re-notifying. Run with `make run-notifyd` (see INTEGRATION note) or
`go run ./cmd/notifyd`.

### Configuration (env)

| Env var | Default | Meaning |
|---|---|---|
| `MXS_NOTIFY_POLL_INTERVAL` | `30s` | scan cadence |
| `MXS_NOTIFY_LOOKBACK` | `10m` | how far back each scan reads incidents |
| `MXS_NOTIFY_THROTTLE` | `15m` | min gap between two sends to one channel |
| `MXS_NOTIFY_DEDUP` | `6h` | window suppressing a repeated `alert_ref` per channel |
| `MXS_NOTIFY_HTTP_TIMEOUT` | `10s` | per-request timeout for HTTP drivers |
| `MXS_NOTIFY_DASHBOARD_URL` | — | base URL for deep links in notifications |
| `MXS_SMTP_ADDR` | — | `host:port` for the email driver |
| `MXS_SMTP_USERNAME` / `MXS_SMTP_PASSWORD` | — | SMTP auth (omit for an unauthenticated relay) |
| `MXS_SMTP_FROM` | — | default From address for email notifications |

Durations accept Go duration strings (`30s`, `5m`) or a bare integer (seconds).

## Privacy

Notifications carry incident metadata only — title, kind, severity, domain. Message bodies
and subject lines never reach this package; the boundary is enforced upstream at the
telemetry parser.
