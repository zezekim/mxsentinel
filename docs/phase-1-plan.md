# Phase 1 — Foundation: Implementation Plan

**Goal of Phase 1:** get real data flowing end-to-end and visible. By the end, a relay
node emits structured SMTP telemetry into ClickHouse, a DNS validator snapshots customer
domains into Postgres, DMARC aggregate reports are ingested and archived, and a minimal
dashboard/API surfaces domain health and a message explorer.

Phase 1 explicitly does **not** include the correlation engine (Phase 2) or AI reasoning
(Phase 3). It builds the substrate they depend on.

**Definition of done for Phase 1:** From a clean `docker compose up`, an operator can
(1) register a tenant + domain, (2) see live DNS validation findings for that domain,
(3) see SMTP telemetry events flowing from a relay into ClickHouse and rendered in a
message explorer, and (4) see DMARC reports ingested and listed. All services structured-log
and expose health/metrics endpoints.

---

## Workstreams

Workstreams are sequenced by dependency. WS0 and WS1 unblock everything; WS2 unblocks the
event-driven services; WS3–WS5 produce data in parallel; WS6–WS7 consume it.

### WS0 — Repo & local-dev foundation

*Unblocks: everything. No dependencies.*

- [ ] Initialize Go module (`go.mod`), `go vet` / `golangci-lint` config, `Makefile`.
- [ ] `deploy/docker-compose.yml` bringing up Postgres, ClickHouse, Redis, NATS
      (JetStream enabled), MinIO — with health checks and seed config.
- [ ] `internal/config` — env+file config loading (koanf), per-service config structs.
- [ ] Structured logging (`slog`) + Prometheus metrics bootstrap shared package.
- [ ] `mxctl` skeleton (cobra/std flags) for migrations and operational commands.
- [ ] CI: build, vet, lint, unit tests, schema-lint (validate JSON Schemas + SQL parses).

**Acceptance:** `make up` starts all backing services healthy; `make test` is green on an
empty service set; `mxctl version` runs.

### WS1 — Data layer (schemas → migrations → access)

*Depends on: WS0. Source DDL already drafted in `schemas/`.*

- [ ] Convert `schemas/postgres/001_init.sql` into versioned migrations under
      `migrations/postgres` (goose). Wire `mxctl migrate`.
- [ ] Convert `schemas/clickhouse/001_smtp_telemetry.sql` into `migrations/clickhouse`
      (incl. materialized-view rollups).
- [ ] `internal/store/postgres` — pgx pool + `sqlc`-generated queries for core entities.
- [ ] `internal/store/clickhouse` — client + batched `smtp_events` writer.
- [ ] `internal/store/objectstore` — S3/MinIO client with tenant-prefixed key helpers.
- [ ] Seed/fixtures: a demo tenant, user, and domain for local dev.

**Acceptance:** migrations apply cleanly forward & back; integration tests
(testcontainers) round-trip a domain through Postgres and a batch of events through
ClickHouse; rollup MV produces correct counts on inserted fixtures.

### WS2 — Event bus & shared contracts

*Depends on: WS0. Parallel with WS1.*

- [ ] Generate `pkg/contracts` Go types from `schemas/events/*.json`; CI check that
      generated types are in sync.
- [ ] `internal/events` — JetStream connection, stream/consumer setup
      (`SMTP`/`DNS`/`REPUTATION`/`AI`), typed publish helper, subject builders.
- [ ] Envelope construction (UUIDv7 `event_id`, `occurred_at`/`ingested_at`, correlation
      block) + schema validation on publish.
- [ ] Idempotent-consumer helper (dedupe on `event_id` via Redis) + DLQ routing for
      schema-invalid messages.

**Acceptance:** a test publishes one event of each family and a test consumer receives,
validates, and acks it; an intentionally-malformed event lands in `mxs.dlq.*`.

### WS3 — DNS Intelligence validator (`dnsd`)

*Depends on: WS1, WS2.*

- [ ] `internal/dns` resolvers (`miekg/dns`) for MX, A/AAAA, PTR, TXT, SPF, DKIM, DMARC,
      DNSSEC, MTA-STS, TLS-RPT, BIMI.
- [ ] Validators with the advanced detections from the architecture: SPF lookup-limit /
      recursive-include / permerror; DKIM stale-selector / weak-key / mismatch; DMARC
      policy-drift / missing rua-ruf / alignment.
- [ ] Snapshot writer: parse full state → JSONB + checksum → write `dns_snapshots` only on
      change → write `dns_findings`.
- [ ] Scheduler: poll each monitored domain on an interval (jittered); on change or
      threshold-crossing finding, publish `dns.changed` / `dns.validation_failed`.
- [ ] On-demand re-check API hook (triggered from dashboard).

**Acceptance:** for a fixture domain with a known-bad SPF (>10 lookups) and a stale DKIM
selector, `dnsd` produces the expected findings, stores a snapshot, and emits a
`dns.validation_failed` event. Modifying the fixture DNS produces a second snapshot and a
`dns.changed` event.

### WS4 — Relay telemetry collector (`telemetryd`)

*Depends on: WS2. The operational core.*

