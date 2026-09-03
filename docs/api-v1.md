# MX Sentinel REST API — v1

Served by `cmd/apid` (default `:8080`). Health/metrics are on the obs port
(`MXS_HTTPADDR`, default `:9090`): `GET /healthz`, `GET /metrics`.

## Authentication

Every `/v1` endpoint requires a Bearer token:

```
Authorization: Bearer mxs_<prefix>_<secret>
```

Create one with `mxctl apikey create --tenant <slug> --scopes read,write` (the token is
printed once). The token resolves to a tenant; **all queries are tenant-scoped** — a token
for one tenant cannot read another tenant's data. Only a SHA-256 hash of the token is
stored.

### Scopes (RBAC)

Tokens carry scopes; endpoints are gated by them (`admin` is a superset):

| Scope | Grants |
| --- | --- |
| `read` | all `GET` endpoints |
| `write` | mutating endpoints (`POST .../dns/recheck`, `POST /v1/incidents/{id}/resolve`) |
| `admin` | everything |

A call lacking the required scope returns **403** `{"error":{"code":"forbidden",...}}`.

### `GET /v1/me`
Returns the authenticated caller's tenant, scopes, and (for user sessions) role/user id:
```json
{ "tenant_id": "uuid", "scopes": ["read", "write"], "role": "owner", "user_id": "uuid" }
```

## User login & sessions

Besides long-lived API tokens, users can log in with email + password to get a **session
token** (prefix `mxs_sess_`, Redis-backed, 24h TTL). A session's scopes derive from the
user's role: `owner`/`admin` → read+write+admin, `operator` → read+write, `viewer` → read.
Bootstrap a user with `mxctl user create --tenant <slug> --email … --password … --role …`.

- **`POST /v1/auth/login`** (public) — `{ "email", "password" }` →
  `{ "token": "mxs_sess_…", "expires_at": "...", "user": { "id","email","tenant_id","role" } }`;
  **401** on bad credentials.
- **`POST /v1/auth/logout`** — revokes the caller's session → `{ "ok": true }`.
- **`POST /v1/me/password`** — change your own password (session callers only, not API
  tokens): `{ "current_password", "new_password" }` → `{ "ok": true }`. **403** if the
  current password is wrong; **400** if `new_password` < 8 chars.
- **`GET /v1/users`** (admin) — list tenant users.
- **`POST /v1/users`** (admin) — `{ "email","password","role" }` → **201** `{ "id", "email", "role" }`.

## Pagination

`GET /v1/messages` and `GET /v1/incidents` accept `?limit=&offset=`; responses echo the
applied `limit`/`offset` and a `count`.

## Errors

Non-2xx responses use:

```json
{ "error": { "code": "not_found", "message": "domain not found" } }
```

Codes: `unauthorized`, `invalid_token`, `forbidden`, `not_found`, `inspect_failed`,
`rate_limited`, `internal`.

## Rate limiting

Requests are rate-limited **per tenant** (fixed window, default 600/min — `apid
--rate-limit`, `0` disables). Over-limit requests get **429** with a `Retry-After` header
and `{"error":{"code":"rate_limited",...}}`. The counter is Redis-backed when available
(shared across `apid` instances) and falls back to in-process.

## Audit log

Every mutating request (`POST`/`PUT`/`PATCH`/`DELETE`) is recorded — tenant, credential,
method, path, status — and exposed at `GET /v1/audit` (read scope):
```json
{ "audit_events": [ { "id": "uuid", "credential_id": "uuid"|null,
                      "method": "POST", "path": "/v1/incidents/<id>/resolve",
                      "status": 200, "created_at": "..." } ] }
```

## Endpoints

### `GET /v1/domains`
List the tenant's domains with health derived from the latest DNS snapshot.
```json
{ "domains": [ {
  "id": "uuid", "name": "example.com", "status": "monitored",
  "categories": { "spf": "ok|warning|critical|unknown", "dkim": "...", "dmarc": "...", "mx": "..." },
  "overall": "healthy|warning|critical|unknown",
  "last_checked_at": "2026-06-03T10:45:00Z" | null,
  "finding_count": 2
} ] }
```

### `POST /v1/domains` (write)
Register a domain for DNS monitoring. `name` must be a valid domain name. Returns **409** if the domain already exists for this tenant.
```json
// request
{ "name": "example.com" }
// response 201
{ "domain": { "id": "uuid", "name": "example.com", "status": "pending_verification",
              "categories": { "spf": "unknown", ... }, "overall": "unknown", "finding_count": 0 } }
```

