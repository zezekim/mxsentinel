# MX Sentinel

**AI-powered email infrastructure observability and deliverability intelligence.**

MX Sentinel correlates SMTP relay telemetry, DNS state, and provider responses into
unified operational traces, then uses local AI inference to explain failures and
recommend remediation. It is an *operational email intelligence platform* — closer in
spirit to Datadog/Grafana/OpenTelemetry than to a DMARC reporting tool.

The system follows one pipeline end to end:

```
Collect → Normalize → Correlate → Analyze → Explain → Remediate
```

> **Status:** Foundation phase. This repository currently contains **design docs,
> data schemas, and event contracts only** — no application code yet. The chosen
> implementation language for application services is **Go**. See
> [`docs/tech-stack.md`](docs/tech-stack.md).

---

## Repository map

| Path | What it is |
| --- | --- |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Canonical vision & system architecture (the "why" and the big picture). |
| [`docs/tech-stack.md`](docs/tech-stack.md) | Resolved technology decisions, Go monorepo layout, library choices. |
| [`docs/phase-1-plan.md`](docs/phase-1-plan.md) | Detailed Phase 1 (Foundation) plan: workstreams, milestones, acceptance criteria. |
| [`docs/data-model.md`](docs/data-model.md) | Storage architecture: what lives in Postgres vs ClickHouse vs Redis vs object storage, and why. |
| [`docs/event-contracts.md`](docs/event-contracts.md) | Event streaming design: NATS subject hierarchy, the common event envelope, delivery semantics. |
| [`schemas/postgres/`](schemas/postgres/) | PostgreSQL DDL — tenants, domains, users, DNS snapshots, alert rules, configuration. |
| [`schemas/clickhouse/`](schemas/clickhouse/) | ClickHouse DDL — SMTP telemetry events and analytics rollups. |
| [`schemas/events/`](schemas/events/) | JSON Schema contracts for every event family published to the bus. |

---

## Design principles (load-bearing)

1. **Operational first.** This is infrastructure software, not marketing SaaS.
2. **Observability over reports.** Raw, queryable telemetry beats static PDFs.
3. **Correlation is the moat.** Value comes from normalizing and joining SMTP + DNS +
   reputation data — not from charts.
4. **AI assists operators.** It augments mail admins; it does not replace them.
5. **Privacy by construction.** Never store message bodies or attachments. Analyze
   headers, metadata, SMTP telemetry, and auth results only. See the privacy notes in
   [`docs/data-model.md`](docs/data-model.md).

---

## Development phases

| Phase | Theme | Scope |
| --- | --- | --- |
| **1 — Foundation** *(current)* | Get data flowing | Relay telemetry, DNS validator, DMARC ingestion, Postgres schema, dashboard MVP. |
| **2 — Intelligence** | Make data mean something | Correlation engine, provider analytics, rejection analysis, reputation tracking. |
| **3 — AI Diagnostics** | Explain & recommend | Root-cause analysis, anomaly detection, remediation recommendations. |
| **4 — Enterprise** | Scale & isolate | HA relay clusters, RBAC, public APIs, multi-region, tenant federation. |

Phase 1 is fully specified in [`docs/phase-1-plan.md`](docs/phase-1-plan.md).

---

## Local development (Phase 1 substrate)

The Go substrate (event bus, stores, migrations, operator CLI) is in place. Requires Go
1.26+ and Docker.

```bash
make up            # start Postgres, ClickHouse, Redis, NATS (JetStream), MinIO
make migrate       # apply Postgres + ClickHouse migrations
make bus-ensure    # create/update JetStream streams
make selftest      # publish + read back one of each event family
make run-dnsd      # DNS Intelligence validator: snapshot monitored domains, emit events
make replay        # replay a sample Postfix maillog into the bus as smtp.* events
make run-apid      # REST API on :8080 (domain health, messages, DMARC)
make apikey        # mint an API token for the demo tenant (printed once)
make web-dev       # Next.js dashboard dev server (web/) -> http://localhost:3000
make test          # unit tests (no services needed)
# or: make bootstrap   # up + migrate + bus-ensure in one shot
```

The REST API (`cmd/apid`, see [`docs/api-v1.md`](docs/api-v1.md)) makes the collected
data queryable: domain health, DNS drift timeline, a message explorer over ClickHouse,
and DMARC reports with alignment — all tenant-scoped via Bearer tokens. The **Next.js
dashboard** in [`web/`](web/) renders those four screens; point it at the API with
`NEXT_PUBLIC_API_TOKEN` (from `make apikey`).

Two signal producers are now implemented:

- **`cmd/dnsd`** — polls monitored domains, validates SPF/DKIM/DMARC/MX, writes a new
  `dns_snapshots` row only when the posture changes, and publishes `dns.changed` /
  `dns.validation_failed`. Validation logic lives in `internal/dns`.
- **`cmd/telemetryd`** — parses Postfix maillogs into `smtp.*` events (metadata only —
  recipients hashed, no bodies), publishes them, and spools to disk if the bus is down so
  mail-flow telemetry is never lost. Parser lives in `internal/telemetry`.
- **`cmd/dmarcd`** — ingests DMARC aggregate reports (xml/.gz/.zip) from a drop directory:
  archives the raw report to object storage, parses it (`internal/dmarc`), writes a
  pointer row (Postgres) + per-source alignment records (ClickHouse), and quarantines
  malformed reports instead of crashing.

Config comes from `deploy/config/mxsentinel.example.yaml`, overridable by `MXS_*` env
vars. The operator CLI is `cmd/mxctl` (`go run ./cmd/mxctl --help`). Code layout is
described in [`docs/tech-stack.md`](docs/tech-stack.md).

---

## Getting oriented (for a new contributor or session)

1. Read [`ARCHITECTURE.md`](ARCHITECTURE.md) for the system shape.
2. Read [`docs/tech-stack.md`](docs/tech-stack.md) for *how* we build it.
3. Pick a workstream from [`docs/phase-1-plan.md`](docs/phase-1-plan.md).
4. The schemas in [`schemas/`](schemas/) are the contracts your service must honor.
