# Integrations API

## Overview

MX Sentinel can sync with cPanel/WHM servers to discover the account→domain mapping for your hosting environment, then aggregate email telemetry per cPanel account and push those metrics to WHMCS for billing visibility. No credentials are ever exposed to the browser — all WHM and WHMCS API calls are made server-side by the `apid` and `cpaneld` services.

**Pipeline:**
1. **Sync** — `cpaneld` polls WHM API → discovers accounts + domain types (main, addon, parked)
2. **Match** — `smtp_events.from_domain` is resolved against `cpanel_domains` table in PostgreSQL
3. **Aggregate** — ClickHouse groups by `from_domain`; Go layer joins and sums per account
4. **Push** — WHMCS `AddBillableItem` (amount=0.00) records the summary per client

---

## Authentication

All `/v1/integrations/*` endpoints require a `Bearer` token in the `Authorization` header, identical to all other `/v1` endpoints. See `docs/api-v1.md` for token issuance.

Mutation endpoints require `admin` scope. Read endpoints require `read` scope.

---

## Configuration

| Environment variable | Description |
|---|---|
| `MXS_ENCRYPTION_KEY` | 64-char hex string (32 bytes). AES-256-GCM key for encrypting stored credentials. Generate: `openssl rand -hex 32`. If unset, credentials stored as plaintext with a startup warning. |

---

## cPanel Server Endpoints

### `GET /v1/integrations/cpanel`

List all configured WHM servers for the tenant.

**Response** `200`
```json
{
  "servers": [
    {
      "id": "019123ab-...",
      "tenant_id": "...",
      "label": "Production WHM",
      "hostname": "server1.example.com",
      "port": 2087,
      "username": "root",
      "api_token": "***",
      "verify_ssl": true,
      "sync_interval": 14400,
      "last_synced_at": "2026-06-10T04:00:00Z",
      "sync_status": "ok",
      "sync_error": "",
      "account_count": 142,
      "created_at": "2026-06-01T12:00:00Z"
    }
  ]
}
```

`api_token` is always redacted as `"***"` — it is write-only.

`sync_status` values: `pending` | `syncing` | `ok` | `error`

---

### `POST /v1/integrations/cpanel`

Add a WHM server. The API **pings the WHM endpoint** before saving — returns `422 ping_failed` if credentials are invalid or the server is unreachable.

**Scope:** `admin`

**Request body**
```json
{
  "label": "Production WHM",
  "hostname": "server1.example.com",
  "port": 2087,
  "username": "root",
  "api_token": "WHM_API_TOKEN_HERE",
  "verify_ssl": true,
  "sync_interval": 14400
}
```

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `label` | string | yes | — | Human-readable name |
| `hostname` | string | yes | — | WHM server hostname or IP |
| `port` | int | no | `2087` | WHM API port |
| `username` | string | yes | — | WHM admin or reseller username |
| `api_token` | string | yes | — | WHM API token (not password) |
| `verify_ssl` | bool | no | `true` | Verify TLS certificate |
| `sync_interval` | int | no | `14400` | Seconds between syncs (min 300) |

**Response** `201` — created server object (same shape as GET, `api_token` redacted)

**Errors**
- `400 missing_field` — required field absent
- `422 ping_failed` — cannot connect to WHM with provided credentials

---

### `GET /v1/integrations/cpanel/{id}`

Get a single WHM server by ID.

**Response** `200` — server object. `404 not_found` if not found.

---

### `PATCH /v1/integrations/cpanel/{id}`

Update label, verify_ssl, or sync_interval. Hostname/username/token cannot be changed after creation — delete and recreate instead.

**Scope:** `admin`

**Request body** (all fields optional)
```json
{
  "label": "New Label",
  "verify_ssl": false,
  "sync_interval": 7200
}
```

---

### `DELETE /v1/integrations/cpanel/{id}`

Remove the server and all synced accounts/domains (CASCADE). This does not affect ClickHouse telemetry.

**Scope:** `admin`

**Response** `204 No Content`

---

### `POST /v1/integrations/cpanel/{id}/sync`

Trigger an immediate background sync for this server. Returns immediately; check `sync_status` on `GET /v1/integrations/cpanel/{id}` to see the result.

**Scope:** `admin`

