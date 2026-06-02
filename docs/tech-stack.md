# Technology Stack & Repository Layout

This document records the *resolved* implementation decisions for MX Sentinel. The
architecture vision lives in [`../ARCHITECTURE.md`](../ARCHITECTURE.md); this is the
"how we build it" companion.

---

## Decision record

| Decision | Choice | Rationale |
| --- | --- | --- |
| Application service language | **Go** | High-throughput telemetry ingestion, first-class concurrency, single-binary deploys, strong DNS/SMTP/observability ecosystem. Good fit for the relay-adjacent hot path and the correlation engine. |
| Mail transport (relay) | **Postfix + Rspamd + OpenDKIM** | Battle-tested, scriptable, log- and milter-friendly for telemetry extraction. PowerMTA / Haraka remain optional alternatives. |
| Event bus | **NATS JetStream** (start) → Kafka (scale) | Operationally simple, durable streams, subject-based routing. Kafka deferred until volume demands it. |
| OLTP / config store | **PostgreSQL** | Relational integrity for tenants, domains, DNS snapshots, alert rules. |
| OLAP / telemetry store | **ClickHouse** | Column-store built for high-ingest, time-series analytics over SMTP events. |
| Cache / rate limiting / sessions | **Redis** | Standard. Shared with relay-side rate limiting where applicable. |
| Object storage | **S3 / MinIO** | Raw DMARC XML, compressed logs, forensic reports. MinIO for local/dev. |
| AI inference | **Local LLM** via Ollama / vLLM (OpenAI-compatible HTTP) | Metadata-only analysis; keeps customer data on-prem. Models: Mistral, Llama 3, Qwen, DeepSeek. |
| Frontend | **TBD** (decide at start of dashboard MVP workstream) | Likely a TypeScript SPA (React/Svelte) talking to the Go REST API. Not blocking for Phase 1 backend work. |
| Secrets | **Vault / KMS + envelope encryption** | Never store SMTP credentials or API tokens in plaintext. |

> **Why Go over Python/Node/Rust here:** Python wins on email/DNS library breadth and AI
> glue, but loses on the high-ingest telemetry path. Node unifies with the frontend but is
> weaker for CPU-heavy correlation. Rust is fastest but costs development velocity and has
> a thinner email-tooling ecosystem. Go is the balanced choice for an infra/observability
> backend. The **AI reasoning layer** may still shell out to Python tooling where a model
> ecosystem demands it — that boundary is an HTTP call, not a language lock-in.

---

## Go module & repository layout

A single Go module monorepo. Services are thin `cmd/` entrypoints over shared `internal/`
packages. Public, reusable contracts (event types, schema-generated structs) live in
`pkg/` so external tooling can import them without pulling internals.

```
mxsentinel/
├── go.mod                       # module: github.com/<org>/mxsentinel
├── go.work                      # (optional) if we later split modules
├── cmd/                         # one main package per deployable binary
│   ├── dnsd/                    # DNS Intelligence validator daemon + scheduler
│   ├── telemetryd/              # SMTP telemetry collector → NATS
│   ├── ingestd/                 # NATS consumer → ClickHouse writer
│   ├── dmarcd/                  # DMARC aggregate-report fetcher + parser
│   ├── apid/                    # REST API server
│   └── mxctl/                   # operator CLI (migrations, backfills, debugging)
├── internal/
│   ├── config/                  # env/file config loading & validation
│   ├── dns/                     # resolvers, SPF/DKIM/DMARC/MX/etc. validators
│   ├── telemetry/               # SMTP event model, Postfix/Rspamd log parsers
│   ├── events/                  # envelope, publish/subscribe helpers, subjects
│   ├── store/
│   │   ├── postgres/            # pgx + sqlc-generated queries
│   │   ├── clickhouse/          # CH client, batch writers
│   │   └── objectstore/         # S3/MinIO client
│   ├── dmarc/                   # XML parsing, normalization
│   ├── tenant/                  # tenancy, RBAC, API credentials
│   ├── api/                     # HTTP handlers, middleware, DTOs
│   └── ai/                      # local-LLM client, prompt builders (Phase 3)
├── pkg/
│   └── contracts/               # generated Go types for event schemas (from schemas/events)
├── schemas/                     # source-of-truth DDL + JSON Schema (see schemas/README mentally)
│   ├── postgres/
│   ├── clickhouse/
│   └── events/
├── migrations/                  # versioned DB migrations (goose/golang-migrate)
│   ├── postgres/
│   └── clickhouse/
├── deploy/
│   ├── docker-compose.yml       # local dev: postgres, clickhouse, redis, nats, minio
│   └── config/                  # per-service example configs
├── docs/
└── test/                        # integration tests, fixtures (sample maillogs, DMARC XML)
```

> The directory tree above is the *target*. Today only `docs/` and `schemas/` exist —
> the rest is created as each Phase 1 workstream begins.

---

## Recommended libraries

| Concern | Library | Notes |
| --- | --- | --- |
| DNS resolution | `github.com/miekg/dns` | Low-level control over record types, EDNS, DNSSEC. |
| HTTP routing | `github.com/go-chi/chi/v5` | Lightweight, stdlib-compatible. |
| Postgres driver | `github.com/jackc/pgx/v5` | Fast, native protocol. |
| Type-safe SQL | `sqlc` | Generate Go from SQL; pairs with the DDL in `schemas/postgres`. |
| Postgres migrations | `pressly/goose` *or* `golang-migrate` | Pick one; goose plays well with `mxctl`. |
| ClickHouse | `github.com/ClickHouse/clickhouse-go/v2` | Native protocol, batch inserts. |
| NATS / JetStream | `github.com/nats-io/nats.go` | Streams, consumers, KV. |
| Redis | `github.com/redis/go-redis/v9` | Cache, rate limiting. |
| Object storage | `github.com/aws/aws-sdk-go-v2` or `minio-go` | S3-compatible. |
| Config | `github.com/knadh/koanf` | Env + file, no global state. |
| Structured logging | stdlib `log/slog` | JSON logs, context-aware. |
| Metrics | `prometheus/client_golang` | Service self-observability. |
| Email parsing | stdlib `net/mail`, `mime` | Header/metadata only — never persist bodies. |
| Testing | stdlib `testing` + `stretchr/testify` | `testcontainers-go` for integration. |
| Lint | `golangci-lint` | Enforced in CI. |

---

## Conventions

- **Config via environment** (12-factor); example values in `deploy/config/`. No secrets
  in the repo.
- **Structured logging only** (`slog`), JSON in production. Every log line that touches a
  tenant carries `tenant_id`; every event-handling line carries the correlation keys.
- **Schemas are the contract.** Go types in `pkg/contracts` are generated from
  `schemas/events/*.json`; DB access is generated from `schemas/postgres`. Hand-written
  structs that drift from the schema are a bug.
- **Every binary is independently deployable** and reads only the backing services it
  needs. No service reaches into another service's database directly — they communicate
  through the event bus or the API.
- **Privacy is enforced at the parser boundary.** Telemetry parsers extract metadata and
  drop bodies before anything is published or stored. See `docs/data-model.md`.
