# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

MX Sentinel is an AI-powered email infrastructure observability platform. It correlates SMTP relay telemetry, DNS state, and provider responses into unified operational traces, then uses local LLM inference to explain failures and recommend remediation. Think Datadog/Grafana for email infrastructure — not a DMARC reporting tool.

Pipeline: **Collect → Normalize → Correlate → Analyze → Explain → Remediate**

## Commands

```bash
# Backend (Go)
make build          # build all binaries into ./bin
make test           # unit tests (no external services needed)
go test ./internal/api/...   # single package test
make vet            # go vet
make lint           # golangci-lint (if installed)
make fmt            # gofmt -w .

# Dev stack (Docker)
make up             # start Postgres, ClickHouse, Redis, NATS, MinIO
make bootstrap      # up + migrate + bus-ensure in one shot
make migrate        # apply all migrations (postgres + clickhouse)
make bus-ensure     # create/update JetStream streams
make selftest       # publish + read back one of each event family
make down           # stop everything
make logs           # tail dev stack logs

# Run individual services
make run-apid       # REST API on :8080
make run-dnsd       # DNS validator (polls every 60s)
make run-correld    # correlation engine
make run-repd       # reputation/DNSBL monitor
make run-aid        # AI diagnostics daemon
make run-incidentd  # incident recorder
make replay         # replay sample maillog into the bus (no DB needed)

# Frontend (Next.js in web/)
make web-dev        # npm install + next dev → http://localhost:3000

# Operator CLI
go run ./cmd/mxctl --help
make apikey         # mint API token for demo tenant
make seed           # seed demo tenant + domain
make user           # create demo owner login

# Production
make install        # interactive VPS installer (run on the server)
make up-app         # full platform in containers (services + dashboard)
```

## Architecture

### Service mesh (Go monorepo)

All services are thin `cmd/` entrypoints over shared `internal/` packages. Module: `github.com/zezekim/mxsentinel`.

| Binary | Role |
|---|---|
| `apid` | REST API server (`internal/api`) — all `/v1` endpoints |
| `telemetryd` | Parses Postfix maillogs → publishes `smtp.*` events to NATS; spools to disk if bus is down |
| `ingestd` | Consumes `smtp.*` events from NATS → batch-writes to ClickHouse |
| `dnsd` | Polls monitored domains, validates SPF/DKIM/DMARC/MX, emits `dns.changed` / `dns.validation_failed` |
| `dmarcd` | Ingests DMARC aggregate XML from a drop dir → object storage + Postgres + ClickHouse |
| `correld` | Watches SMTP telemetry for rejection spikes, correlates against DNS changes → root-cause hypothesis |
| `repd` | Checks sending IPs against DNSBLs → `reputation.blacklist_hit` events |
| `incidentd` | Turns reputation/DNS signals into queryable incidents |
| `aid` | Reads pending incidents → local LLM → writes `ai_summary` / `ai_remediation` back to incident |
| `abused` | Watches per-user telemetry; auto-suspends accounts whose mail is being rejected as spam |
| `rbld` | Monitors relay's own egress IPs against DNSBLs; triggers Postfix hook to pull bad IPs |
| `anomalyd` | Learns per-domain hourly volume baseline; opens incident on spike |
| `fbld` | Ingests ARF feedback-loop complaints + pulls Gmail Postmaster reputation |
| `authwatchd` | Flags per-credential compromise behavior (recipient-blasting, bounce spikes) |
| `scored` | Snapshots a composite 0–100 deliverability health score per domain (fuses auth/bounce/blocklist/complaint/reputation/anomaly signals) |
| `tlsrptd` | Monitors MTA-STS policy + MX TLS certs and ingests TLS-RPT (RFC 8460) reports from a drop dir |
| `bounced` | Classifies SMTP bounces (RFC 3463) and auto-builds per-tenant suppression lists for relay sync |
| `sndsd` | Polls Microsoft SNDS per-IP data + ingests JMRP ARF complaints (Outlook/Hotmail deliverability) |
| `seedd` | Runs inbox-placement seed tests (send tagged probes → IMAP-collect inbox/spam/missing per provider) |
| `probed` | Synthetic SMTP probing of relay endpoints: connect/latency, EHLO, STARTTLS cert expiry, AUTH |
| `bimid` | Snapshots per-domain BIMI/VMC readiness (cross-checks DMARC enforcement, validates SVG/VMC) |
| `notifyd` | Dispatches firing alerts/incidents to per-tenant channels (Slack, webhook, PagerDuty, email) with throttle/dedup |
| `relayfailoverd` | Circuit breaker: when direct delivery to a provider (e.g. Outlook) is sustainedly deferred with **transient 4xx** codes, reroutes that provider's mail to a fallback smarthost (e.g. mail.baby) via a host-side Postfix transport-overlay hook, then auto-reverts. Never fails over 5xx spam/reputation blocks. |
| `mxctl` | Operator CLI: migrations, seeds, API tokens, user management, bus tools |

### Storage