**Response** `202`
```json
{ "queued": true, "server_id": "019123ab-..." }
```

---

### `GET /v1/integrations/cpanel/{id}/accounts`

List all synced cPanel accounts for a WHM server.

**Query params**

| Param | Description |
|---|---|
| `suspended` | `true` or `false` — filter by suspension status |
| `search` | Substring match against username or primary domain |

**Response** `200`
```json
{
  "accounts": [
    {
      "id": "...",
      "server_id": "...",
      "username": "johndoe",
      "primary_domain": "johndoe.com",
      "owner_email": "john@example.com",
      "plan": "business",
      "suspended": false,
      "disk_used_mb": 1024,
      "synced_at": "2026-06-10T04:00:00Z"
    }
  ]
}
```

---

### `GET /v1/integrations/cpanel/{id}/accounts/{username}`

Single account detail including all domains and 30-day email metrics.

**Response** `200`
```json
{
  "account": {
    "id": "...",
    "username": "johndoe",
    "primary_domain": "johndoe.com",
    "owner_email": "john@example.com",
    "plan": "business",
    "suspended": false,
    "disk_used_mb": 1024,
    "synced_at": "2026-06-10T04:00:00Z",
    "domains": [
      { "domain": "johndoe.com", "domain_type": "main" },
      { "domain": "shop.johndoe.com", "domain_type": "addon" },
      { "domain": "johndoeshop.com", "domain_type": "parked" }
    ],
    "metrics": {
      "delivered": 4821,
      "deferred": 33,
      "bounced": 12,
      "rejected": 5,
      "total": 4871
    }
  }
}
```

`metrics` covers the last 30 days aggregated across all domains for this account.

---

### `GET /v1/integrations/cpanel/lookup?domain={domain}`

Reverse lookup: which cPanel account owns a given domain?

**Response** `200`
```json
{
  "cpanel_server_id": "019123ab-...",
  "account": {
    "username": "johndoe",
    "primary_domain": "johndoe.com",
    "owner_email": "john@example.com",
    "plan": "business"
  }
}
```

`404 not_found` if the domain is not in any synced server.

---

## WHMCS Connection Endpoints

### `GET /v1/integrations/whmcs`

List all WHMCS connections for the tenant.

**Response** `200`
```json
{
  "connections": [
    {
      "id": "...",
      "label": "Main Billing",
      "api_url": "https://billing.example.com/includes/api.php",
      "api_identifier": "abc123",
      "push_frequency": "daily",
      "push_metric_fields": ["delivered","bounced","rejected","total"],
      "enabled": true,
      "last_pushed_at": "2026-06-10T00:05:00Z",
      "created_at": "2026-06-01T12:00:00Z"
    }
  ]
}
```

`api_secret` is never returned — it is write-only.

---

### `POST /v1/integrations/whmcs`

Add a WHMCS connection. The API pings WHMCS before saving.

**Scope:** `admin`

**Request body**
```json
{
  "label": "Main Billing",
  "api_url": "https://billing.example.com/includes/api.php",
  "api_identifier": "YOUR_IDENTIFIER",
  "api_secret": "YOUR_SECRET",
  "push_frequency": "daily",
  "push_metric_fields": ["delivered","bounced","rejected","total"]
}
```

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `label` | string | yes | — | Human-readable name |
| `api_url` | string | yes | — | Full URL to WHMCS `api.php` |
| `api_identifier` | string | yes | — | WHMCS API identifier |
| `api_secret` | string | yes | — | WHMCS API secret (write-only) |
| `push_frequency` | string | no | `"daily"` | `"daily"` or `"weekly"` |
| `push_metric_fields` | []string | no | all four | Fields to include in push |

**Errors**: `422 ping_failed` if credentials invalid.

---

### `PATCH /v1/integrations/whmcs/{id}`

Update label, push_frequency, push_metric_fields, or enabled flag.

**Scope:** `admin`

```json
{
  "label": "Updated Label",
  "push_frequency": "weekly",
  "push_metric_fields": ["delivered","total"],
  "enabled": false
}
```

---

### `DELETE /v1/integrations/whmcs/{id}`

Remove the connection and its push log.

**Scope:** `admin` — `204 No Content`

---

### `POST /v1/integrations/whmcs/{id}/push`

Trigger an immediate metrics push in the background.

