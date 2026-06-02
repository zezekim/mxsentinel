# MX Sentinel REST API — v1

Served by `cmd/apid` (default `:8080`). Health/metrics are on the obs port
(`MXS_HTTPADDR`, default `:9090`): `GET /healthz`, `GET /metrics`.

## Authentication

Every `/v1` endpoint requires a Bearer token:

```
Authorization: Bearer mxs_<prefix>_<secret>
```

Create one with `mxctl apikey create --tenant <slug>` (the token is printed once). The
token resolves to a tenant; **all queries are tenant-scoped** — a token for one tenant
cannot read another tenant's data. Only a SHA-256 hash of the token is stored.

## Errors

Non-2xx responses use:

```json
{ "error": { "code": "not_found", "message": "domain not found" } }
```

Codes: `unauthorized`, `invalid_token`, `not_found`, `inspect_failed`, `internal`.

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

## Notes
- Routing uses the Go 1.22 stdlib `net/http` mux (`GET /v1/domains/{id}/health`,
  `r.PathValue("id")`) — no third-party router.
- CORS: `apid --cors-origin` (default `*`) so the dashboard can call it from the browser.
