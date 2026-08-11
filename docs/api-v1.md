# MX Sentinel REST API — v1

Served by `cmd/apid` (default `:8080`). Health/metrics are on the obs port
(`MXS_HTTPADDR`, default `:9090`): `GET /healthz`, `GET /metrics`.

## Authentication

Every `/v1` endpoint requires a Bearer token:

```
Authorization: Bearer mxs_<prefix>_<secret>
```

Create one with `mxctl apikey create --tenant <slug> --scopes read,write` (the token is
printed once), or mint one over the API — see [API keys & self-enrollment](#api-keys--self-enrollment).
The token resolves to a tenant; **all queries are tenant-scoped** — a token
for one tenant cannot read another tenant's data. Only a SHA-256 hash of the token is
stored.

### Scopes (RBAC)

Tokens carry scopes; endpoints are gated by them (`admin` is a superset):

| Scope | Grants |
| --- | --- |
| `read` | all `GET` endpoints |
| `write` | mutating endpoints (`POST .../dns/recheck`, `POST /v1/incidents/{id}/resolve`) |
| `relay` | creating/listing/updating SMTP submission users (`/v1/smtp-users`, except `DELETE`) — what a relay client such as the cPanel plugin needs at runtime |
| `provision` | one thing only: `POST /v1/apikeys`, and only narrowly (see below) |
| `admin` | everything |

`admin` satisfies **every** scope check, so tokens minted before `relay`/`provision` existed
keep working unchanged — there is no flag day and nothing to reissue.

A call lacking the required scope returns **403** `{"error":{"code":"forbidden",...}}`.

### `GET /v1/me`
Returns the authenticated caller's tenant, credential name, scopes, expiry, and (for user
sessions) role/user id:
```json
{ "tenant_id": "uuid", "credential_name": "cpanel-host.example.com",
  "scopes": ["read", "relay"], "expires_at": "2027-08-12T00:00:00Z",
  "role": "owner", "user_id": "uuid" }
```
- `credential_name` — the name the calling credential was minted under; `""` for user
  session tokens.