**Scope:** `admin`

**Response** `202`
```json
{ "queued": true, "connection_id": "..." }
```

---

### `GET /v1/integrations/whmcs/{id}/log`

Push history for a connection.

**Query params:** `limit` (default `20`, max `100`)

**Response** `200`
```json
{
  "log": [
    {
      "id": "...",
      "pushed_at": "2026-06-10T00:05:00Z",
      "accounts_pushed": 138,
      "status": "ok",
      "error_detail": "",
      "period_start": "2026-06-09T00:05:00Z",
      "period_end": "2026-06-10T00:05:00Z"
    }
  ]
}
```

`status` values: `ok` | `partial` | `error`

---

## Metrics Export

### `GET /v1/integrations/metrics?server_id={id}&from={RFC3339}&to={RFC3339}`

Per-cPanel-account email metrics for an arbitrary time range.

**Query params**

| Param | Required | Description |
|---|---|---|
| `server_id` | yes | cPanel server UUID |
| `from` | no | RFC3339 start (default: 30 days ago) |
| `to` | no | RFC3339 end (default: now) |

**Response** `200`
```json
{
  "server_id": "019123ab-...",
  "period": {
    "from": "2026-05-11T00:00:00Z",
    "to": "2026-06-10T00:00:00Z"
  },
  "accounts": [
    {
      "username": "johndoe",
      "primary_domain": "johndoe.com",
      "owner_email": "john@example.com",
      "metrics": {
        "delivered": 4821,
        "deferred": 33,
        "bounced": 12,
        "rejected": 5,
        "total": 4871
      }
    }
  ]
}
```

---

## WHMCS Push Behavior

When a push runs (scheduled by `cpaneld` or triggered via the API):

1. All cPanel servers for the tenant are loaded from PostgreSQL
2. For each server, the domain→account map is fetched
3. ClickHouse is queried for `from_domain` metric counts in the billing period
4. Results are aggregated per cPanel account username in Go
5. For each account with activity:
   - Look up WHMCS client by `owner_email` (exact match)
   - If not found, fall back to domain lookup via `GetClientsDomains`
   - If still not found, the account is skipped (logged at WARN level)
6. `AddBillableItem` is POSTed with `amount=0.00` and `invoiceaction=noinvoice`:
   ```
   MX Sentinel Email Report 2026-06-09 – 2026-06-10 | johndoe | Delivered=4821 Bounced=12 Rejected=5 Total=4871
   ```
7. Result is recorded in `whmcs_push_log`

Zero-activity accounts are skipped (no billable item created).

---

## `cpaneld` Daemon

The `cpaneld` binary handles both sync and push automatically.

**Sync schedule:** Each server defines its own `sync_interval` (default 4 hours). `cpaneld` checks every 60 seconds which servers are due and runs them.

**Push schedule:** Each WHMCS connection defines `push_frequency` (`daily` = every 24h, `weekly` = every 7 days). `cpaneld` checks every 60 seconds for due connections.

**Run it:**
```bash
make run-cpaneld
# or
MXS_ENCRYPTION_KEY=$(openssl rand -hex 32) go run ./cmd/cpaneld
```

---

## Error Codes

| Code | HTTP | Description |
|---|---|---|
| `not_found` | 404 | Resource not found |
| `ping_failed` | 422 | Could not connect to cPanel/WHMCS with provided credentials |
| `missing_field` | 400 | Required field absent |
| `invalid_body` | 400 | Request body is not valid JSON |
| `invalid_field` | 400 | Field value is invalid |
| `invalid_config` | 400 | Configuration parameters are invalid |
| `invalid_token` | 401 | Bearer token missing or invalid |
| `forbidden` | 403 | Token lacks required scope |
| `internal_error` | 500 | Unexpected server error |

---

## Security Notes

- **Credentials encrypted at rest** with AES-256-GCM when `MXS_ENCRYPTION_KEY` is set
- `api_token` (WHM) and `api_secret` (WHMCS) are **write-only** — never returned in GET responses
- All WHM and WHMCS API calls are made **server-side only** — credentials never reach the browser
- `AddBillableItem` with `amount=0.00` — no charges are created automatically
- cPanel sync uses the WHM JSON API v1 (port 2087) with API token auth — not password auth
