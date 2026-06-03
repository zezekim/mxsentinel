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
Message explorer over SMTP telemetry. Query params (all optional except auth):
`domain`, `sender`, `message_id`, `provider`, `outcome`
(`received|delivered|deferred|bounced|rejected`), `since`/`until` (RFC 3339), `limit`
(default 100, max 1000).
```json
{ "messages": [ {
  "event_id": "...", "event_time": "...", "event_type": "smtp.delivered", "outcome": "delivered",
  "message_id": "<...>", "from_domain": "example.com", "recipient_domain": "gmail.com",
  "provider": "google", "relay_ip": "198.51.100.5", "smtp_code": 250,
  "enhanced_status": "2.0.0", "bounce_class": "none", "response_text": "..." } ],
  "count": 1 }
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
Lists the tenant's SMTP submission users → `{ "users": [ { "id","username","domain","enabled","created_at" } ] }`.
Password hashes are never returned.

### `POST /v1/smtp-users` (admin)
`{ "username", "password", "domain"? }` → **201** `{ "id","username","domain","enabled" }`.
`username` is globally unique (the relay's SASL login); `password` is ≥ 8 chars and stored
as a bcrypt hash. **409** if the username already exists.

### `PATCH /v1/smtp-users/{id}` (admin)
`{ "enabled"?: bool, "password"?: string }` (at least one) → `{ "ok": true }`. Toggles the
account and/or resets its password. **404** if not found for the tenant.

### `DELETE /v1/smtp-users/{id}` (admin)
Removes the credential → `{ "deleted": true }` (404 if not found for the tenant).

### `GET /v1/settings` (read)
Returns the tenant's mail settings → `{ "settings": { "spf_include","dkim_selector",
"dmarc_policy","dmarc_rua","dmarc_ruf","relay_host","relay_port" } }`. Unset fields fall
back to defaults (`dkim_selector=mxs`, `dmarc_policy=none`, `relay_port=587`).

### `PUT /v1/settings` (admin)
Replaces the tenant's mail settings (same shape) → `{ "settings": { … } }`.
`dmarc_policy` must be `none`|`quarantine`|`reject`. Stored under the `mail` key of the
tenant's `settings` JSONB. These drive the recommended DNS records and the smarthost
connection instructions (see [smarthost.md](smarthost.md)).

## Notes
- Routing uses the Go 1.22 stdlib `net/http` mux (`GET /v1/domains/{id}/health`,
  `r.PathValue("id")`) — no third-party router.
- CORS: `apid --cors-origin` (default `*`) so the dashboard can call it from the browser.