- `expires_at` — RFC 3339, and **absent from the response entirely** when the credential
  never expires (rather than `null` or a far-future date). A client can therefore tell
  "nothing to renew" from "expires later" without a sentinel value, which is exactly how a
  self-renewing client decides whether to bother — see
  [`POST /v1/apikeys/renew`](#post-v1apikeysrenew).

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

### `GET /v1/smtp-users` (relay)
Lists the tenant's SMTP submission users → `{ "users": [ { "id","username","domain","enabled","created_at" } ] }`.
Password hashes are never returned.

### `POST /v1/smtp-users` (relay)
`{ "username", "password", "domain"? }` → **201** `{ "id","username","domain","enabled" }`.
`username` is globally unique (the relay's SASL login); `password` is ≥ 8 chars and stored
as a bcrypt hash. **409** if the username already exists.

### `PATCH /v1/smtp-users/{id}` (relay)
`{ "enabled"?: bool, "password"?: string }` (at least one) → `{ "ok": true }`. Toggles the
account and/or resets its password. **404** if not found for the tenant.

### `DELETE /v1/smtp-users/{id}` (admin)
Removes the credential → `{ "deleted": true }` (404 if not found for the tenant).
Deletion stays admin-only on purpose: an enrolling server needs to create its own submission
user, never to tear down the ones other servers are authenticating with.

### API keys & self-enrollment

Tokens can be minted over the API as well as with `mxctl apikey create`. This exists so a
fleet of servers — the cPanel plugin in particular — can enroll itself without an admin token
ever landing on the remote host: hand the installer a narrow `provision` token and it mints
its own `read`+`relay` key, which is all it needs at runtime.

A key that can mint admin keys **is** an admin key, so `provision` is deliberately prevented
from escalating: it can only ask for scopes it is allowed to hand out, under a name shape it
is allowed to use.

#### `POST /v1/apikeys` (provision)
Mint a token for the caller's tenant. `expires_in` is optional (a Go duration string). The
`token` is returned **once** and is never retrievable afterwards — only its SHA-256 hash and
the non-secret `prefix` are stored.
```json
// request
{ "name": "cpanel-host.example.com", "scopes": ["read", "relay"], "expires_in": "8760h" }
// response 201
{ "id": "uuid", "name": "cpanel-host.example.com",
  "token": "mxs_xxxxxxxx_<40 hex>", "prefix": "mxs_xxxxxxxx",
  "scopes": ["read", "relay"], "expires_at": "2027-08-12T00:00:00Z" }
```
What a caller may ask for depends on the scopes it holds:

| Caller holds | Scopes it may mint | Name | Expiry | Name collision |
| --- | --- | --- | --- | --- |
| `admin` | any | any | as requested | **409** |
| `provision` (not `admin`) | a subset of `read`, `relay` | must match `^cpanel-[a-z0-9][a-z0-9.-]*$` | forced to 365 days | the existing credential is revoked and a fresh one issued (idempotent re-enrollment) |

#### `POST /v1/apikeys/renew`

Renew **the calling credential itself**. No request body, and **no particular scope** — any
valid API credential may renew itself. That is safe because renewal grants nothing new: the
reissued key keeps the same name and the same scopes, and gets only a fresh secret and a
pushed-out expiry. Gating it on `admin` or `provision` would just force long-lived keys to be
more privileged than their job needs.

**200** returns the same shape as `POST /v1/apikeys`, including the `token` — once.

**400** if the credential has no expiry (there is nothing to renew), or if the caller is a
user session token (sessions are renewed by logging in again, not here).

##### The 15-minute grace window

Renewal replaces the old secret, but **the old token keeps working for 15 minutes**
afterwards: the server dates the old credential's revocation 15 minutes into the future
rather than revoking it on the spot. This exists for the client that renews successfully and
then fails to *persist* the new token — crash, full disk, read-only filesystem. Without the
window such a client would be permanently locked out, holding a token the server no longer
accepts; with it, the client can simply retry with the token it still has.

The same window applies to the other replacement-by-reissue path: re-enrolling a host that
already has a key (`POST /v1/apikeys` from a `provision` caller) also leaves the displaced
key alive for 15 minutes, for the same reason — the installer may fail to write the new one.

Deliberate revocation is **immediate** — `mxctl apikey revoke` and `DELETE /v1/apikeys/{id}`
cut a key off at once, with no 15-minute tail, and that holds even for a key currently inside
a grace window. An operator killing a leaked key does not have to wait it out.

#### `GET /v1/apikeys` (admin)
Lists the tenant's API keys — metadata and status only; plaintext tokens are never recoverable.

#### `DELETE /v1/apikeys/{id}` (admin)
Revokes a key; it stops authenticating immediately — the renewal grace window above does not
apply to explicit revocation.

#### Enrollment flow

On a new server, with only a `provision`-scoped token:

```bash
curl -sS -X POST https://sentinel.example.com/v1/apikeys \
  -H "Authorization: Bearer $MXS_ENROLL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"cpanel-$(hostname -f)\",\"scopes\":[\"read\",\"relay\"]}"
# → 201 { "token": "mxs_xxxxxxxx_…", … }   store it now; it is never shown again
```

Re-running that on the same host is safe: the old credential for `cpanel-<fqdn>` is revoked
and replaced. A key minted this way expires in 365 days, but re-enrollment is not how it
stays alive: the holder keeps it current itself with `POST /v1/apikeys/renew`, so the
enrollment token is only needed to bootstrap a server (see
[cpanel-plugin.md → Token renewal](cpanel-plugin.md#token-renewal)).
From the MX Sentinel host, the same keys are managed with
`mxctl apikey list --tenant <slug>` and `mxctl apikey revoke --tenant <slug> --name <name>`
(`mxctl apikey create` also takes `--expires-in <duration>` and `--json`).

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
