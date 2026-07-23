# TLS Reporting (TLS-RPT + MTA-STS)

MX Sentinel's transport-security layer. It covers the two modern SMTP-over-TLS standards the
rest of the platform (SPF/DKIM/DMARC) does not:

- **MTA-STS (RFC 8461)** — a domain publishes a policy telling senders to *require* TLS to
  its MX hosts. We validate that policy the same way `dnsd` validates SPF/DKIM/DMARC.
- **TLS-RPT (RFC 8460)** — sending providers mail back JSON reports summarizing, per sending
  MTA, how many TLS sessions to your MX succeeded or failed and why. We ingest those the
  same way `dmarcd` ingests DMARC aggregate XML.

Both are driven by one daemon, **`tlsrptd`**.

## Part 1 — MTA-STS validation

On each poll cycle `tlsrptd` walks every monitored domain and, for each one:

1. Resolves `_mta-sts.<domain>` TXT (expects `v=STSv1; id=…`).
2. Fetches `https://mta-sts.<domain>/.well-known/mta-sts.txt` and parses it
   (`version`, `mode` ∈ {none, testing, enforce}, repeated `mx`, `max_age`).
3. For each concrete (non-wildcard) MX host in the policy, opens SMTP STARTTLS on `:25` and
   inspects the leaf certificate — validity against the MX hostname and `NotAfter` (expiry).
4. Serializes the resulting `State`, checksums it, and writes a new `mtasts_snapshots` row
   **only when the checksum changes** (drift detection, exactly like `dns_snapshots`).
5. If any finding is warning-or-worse, publishes a `dns.validation_failed` event (reusing
   the existing DNS event contract with category `mta_sts`) so it flows into correlation and
   alerting alongside the other DNS findings.

### Findings (machine codes)

| Code | Severity | Meaning |
|---|---|---|
| `MTASTS_TXT_MISSING` | warning | no `_mta-sts` TXT record |
| `MTASTS_TXT_INVALID` | warning | TXT record present but malformed |
| `MTASTS_POLICY_UNREACHABLE` | warning | policy HTTPS endpoint failed |
| `MTASTS_POLICY_INVALID` | critical | policy body malformed |
| `MTASTS_MODE_NOT_ENFORCED` | warning/info | mode is `none` (warn) or `testing` (info) |
| `MTASTS_NO_MX_HOSTS` | critical | enforcing policy lists no MX |
| `MTASTS_CERT_EXPIRED` | critical | an MX cert has expired |
| `MTASTS_CERT_EXPIRING_SOON` | warning | expires within `CERT_WARN_DAYS` (default 14) |
| `MTASTS_CERT_INVALID` | critical | MX cert invalid for the hostname |
| `MTASTS_CERT_UNREACHABLE` | warning | could not reach MX to check the cert |

The parsers (`ParsePolicy`, `ParseTXT`) and `Inspect` are pure and network-free — live
lookups happen behind the `Resolver`, `PolicyFetcher`, and `CertChecker` interfaces, which
are fed fixtures in the unit tests.

## Part 2 — TLS-RPT report ingestion

`tlsrptd` also watches a **drop directory** for TLS-RPT report files (`.json`, `.json.gz`).
For each file it archives the raw bytes to object storage, parses the JSON, dedupes on
`(tenant, report_id)`, writes a pointer row to Postgres and per-MTA detail rows to
ClickHouse, then moves the file to `processed/` (or `quarantine/` for malformed / no-tenant
reports). Malformed input is quarantined, never fatal — the pipeline mirrors `dmarcd`.

## Data model

**Postgres** (migration `00018_tls_rpt.sql`):

- `mtasts_snapshots` — versioned per-domain policy posture: `mode`, `max_age`, `mx_hosts`
  (jsonb), `checksum`, `cert_expiry`, `is_healthy`, `state` (jsonb), `findings` (jsonb).
- `tlsrpt_reports` — pointer/index rows for archived reports: `org_name`, `report_id`,
  `date_begin/end`, `object_key`, `policy_count`, `success_count`, `failure_count`.
  Unique on `(tenant_id, report_id)`.

**ClickHouse** (migration `00004_tlsrpt.sql`):

- `tlsrpt_results` — high-volume detail rows. One `result_type='successful'` summary row per
  policy plus one row per failure detail (`sending_mta_ip`, `receiving_mx_hostname`,
  `receiving_ip`, `failure_count`). `MergeTree`, partitioned by month.

**Object storage** — raw reports at `tlsrpt-raw/<tenant>/<yyyy>/<mm>/<report-id>.json.gz`,
quarantined ones under `tlsrpt-quarantine/`.

No message bodies, subjects, or recipients are ever stored — only transport metadata and
session counts (privacy boundary preserved).

## API

All read-scoped:

| Method + path | Returns |
|---|---|
| `GET /v1/tls-reporting/mta-sts` | latest MTA-STS policy state per domain |
| `GET /v1/tls-reporting/reports?domain=&limit=` | archived TLS-RPT reports + aggregate TLS success/failure summary (with `by_type` failure breakdown) |
| `GET /v1/domains/{id}/mta-sts` | latest MTA-STS snapshot for one domain |

## Configuration

Feature knobs (read by `tlsrptd` via env, precedent `internal/rbl.LoadConfig`):

| Env | Default | Meaning |
|---|---|---|
| `MXS_TLSRPT_DIR` | *(unset)* | TLS-RPT report drop directory (also `-dir` flag) |
| `MXS_MTASTS_INTERVAL` | `1h` | MTA-STS re-check interval (also `-interval` flag) |
| `MXS_TLSRPT_INTERVAL` | `30s` | drop-dir scan interval |
| `MXS_MTASTS_HTTP_TIMEOUT` | `10s` | policy fetch timeout |
| `MXS_MTASTS_CERT_TIMEOUT` | `10s` | STARTTLS dial/handshake timeout |
| `MXS_MTASTS_CERT_WARN_DAYS` | `14` | days-before-expiry warning threshold |

Infrastructure config (Postgres, ClickHouse, NATS, object store) comes from the shared
`internal/config.Load` like every other service. The event bus is best-effort: if NATS is
down, snapshots and reports are still persisted; only drift events are skipped.

## Running

```bash
go run ./cmd/tlsrptd -dir /var/spool/mxsentinel/tlsrpt -interval 1h
```