### `PATCH /v1/domains/{id}` (write)
Update a domain's monitoring status. Valid values for `status`: `monitored`, `paused`. Returns **404** if not found.
```json
// request
{ "status": "paused" }
// response
{ "ok": true }
```

### `DELETE /v1/domains/{id}` (admin)
Remove a domain and all its DNS snapshots, findings, and related data (CASCADE). Returns **404** if not found.
```json
{ "deleted": true }
```

### `GET /v1/domains/{id}/health`
Full health for one domain.
```json
{ "domain": { "id": "uuid", "name": "example.com", "status": "monitored" },
  "snapshot": { "id": "uuid", "captured_at": "...", "checksum": "...", "healthy": false } | null,
  "categories": { "spf": "...", "dkim": "...", "dmarc": "...", "mx": "..." },
  "overall": "critical",
  "findings": [ { "category": "spf", "severity": "critical", "code": "SPF_LOOKUP_LIMIT",
                  "message": "...", "detail": { "lookups": 12 } } ] }
```

### `GET /v1/domains/{id}/dns/snapshots?limit=50`
DNS drift timeline — snapshots newest-first.
```json
{ "snapshots": [ { "id": "uuid", "captured_at": "...", "checksum": "...",
                   "healthy": true, "finding_count": 0 } ] }
```

### `POST /v1/domains/{id}/dns/recheck`
Re-inspect now; persists a new snapshot if the posture changed. Returns the `health`
shape plus `"changed": bool`.

### `GET /v1/messages`
Message explorer over SMTP telemetry (persisted to ClickHouse by `ingestd`). Query params
(all optional except auth): `domain`, `sender`, `message_id`, `provider`, `outcome`
(`received|delivered|deferred|bounced|rejected`), `user` (authenticated SMTP submission
user — for per-account history), `since`/`until` (RFC 3339), `limit` (default 100, max
1000).
```json
{ "messages": [ {
  "event_id": "...", "event_time": "...", "event_type": "smtp.delivered", "outcome": "delivered",
  "message_id": "<...>", "from_domain": "example.com", "recipient_domain": "gmail.com",
  "provider": "google", "relay_ip": "198.51.100.5", "smtp_code": 250,
  "enhanced_status": "2.0.0", "bounce_class": "none", "response_text": "...",
  "sasl_username": "mailer@send.example.com" } ],
  "count": 1 }
```
Each message row now also carries `queue_id` — the relay-local queue id used to key shareable
trace links (below).

### Shareable message-trace links

A "did my message deliver?" tracking page for a single message — like courier tracking, built
from the SMTP telemetry. A link is a capability URL: the high-entropy token *is* the credential,
so it can be handed to a client without a login. The message is identified by its relay
`queue_id` (stable even when a message carries no `Message-ID`). Only the token's SHA-256 hash +
a non-secret lookup prefix are stored; links support expiry and revocation.

#### `POST /v1/messages/{queueID}/share` (write)
Mint a link for a message the tenant owns (404 if no telemetry exists for that queue id).
Body (optional): `{ "label": string, "ttl_hours": int }`. The `token` is returned **once**.
```json
{ "id": "uuid", "queue_id": "759CB8A8FD", "message_id": "<...>", "label": "",
  "url": "https://sentinel.squidix.net/trace/mxt_ab12cd34_…", "path": "/trace/mxt_…",
  "token": "mxt_ab12cd34_…", "active": true, "view_count": 0, "expires_at": null,
  "created_at": "..." }
```
`url` is absolute when `apid` runs with `MXS_PUBLIC_BASE_URL` set; otherwise it equals `path`
and the caller composes its own origin.

#### `GET /v1/messages/{queueID}/shares` (read)
List a message's links (metadata + status only — the plaintext token is never recoverable):
`{ "shares": [ { "id", "label", "active", "view_count", "expires_at", "revoked_at",
"last_viewed_at", "created_at" } ], "count": N }`.

#### `DELETE /v1/messages/shares/{id}` (write)
Revoke a link → `{ "revoked": true }`. Its URL then returns `410 Gone`.

#### `GET /v1/trace/{token}` (public, no auth)
Resolve a link to the message's delivery trace. Unknown/wrong tokens return a uniform `404`
(no enumeration signal); revoked/expired return `410`. Exposes only receipt-level data — sending
domain, recipient domain, provider, the status ladder, and each provider response. Internal
identifiers (queue id, relay IP, SMTP username, tenant) are never included; message bodies and
full recipient addresses are never stored, so they cannot leak here.
```json
{ "message_id": "<...>", "from_domain": "calcamino.com", "recipient_domain": "icloud.com",
  "provider": "apple", "status": "rejected", "label": "",
  "events": [
    { "event_time": "...", "event_type": "received", "provider": "", "mx_host": "",
      "recipient_domain": "icloud.com", "smtp_code": 0, "enhanced_status": "",
      "bounce_class": "none", "response_text": "" },
    { "event_time": "...", "event_type": "rejected", "provider": "apple",
      "mx_host": "mx01.mail.icloud.com", "recipient_domain": "icloud.com", "smtp_code": 554,
      "enhanced_status": "5.7.1", "bounce_class": "policy",
      "response_text": "554 5.7.1 [HM08] Message rejected due to local policy." } ],
  "checked_at": "..." }
```

