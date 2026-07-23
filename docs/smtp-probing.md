# Synthetic SMTP probing

MX Sentinel's telemetry is mostly **passive** — it parses what Postfix already did. Synthetic
probing adds an **active** check: `probed` periodically opens real SMTP connections to the
relay's own listeners and records how they behave. It answers questions passive telemetry
can't, e.g. "is port 587 accepting connections right now?", "does STARTTLS still negotiate?",
and "when does the TLS certificate expire?" — before a customer's mail is affected.

This is relay-wide **infrastructure** state (like RBL self-monitoring in `internal/rbl`), not
tenant-scoped: targets come from the environment, results are global, and the API serves them
behind `ScopeRead`.

## What a probe measures

For each configured endpoint (`host:port` + transport mode) a single probe performs, in order:

1. **TCP connect** and records the connect **latency**.
2. **Implicit TLS** handshake first, for `implicit_tls` endpoints (port 465 / SMTPS).
3. **SMTP banner** read (expects a `2xx` greeting).
4. **EHLO** and parses the advertised **capabilities** (STARTTLS, AUTH + mechanisms,
   PIPELINING, SIZE, 8BITMIME, ENHANCEDSTATUSCODES).
5. **STARTTLS** negotiation for `starttls` endpoints (587), and opportunistically on `plain`
   endpoints that advertise it. After the upgrade it re-issues EHLO (many servers only
   advertise AUTH once the session is encrypted).
6. **TLS certificate inspection**: leaf subject/issuer, `NotAfter`, **days until expiry**,
   whether it is within the warning window, whether it is already **expired**, and whether a
   **valid chain** to a trusted root was built.
7. **AUTH advertisement** (is authenticated submission offered, and by which mechanisms).
8. *(optional)* **Response-behaviour sampling**: a `MAIL FROM` / `RCPT TO` pair (no `DATA` is
   ever sent) to observe the reply code — a `4xx` on RCPT is flagged as **greylisting**.

A probe never sends message content: there is no body or subject line involved, so the
privacy boundary is trivially preserved.

## Architecture

```
probed (cmd/probed)  ──probe──▶  relay SMTP endpoints (25/465/587)
        │
        ├─ Postgres  smtp_probe_results   (current status + recent history)   ← API reads here
        ├─ ClickHouse smtp_probe_results  (dense latency/uptime time series)   ← history/rollups
        └─ NATS (reputation.*)  ──▶  incidentd  ──▶  incidents                 ← probe failed / cert expiring
```

- The probe logic (`internal/smtpprobe`) is built on the `Dialer` and `TLSHandshaker`
  interfaces, so the whole conversation is unit-tested with in-memory fakes — no live network
  and no real ports are touched by `make test`. `ParseEHLO` and `InspectCerts` are pure and
  tested directly.
- **Recording is the primary job.** ClickHouse and NATS are best-effort: if either is down,
  `probed` still records to Postgres and keeps probing. Probing never blocks mail flow.

## Data model

### Postgres (`migrations/postgres/00022_smtp_probe.sql`)

- `smtp_probe_targets` *(optional)* — the configured endpoint universe, so the dashboard can
  show an endpoint before its first result lands. Keyed on `endpoint` (`host:port`).
- `smtp_probe_results` — one row per probe execution: `ok`, `stage`, `error`, `latency_ms`,
  `banner`, capability flags, `auth_mechs`, TLS/cert columns (`tls_negotiated`, `tls_version`,
  `tls_chain_valid`, `cert_subject`, `cert_issuer`, `cert_not_after`, `cert_days_until_expiry`,
  `cert_expiring`), `greylisting`, `response_code`, `probed_at`. Indexed for
  `DISTINCT ON (endpoint) … ORDER BY probed_at DESC` (current status) and for scanning recent
  problems. Relay-wide — **no `tenant_id`** (mirrors `ip_blocklist_status`).

### ClickHouse (`migrations/clickhouse/00008_smtp_probe.sql`)

- `smtp_probe_results` — high-frequency probe rows (`MergeTree`, partitioned by month, 90-day
  TTL, ordered by `(endpoint, probed_at)`) — the backing for latency/uptime charts.
