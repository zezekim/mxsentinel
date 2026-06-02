# Data Model & Storage Architecture

MX Sentinel uses four storage tiers, each chosen for a distinct access pattern. The
guiding rule: **slowly-changing relational truth in Postgres, high-volume append-only
telemetry in ClickHouse, ephemeral hot state in Redis, large opaque blobs in object
storage.**

---

## Storage tiers at a glance

| Store | Workload | Holds | Why |
| --- | --- | --- | --- |
| **PostgreSQL** | OLTP, relational integrity | tenants, users, domains, DNS snapshots & findings, alert rules, IP pools, configuration | Joins, constraints, transactions, the system of record. |
| **ClickHouse** | OLAP, time-series, high ingest | SMTP telemetry events, aggregated deliverability rollups | Columnar; built for billions of append-only rows and fast time-window analytics. |
| **Redis** | Hot, ephemeral, sub-ms | caches, rate-limit counters, dedupe sets, session/API-key lookups, work queues | Speed; data is reconstructable and TTL'd. |
| **Object storage (S3/MinIO)** | Large immutable blobs | raw DMARC XML, compressed maillogs, forensic reports | Cheap durable bytes; referenced by ID from Postgres/ClickHouse. |

A telemetry event's *lifecycle* touches three of them: parsed at the relay → published to
NATS → written in batches to **ClickHouse**; the raw source artifact (e.g. DMARC XML) is
archived in **object storage** with a pointer row; correlation enriches it using
slowly-changing facts read from **Postgres**.

---

## Privacy boundary (non-negotiable)

> We never store full message bodies or attachments. Parsers extract **headers,
> envelope metadata, auth results, and SMTP telemetry only**, and discard the body before
> anything leaves the relay node.

Practical rules baked into the schema:

- **Recipient addresses are PII.** ClickHouse always stores `recipient_domain`. The full
  recipient address is stored *only* as `recipient_hash` (keyed hash) unless a tenant
  explicitly opts into plaintext retention for their own debugging. Same policy for
  envelope sender local-parts where feasible.
- **Subject lines are not stored** by default (they can leak content). A length and a hash
  may be kept for dedupe/correlation.
- **Response text from remote MTAs is truncated** (e.g. 512 chars) — it's diagnostic, not
  archival.
- **Object-storage blobs (DMARC XML) contain no message content** — DMARC aggregate
  reports are themselves metadata. Forensic (failure/`ruf`) reports, which *can* contain
  headers, are access-controlled and TTL'd.

---

## PostgreSQL — entity model

Full DDL: [`../schemas/postgres/001_init.sql`](../schemas/postgres/001_init.sql).

Core entities and relationships:

```
tenants ──1:N── users
   │  └─1:N── api_credentials
   │  └─1:N── notification_channels
   │  └─1:N── alert_rules ──1:N── alert_events
   │  └─1:N── ip_pools
   │  └─1:N── relay_nodes
   └─1:N── domains
              └─1:N── dns_snapshots ──1:N── dns_findings
```

| Table | Purpose |
| --- | --- |
| `tenants` | Top-level isolation boundary (hosting provider / MSP / enterprise). |
| `users` | People who log in; scoped to a tenant; `role` drives RBAC. |
| `api_credentials` | Hashed API tokens with scopes; never plaintext. |
| `notification_channels` | Where alerts go (email/slack/webhook); config encrypted. |
| `domains` | Customer sending domains, with verification + monitoring status. |
| `dns_snapshots` | **Versioned, timestamped** capture of a domain's DNS state. The differentiator: lets us say "at 10:44 the record changed." |
| `dns_findings` | Per-snapshot validation results (category, severity, code, message). |
| `ip_pools` | Outbound IP pool segmentation (transactional / marketing / warmup). |
| `relay_nodes` | Registered relay hosts emitting telemetry. |
| `alert_rules` | Tenant-defined conditions (threshold + window) over telemetry/DNS/reputation. |
| `alert_events` | Firings of a rule, with state (open/ack/resolved) and payload. |

Every tenant-scoped table carries `tenant_id`; the schema is designed so **Postgres
row-level security** can later enforce isolation at the database layer.

### DNS snapshotting model

Snapshotting is what powers root-cause correlation. Each poll of a domain writes one
`dns_snapshots` row capturing the **full parsed state** (as JSONB) plus a content
`checksum`. A new snapshot is only retained when the checksum differs from the previous
one (change-detection), so storage stays bounded while every *change* is preserved with
its timestamp. `dns_findings` rows hang off each snapshot so the dashboard can show "what
was wrong, and since when."

---

## ClickHouse — telemetry model

Full DDL: [`../schemas/clickhouse/001_smtp_telemetry.sql`](../schemas/clickhouse/001_smtp_telemetry.sql).

The central table `smtp_events` is a wide, denormalized, append-only fact table. Design
choices:

- **Partition** by month (`toYYYYMM(event_time)`) — efficient TTL and pruning.
- **Order by** `(tenant_id, event_time, message_id)` — most queries filter by tenant and
  time window, then drill into a message.
- **TTL** on `event_time` for automatic retention (configurable per deployment).
- **Enums** for `event_type`, `bounce_class`, `provider`, auth results — compact and fast.
- **Low-cardinality** wrappers on repetitive strings (provider, dkim_selector, etc.).

Correlation keys are first-class columns (`message_id`, `queue_id`, `session_id`,
`source_ip`, `relay_ip`, `dkim_selector`, `envelope_from`) so the correlation engine can
join SMTP events to DNS snapshots and reputation events without a separate index service.

**Rollups.** Materialized views maintain pre-aggregated deliverability metrics
(per tenant × provider × time bucket: counts of delivered/deferred/bounced/rejected,
median latency) so the dashboard reads cheap summaries instead of scanning raw events.

---

## Redis — hot state

- **Rate limiting:** token buckets per tenant / per IP pool (shared with relay where
  applicable).
- **Dedupe:** short-TTL sets keyed by `event_id` to make event consumers idempotent.
- **Lookups:** cached tenant/domain/api-key resolution to keep the API and ingest paths
  off Postgres on the hot path.
- **Queues:** lightweight work queues (e.g. DNS re-check requests) where JetStream is
  overkill.

Nothing in Redis is a source of truth; everything is TTL'd and reconstructable.

---

## Object storage — blobs

- `dmarc-raw/<tenant>/<yyyy>/<mm>/<report-id>.xml.gz` — raw DMARC aggregate reports,
  archived on receipt; a pointer row records the path + metadata.
- `logs/<tenant>/...` — compressed maillog segments for forensic replay.
- `forensic/<tenant>/...` — DMARC failure reports; access-controlled, TTL'd.

Object keys are deterministic and tenant-prefixed for isolation and lifecycle policies.

---

## Multi-tenancy enforcement summary

| Layer | Mechanism |
| --- | --- |
| Postgres | `tenant_id` on every scoped table; RLS-ready. |
| ClickHouse | `tenant_id` leads the sort key; all queries are tenant-filtered. |
| Object storage | tenant-prefixed key namespaces + bucket policies. |
| API | tenant resolved from API credential / session before any query. |
| Events | `tenant_id` in the envelope and the subject; consumers filter by subject. |