### `GET /v1/dmarc/reports?domain=&limit=50`
Archived DMARC reports + aggregate alignment.
```json
{ "reports": [ { "id": "uuid", "org_name": "google.com", "report_id": "123",
                 "domain": "example.com", "date_begin": "...", "date_end": "...",
                 "record_count": 10 } ],
  "alignment": { "total": 1234, "dkim_aligned": 1200, "spf_aligned": 1100,
                 "dkim_pass_rate": 0.972, "spf_pass_rate": 0.891 } }
```

### `GET /v1/analytics/deliverability?since=&until=`
Per-provider outcome counts (Phase 2). `since`/`until` are RFC 3339, optional.
```json
{ "providers": [ { "provider": "google", "delivered": 980, "deferred": 5,
                   "bounced": 3, "rejected": 12, "total": 1000, "delivered_rate": 0.98 } ] }
```

### `GET /v1/analytics/rejections?since=&until=&limit=50`
Rejected/bounced events grouped and classified into reasons by the correlation engine's
provider-aware classifier (Phase 2).
```json
{ "reasons": [ { "reason": "auth", "count": 40 }, { "reason": "reputation", "count": 12 } ],
  "groups": [ { "smtp_code": 550, "enhanced_status": "5.7.26", "bounce_class": "auth",
                "provider": "google", "reason": "auth", "sample": "...", "count": 40 } ] }
```

### `GET /v1/incidents?status=&domain=&limit=50`
Persisted intelligence-layer incidents (Phase 2) — produced by `incidentd` from
correlation/reputation/DNS events. `status` (`open`/`acknowledged`/`resolved`) and
`domain` filters are optional.
```json
{ "incidents": [ {
  "id": "uuid", "source_event_id": "uuid", "kind": "rejection_spike",
  "severity": "critical", "domain": "example.com", "subject": "example.com",
  "title": "DKIM selector missing after DNS change", "detail": { },
  "status": "open", "confidence": 0.85,
  "created_at": "...", "resolved_at": null,
  "ai_summary": "Microsoft began rejecting for authentication after selector2 was removed...",
  "ai_remediation": [ { "action": "restore_dkim_selector", "summary": "...",
                        "target": "selector2._domainkey.example.com", "priority": "urgent" } ],
  "ai_model": "llama3", "ai_analyzed_at": "..." } ] }
```
The `ai_*` fields are populated asynchronously by `cmd/aid` (Phase 3) from a local LLM;
they are `null` until an incident has been analyzed.

### `POST /v1/incidents/{id}/resolve`
Marks an incident resolved → `{ "resolved": true }` (404 if not found for the tenant).

### `GET /v1/smtp-users` (admin)
Lists the tenant's SMTP submission users → `{ "users": [ { "id","username","domain","enabled","webmail_available","created_at" } ] }`.
Password hashes are never returned. `webmail_available` reports whether one-click webmail can
be opened for that user (see `docs/webmail-autologin.md`).