- [ ] Decide collection mechanism and document it: **Postfix maillog parsing** (start,
      lowest-friction) with a milter/policy-daemon path noted for richer real-time data.
- [ ] `internal/telemetry` — parse Postfix/Rspamd log lines into the SMTP event model;
      classify outcome (delivered/deferred/bounced/rejected/received), bounce class, and
      provider (from recipient MX / response).
- [ ] **Privacy at the parser boundary:** extract metadata only; never read/emit bodies;
      hash recipient address, keep recipient domain; truncate response text.
- [ ] Extract TLS metadata, auth results (spf/dkim/dmarc), timing (queue/delivery latency),
      sizes; assemble envelope + correlation block; publish `smtp.*`.
- [ ] Local disk spool + replay so mail flow never blocks on the bus (backpressure).

**Acceptance:** replaying a captured maillog fixture produces the correct sequence of
`smtp.*` events with accurate classification and zero body content; with the bus down,
events spool and replay on recovery.

### WS5 — DMARC aggregate-report ingestion (`dmarcd`)

*Depends on: WS1, WS2.*

- [ ] Fetch reports from a configured mailbox (IMAP) and/or an S3 drop bucket; handle
      gzip/zip attachments.
- [ ] Archive raw XML to object storage (`dmarc-raw/...`) with a Postgres pointer row.
- [ ] `internal/dmarc` — parse aggregate report XML → normalized rows; store summary in
      ClickHouse (or a dedicated CH table) keyed by domain/source-IP/disposition.
- [ ] Surface SPF/DKIM alignment pass/fail rates per source for the dashboard.

**Acceptance:** ingesting a set of real-world DMARC aggregate XML fixtures (including
malformed ones) archives raws, stores parsed rows, and reports per-source alignment;
malformed reports are quarantined, not crashing the daemon.

### WS6 — API service (`apid`)

*Depends on: WS1; consumes outputs of WS3–WS5.*

- [ ] `internal/api` (chi) with tenancy middleware: resolve tenant from API credential /
      session before any query; RBAC scaffold.
- [ ] Endpoints (REST):
  - `GET /v1/domains` + `GET /v1/domains/{id}/health` — SPF/DKIM/DMARC status + latest
    findings + reputation placeholder.
  - `GET /v1/domains/{id}/dns/snapshots` — DNS history (the drift timeline).
  - `POST /v1/domains/{id}/dns/recheck` — trigger `dnsd`.
  - `GET /v1/messages` — message explorer: filter by domain/sender/message-id/rejection
    reason/time window (queries ClickHouse).
  - `GET /v1/dmarc/reports` — list ingested reports + alignment summary.
  - `GET /healthz`, `GET /metrics`.

**Acceptance:** with seeded data, each endpoint returns correct, tenant-isolated results;
a request with another tenant's token cannot read this tenant's data.

### WS7 — Dashboard MVP

*Depends on: WS6. Frontend stack chosen at workstream start (see `tech-stack.md`).*

- [ ] Decide frontend stack (likely TS SPA) and scaffold.
- [ ] Screens: **Domain Health** (status badges + findings), **DNS Drift Timeline**
      (snapshot history), **Message Explorer** (search + result table), **DMARC Reports**
      (list + alignment). Keep it deliberately minimal — function over polish.

**Acceptance:** an operator can complete the Phase-1 "definition of done" walkthrough end
to end in the browser.

---

## Suggested sequencing

```
WS0 ─┬─> WS1 ─┬─────────────> WS6 ──> WS7
     └─> WS2 ─┼─> WS3 (DNS) ──┘
              ├─> WS4 (relay telemetry)
              └─> WS5 (DMARC)
```

WS3, WS4, WS5 are independent and parallelizable once WS1+WS2 land. WS4 is the highest-risk
/ highest-value (it's the operational core) — start its mechanism spike early even while
WS1/WS2 finish.

---

## Milestones

| Milestone | Contents | Demonstrable outcome |
| --- | --- | --- |
| **M1 — Substrate** | WS0 + WS1 + WS2 | Services start; schemas migrated; one of each event type round-trips the bus. |
| **M2 — Signals** | WS3 + WS4 | Live DNS findings in Postgres; live SMTP telemetry in ClickHouse from a real maillog. |
| **M3 — Reports** | WS5 | DMARC reports archived + parsed + alignment summarized. |
| **M4 — Visible** | WS6 + WS7 | The Phase-1 definition-of-done walkthrough passes in the dashboard. |

---

## Risks & guardrails

- **Don't build the correlation engine early.** Phase 1 stores correlation *keys* on every
  event/snapshot; the engine that joins them is Phase 2. Resist scope creep.
- **Don't over-engineer orchestration.** Docker Compose only in Phase 1 (architecture §10).
- **Privacy is a release-blocker, not a nice-to-have.** Any code path that could persist a
  message body fails review. Telemetry parser tests must assert no body content escapes.
- **The relay must never depend on MX Sentinel.** Telemetry is emitted out-of-band
  (log tail / spool); if our pipeline is down, mail still flows. This constraint shapes WS4.
- **Frontend choice is deferred, not skipped** — but it must not block backend WS1–WS6.