- `smtp_probe_uptime_1h` + materialized view — per-endpoint hourly uptime/latency rollup so
  long-window dashboards don't scan raw rows.

## API

Both routes are relay-wide reads behind `ScopeRead` (registered via
`registerSMTPProbeRoutes`).

- `GET /v1/smtp-probes` — current status of every configured endpoint. The endpoint universe
  is the same env config `probed` uses (falling back to `smtp_probe_targets`), so an endpoint
  that has never been probed still appears (`probed: false`). Response:

  ```json
  {
    "probed_at": "2026-07-21T12:00:00Z",
    "summary": {"total_endpoints": 3, "healthy": 2, "failing": 1, "unprobed": 0, "cert_warnings": 1},
    "endpoints": [
      {
        "endpoint": {"host": "relay.example.com", "port": 587, "mode": "starttls"},
        "probed": true, "ok": true, "latency_ms": 24,
        "starttls_offered": true, "auth_advertised": true, "auth_mechs": ["PLAIN","LOGIN"],
        "tls": {"negotiated": true, "version": "TLS 1.3", "chain_valid": true,
                "cert_subject": "relay.example.com", "days_until_expiry": 61, "expiring": false},
        "greylisting": false, "probed_at": "2026-07-21T12:00:00Z"
      }
    ]
  }
  ```

- `GET /v1/smtp-probes/history?endpoint=&since=&until=&limit=` — per-probe latency/uptime
  history plus a per-endpoint uptime rollup. Served from ClickHouse; falls back to the recent
  Postgres rows if ClickHouse is unavailable (the `source` field says which).

## Events / incidents

When a probe **fails** or a certificate is **within the warning window / expired**, `probed`
emits an incident-eligible event, deduplicated per endpoint+kind (30 min cooldown for
failures, 12 h for cert warnings).

> **Design note.** The event envelope schema (`schemas/events/envelope.schema.json`, a shared,
> immutable contract) restricts `event_type` to the `smtp|dns|reputation|ai` families. Rather
> than introduce an unroutable fifth family, probe signals are modelled as
> `reputation.rate_anomaly` events, so they flow through the existing `incidentd` consumer
> (the `REPUTATION` stream) and surface as incidents with a custom title carried in
> `detail.root_cause`. See `INTEGRATION_smtp-probes.md` for how to promote these to a
> dedicated `smtpprobe.*` family later.

## Configuration

`probed` reads its own config from the environment (precedent: `internal/rbl.LoadConfig`):

| Env var | Default | Meaning |
|---|---|---|
| `MXS_PROBE_ENDPOINTS` | — | `host:port[:mode]` CSV, e.g. `relay.example.com:587:starttls,relay.example.com:465:implicit_tls`. Mode defaults from the port. |
| `MXS_PROBE_HOST` | `RELAY_HOST`/`RELAY_FQDN`/`RELAY_NODE_IP` | Host used when only ports are given. |
| `MXS_PROBE_PORTS` | `25,587,465` | Ports to probe on `MXS_PROBE_HOST`. |
| `MXS_PROBE_INTERVAL` | `60s` | Probe cadence. |
| `MXS_PROBE_EHLO_NAME` | `mxsentinel.probe` | Name sent in EHLO/HELO. |
| `MXS_PROBE_CERT_WARN` | `336h` (14d) | Cert-expiry warning lead time. |
| `MXS_PROBE_CONNECT_TIMEOUT` / `MXS_PROBE_COMMAND_TIMEOUT` | `10s` / `10s` | Dial / per-command deadlines. |
| `MXS_PROBE_CHECK_RESPONSE` | off | Sample `MAIL`/`RCPT` to detect greylisting. |
| `MXS_PROBE_TLS_INSECURE` | off | Skip chain-validity reporting (handshake always completes so expired certs are still inspected). |
| `MXS_PROBE_TENANT_ID` | `RELAY_TENANT_ID` | Tenant UUID stamped on emitted events. When empty, `probed` records results but emits no events. |

## Running

```bash
make run-probed          # or: go run ./cmd/probed
go run ./cmd/probed -interval 30s
```

`probed` exposes the standard `/healthz` + `/metrics` on `MXS_HTTP_ADDR` like every other
daemon.