### `POST /v1/smtp-users` (admin)
`{ "username", "password", "domain"? }` → **201** `{ "id","username","domain","enabled" }`.
`username` is globally unique (the relay's SASL login); `password` is ≥ 8 chars and stored
as a bcrypt hash. **409** if the username already exists.

### `PATCH /v1/smtp-users/{id}` (admin)
`{ "enabled"?: bool, "password"?: string }` (at least one) → `{ "ok": true }`. Toggles the
account and/or resets its password. **404** if not found for the tenant.

### `DELETE /v1/smtp-users/{id}` (admin)
Removes the credential → `{ "deleted": true }` (404 if not found for the tenant).

### `POST /v1/smtp-users/{id}/webmail-session` (admin)
Mints a single-use Roundcube autologin URL for the credential → **201**
`{ "username","url","token","expires_at" }`. The token is valid for seconds and dies on first
use. **409** `disabled` (user is disabled) / `no_webmail_credential` (no sealed password —
reset the user's password); **503** `not_configured` when webmail is not set up. Full model in
`docs/webmail-autologin.md`.

### `POST /v1/webmail/redeem` (no tenant auth — `X-MXS-Webmail-Secret`)
Called only by the Roundcube `mxs_autologin` plugin. `{ "token" }` → `{ "username","password",
"imap_host","imap_port" }`, consuming the token. **401** for a bad secret, and **401**
`invalid_token` for a token that is expired, already redeemed, unknown, or whose user has since
been disabled — the cases are deliberately indistinguishable.

### `GET /v1/settings` (read)
Returns the tenant's mail settings → `{ "settings": { "spf_include","dkim_selector",
"dmarc_policy","dmarc_rua","dmarc_ruf","relay_host","relay_port","resolver_address",
"resolver_timeout_secs" } }`. Unset fields fall back to defaults (`dkim_selector=mxs`,
`dmarc_policy=none`, `relay_port=587`, `resolver_timeout_secs=5`).

### `PUT /v1/settings` (admin)
Replaces the tenant's mail settings (same shape) → `{ "settings": { … } }`.
`dmarc_policy` must be `none`|`quarantine`|`reject`. `resolver_address` is an IP/host
(optionally `:port`) or empty for the system resolver; `resolver_timeout_secs` is 1–60.
Stored under the `mail` key of the tenant's `settings` JSONB. These drive the recommended
DNS records, the smarthost connection instructions (see [smarthost.md](smarthost.md)), and
which DNS server `dnsd` + on-demand rechecks use to validate SPF/DKIM/DMARC.

### `GET /v1/analytics/top-senders?metric=volume&window=24h`
Ranked senders by the given metric (`volume`, `spam`, `rejected`) over a time window (`1h`, `24h`, `7d`, `30d`). Results are broken down by relay IP, envelope sender, and sending domain.
```json
{ "metric": "volume", "window": "24h",
  "by_ip":     [ { "key": "198.51.100.5", "count": 4200 } ],
  "by_sender": [ { "key": "mailer@send.example.com", "count": 3100 } ],
  "by_domain": [ { "key": "example.com", "count": 4200 } ] }
```

### `GET /v1/rbl/status`
Current DNSBL listing status for all relay egress IPs monitored by `rbld`. Includes a summary and per-IP breakdown.
```json
{ "checked_at": "2026-06-09T10:00:00Z",
  "summary": { "total_ips": 3, "healthy": 2, "listed": 1 },
  "ips": [ { "ip": "198.51.100.5", "healthy": false, "checked": true,
              "listings": [ { "zone": "zen.spamhaus.org", "listed": true,
                               "reason": "...", "listed_since": "..." } ] } ] }
```

### `GET /v1/anomaly/recent`
Recent send-volume anomalies detected by `anomalyd` (domains whose hourly count spiked beyond their learned baseline). Also returns the top current movers (highest ratio vs baseline).
```json
{ "anomalies": [ { "sender_domain": "example.com", "observed_hour_count": 5000,
                   "baseline": 500, "factor": 10.0, "detected_at": "..." } ],
  "top_movers": [ { "sender_domain": "example.com", "current": 5000, "baseline": 500, "ratio": 10.0 } ] }
```

### `GET /v1/reputation`
Feedback-loop complaint counts and Gmail Postmaster reputation for all monitored sending domains (populated by `fbld`).
```json
{ "domains": [ { "domain": "example.com", "complaints_24h": 3, "complaints_total": 47,
                  "postmaster_reputation": "HIGH", "spam_rate": 0.0003,
                  "fetched_at": "2026-06-09T09:00:00Z" } ] }
```

### `GET /v1/auth-security`
Credential compromise signals detected by `authwatchd` — per SMTP submission user, with recent signal details and lock status.
```json
{ "credentials": [ { "sasl_username": "mailer@send.example.com",
                      "recent_signals": [ { "signal": "recipient_blast",
                                            "detail": { "recipient_count": 1200 },
                                            "detected_at": "..." } ],
                      "locked": false, "locked_at": null } ] }
```

### `POST /v1/auth-security/{user}/lock` (admin)
Lock or unlock an SMTP submission credential flagged by `authwatchd`. `locked=true` disables the SASL login; `locked=false` re-enables it (equivalent to re-enabling via `/v1/smtp-users`).
```json
// request
{ "locked": true, "reason": "recipient blast detected" }
// response
{ "sasl_username": "mailer@send.example.com", "locked": true }
```

## Notes
- Routing uses the Go 1.22 stdlib `net/http` mux (`GET /v1/domains/{id}/health`,
  `r.PathValue("id")`) — no third-party router.
- CORS: `apid --cors-origin` (default `*`) so the dashboard can call it from the browser.
