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
| `telegram` | Telegram Bot API `sendMessage` (`POST` JSON) | `bot_token` |

## Data model

Two Postgres tables (migration `00027_alert_channels.sql`; `00030_telegram_alert_channel.sql`
adds `telegram` to the type whitelist):

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

// telegram
{ "bot_token": "123456789:AA…",        // from @BotFather
  "chat_id": "-1001234567890",         // numeric id, or "@channelname"
  "message_thread_id": "42" }          // optional; a topic in a forum group
```

Any channel may additionally carry two plain (non-secret) routing flags, returned as-is by
the API:

| Flag | Default | Meaning |
|---|---|---|
| `login_alerts` | `false` | message this channel when the viewer account signs in — see [Login notifications](#login-notifications) |
| `incident_alerts` | `true` | include this channel in the firing-incident feed `notifyd` fans out |

They are independent, so a channel can carry both feeds, either one, or neither. A Telegram
bot that should only ever report sign-ins is
`{"login_alerts": true, "incident_alerts": false}`. The default (`incident_alerts` absent =
on) is what every channel created before the flag existed expects, so adding it changed no
existing behaviour.

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

On `PATCH`, a secret field whose value is still the redaction marker `***` (or absent) keeps
its stored value — so the dashboard can round-trip a channel to flip a flag without blanking
the Telegram bot token or the Slack webhook URL. Only a genuinely new value replaces the
stored secret.

## Dispatch: throttle & dedup

The dispatcher (`internal/alertchannels.Dispatcher`) fans a `Notification` out to a set of
channels. For each enabled channel:

1. **Dedup** — if this exact `alert_ref` was already delivered (`sent`) to the channel
   within the dedup window, skip (`skipped_dedup`).
2. **Throttle** — else if *any* alert was delivered (`sent`) to the channel within the
   throttle window, skip (`skipped_throttle`). This is what stops a flapping alert from
   spamming a channel.
3. Otherwise send via the matching driver and record the result.

Test notifications bypass both; login notifications bypass the throttle only
(`Notification.SkipSuppression`). Every decision (including skips) is written to the delivery
log. All non-determinism (clock via the DB `now()`, persistence, network) is injected, so
the logic is unit-tested with fakes and no live network.

### Driver design

Each driver builds its request payload with a **pure** function
(`buildSlackRequest`, `buildWebhookRequest`, `buildPagerDutyRequest`, `buildEmailMessage`,
`buildTelegramRequest`) —
table-driven-tested. The actual send is behind the `HTTPDoer` / `Mailer` interfaces, faked
in tests; `make test` never touches the network.

## Daemon: `notifyd`

`cmd/notifyd` polls the `incidents` table for open (firing) incidents and dispatches each to
the affected tenant's enabled channels that have not set `"incident_alerts": false`. Overlapping scans are safe — the delivery-log dedup
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

## Login notifications

A channel with `"login_alerts": true` in its config is messaged whenever the tenant's
**viewer** account signs in to the dashboard — the intended use is a Telegram bot that pings
the operator's phone when the customer-facing login is used.

- **Viewer role only.** `owner` / `admin` / `operator` sign-ins are the operator's own
  day-to-day traffic; alerting on those would bury the one event worth seeing. The rule is
  `loginAlertable` in `internal/api/login_alerts.go`.
- **Opt-in per channel.** A Slack channel wired up for incidents does not start reporting
  sign-ins on its own. Toggle it in Alert Channels → *Login alerts*, or set the flag in the
  channel config.
- **Independent of the incident feed.** Login alerts travel apid's own path, so a channel
  with `"incident_alerts": false` still receives them — that is how you get a Telegram bot
  that carries sign-ins and nothing else (Alert Channels → *Incidents* → Off).
- **Sent by `apid`, not `notifyd`.** The login is the event, so `handleLogin` dispatches it
  directly (`internal/api/login_alerts.go`). Delivery is asynchronous and best-effort, like
  the audit log: a dead Telegram endpoint can never turn a valid login into an error, and
  the outbound send never delays the login response.
- **Never suppressed.** Each sign-in gets a unique `alert_ref` (so dedup cannot collapse
  two logins into one) and sets `SkipSuppression` (so an unrelated flapping incident cannot
  swallow it through the per-channel throttle). Every send is still recorded in
  `alert_deliveries`.
- **Content.** Email, role, source IP (+ country when known), truncated user agent, and the
  time — no password, no session token.

#### Source IP behind a CDN

`clientIP` (`internal/api/clientip.go`) resolves the caller as
**`CF-Connecting-IP` → `True-Client-IP` → left-most `X-Forwarded-For` → `RemoteAddr`**,
skipping any entry that is not a valid IP.

Cloudflare fronts some hostnames in this deployment (e.g. `control.mxsentinel.app` via
`MXS_EXTRA_DOMAINS`). Every hop appends itself to `X-Forwarded-For`, so a request that
crossed the Cloudflare edge can reach apid with an edge address in the left-most position —
which is why the edge's own `CF-Connecting-IP`, set at Cloudflare and always the true client,
is preferred. `CF-IPCountry` supplies the country shown alongside it (`XX` = unknown,
`T1` = Tor).

These headers are trusted because apid is bound to `127.0.0.1` and reachable only through
Caddy; the value is shown in an audit trail and never used for authorization. Keep apid off
any public interface, or a caller could assert its own address.

Deep links use `MXS_PUBLIC_BASE_URL` when set, else `MXS_NOTIFY_DASHBOARD_URL`.

### Setting up Telegram

1. Message [@BotFather](https://t.me/BotFather) → `/newbot` → copy the token.
2. Add the bot to the target group/channel (or just DM it), then read the chat id — e.g.
   `curl "https://api.telegram.org/bot<TOKEN>/getUpdates"` after sending one message there.
   Group ids are negative (`-100…`); a public channel can use `@channelname` instead.
3. Alert Channels → **+ New Channel** → type *Telegram*, paste the token and chat id, tick
   **Notify this channel when the viewer account signs in**, and untick **Also send firing
   incidents here** if the bot should carry sign-ins only.
4. Press **Test** on the row — the bot should post a `[TEST]` message immediately.

## Privacy

Notifications carry incident metadata only — title, kind, severity, domain (plus account and
request metadata for a login notification). Message bodies and subject lines never reach this
package; the boundary is enforced upstream at the telemetry parser.