- **PostgreSQL** — tenants, domains, users, SMTP submission users, DNS snapshots, alert rules, config
- **ClickHouse** — `smtp_events` table (high-ingest SMTP telemetry), analytics rollups, DMARC alignment records
- **Redis** — caching, rate limiting (rspamd integration), session state
- **NATS JetStream** — event bus; streams: `SMTP`, `DNS`, `REPUTATION`, `AI`
- **MinIO/S3** — raw DMARC XML, compressed logs

### Event bus contract

Every message is a JSON envelope (`pkg/contracts`) with `event_id` (UUIDv7), `tenant_id`, `occurred_at`, `correlation` block (message_id, source_ip, relay_ip, dkim_selector, etc.), and a type-specific `payload`. Subject format: `mxs.<family>.<tenant_id>.<event>`. Consumers must be idempotent (dedupe on `event_id`). See `docs/event-contracts.md` and `schemas/events/` for schemas.

### Configuration

Config loads from YAML (`MXS_CONFIG` env, defaults to `deploy/config/mxsentinel.example.yaml`) overlaid by `MXS_*` env vars. `internal/config` is the single source of truth. Dev defaults in `config.Defaults()` match `deploy/docker-compose.yml`. Key env vars: `MXS_POSTGRES_DSN`, `MXS_CLICKHOUSE_ADDR`, `MXS_NATS_URL`, `MXS_AI_ENDPOINT`, `MXS_AI_MODEL`.

### Frontend (web/)

Next.js App Router (TypeScript). Points at `apid` via `NEXT_PUBLIC_API_TOKEN` (from `make apikey`) or login. Pages map to API resources: domains, messages, senders, ip-health, velocity, reputation, auth-security, dmarc, incidents, smtp-users, settings, docs, account.

### AI layer

`cmd/aid` + `internal/ai`: calls a local OpenAI-compatible HTTP endpoint (Ollama/vLLM). Input is incident metadata only — **never message bodies or subject lines** (privacy boundary enforced at the telemetry parser). Configure via `MXS_AI_ENDPOINT` and `MXS_AI_MODEL`.

## Key conventions

- **Schemas are the contract.** Go types in `pkg/contracts` are generated from `schemas/events/*.json`. DB access is generated from `schemas/postgres`. Don't hand-write structs that duplicate schema definitions.
- **Privacy at the parser boundary.** `internal/telemetry` extracts metadata and drops bodies before anything is published. Recipient addresses are hashed. Never store or log message bodies. **One deliberate exception:** the per-message report (`message_content` table, fed by `deploy/rspamd/mxs_trace.lua`) stores subject + raw headers for operator triage — isolated in its own table with a 30-day TTL, admin-scope-gated, and never read by the AI layer. See `docs/message-report.md`.
- **Services are independently deployable.** No service reads another service's database directly — all cross-service communication goes through NATS or the API.
- **Structured logging only** (`log/slog`), JSON in production. Every log line touching a tenant carries `tenant_id`; every event-handling line carries correlation keys.
- **Mail delivery must never depend on MX Sentinel being up.** `telemetryd` spools to disk on bus outage. `rbld` fails open on total-listing so mail isn't halted.
- **Migrations** use `pressly/goose`. Run via `make migrate` or `go run ./cmd/mxctl migrate up`. Postgres migrations in `migrations/postgres/`, ClickHouse in `migrations/clickhouse/`.

## Reference docs

| Doc | Contents |
|---|---|
| `ARCHITECTURE.md` | System vision, pipeline, component descriptions |
| `docs/tech-stack.md` | Resolved library/language decisions, repo layout |
| `docs/event-contracts.md` | NATS subject hierarchy, envelope schema, delivery semantics |
| `docs/data-model.md` | What lives in Postgres vs ClickHouse vs Redis and why |
| `docs/api-v1.md` | Full REST API reference (auth, scopes, every `/v1` endpoint) |
| `docs/deploy-vps.md` | VPS runbook |
| `docs/deploy-relay.md` | Postfix relay setup + outbound security suite (§9.8, §12) |
| `docs/smarthost.md` | Pointing cPanel/Exim/Postfix at the relay |
| `docs/health-score.md` | Deliverability health-score model, weights, API |
| `docs/tls-reporting.md` | MTA-STS validation + TLS-RPT ingest pipeline |
| `docs/bounce-suppression.md` | Bounce classification + suppression-list management |
| `docs/microsoft-snds-jmrp.md` | Microsoft SNDS + JMRP integration |
| `docs/inbox-placement.md` | Seed-list inbox-placement testing |
| `docs/smtp-probing.md` | Synthetic SMTP endpoint/cert probing |
| `docs/nl-analytics.md` | Natural-language analytics ("ask your logs") + privacy design |
| `docs/bimi.md` | BIMI/VMC validation + readiness checklist |
| `docs/message-report.md` | Per-message drill-down (envelope/spam/headers/timeline) + rspamd capture + privacy carve-out |
| `docs/alert-channels.md` | Alert delivery channels (Slack/webhook/PagerDuty/email) |
| `docs/relay-failover.md` | Outbound failover: reroute throttled/blocked provider mail to a fallback smarthost (dashboard-managed; always-route or circuit breaker + host hook) |
| `docs/settings-inventory.md` | Every config knob categorized: already-web / safe-to-move / must-stay-`.env` / must-stay-host, and the mechanism for web-managed settings |
