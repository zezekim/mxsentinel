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

> **Status:** Implemented. Phases 1–3 are built — relay telemetry, DNS intelligence,
> DMARC ingestion, correlation, reputation, incidents, and local-AI diagnostics — running
> as a Go service mesh behind a Next.js dashboard, with a one-command VPS installer. Phase
> 4 work has begun: RBAC, a public REST API, SMTP relay/submission-user management, and
> tenant settings already ship. Stack: [`docs/tech-stack.md`](docs/tech-stack.md).

---

## Repository map

| Path | What it is |
| --- | --- |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Canonical vision & system architecture (the "why" and the big picture). |
| [`docs/tech-stack.md`](docs/tech-stack.md) | Resolved technology decisions, Go monorepo layout, library choices. |
| [`docs/phase-1-plan.md`](docs/phase-1-plan.md) | Detailed Phase 1 (Foundation) plan: workstreams, milestones, acceptance criteria. |
| [`docs/data-model.md`](docs/data-model.md) | Storage architecture: what lives in Postgres vs ClickHouse vs Redis vs object storage, and why. |
| [`docs/event-contracts.md`](docs/event-contracts.md) | Event streaming design: NATS subject hierarchy, the common event envelope, delivery semantics. |
| [`schemas/postgres/`](schemas/postgres/) | PostgreSQL DDL — tenants, domains, users, SMTP submission users, DNS snapshots, alert rules, configuration. |
| [`schemas/clickhouse/`](schemas/clickhouse/) | ClickHouse DDL — SMTP telemetry events and analytics rollups. |
| [`schemas/events/`](schemas/events/) | JSON Schema contracts for every event family published to the bus. |
| [`cmd/`](cmd/) | Service entrypoints: `apid` (REST API), `dnsd`, `telemetryd`, `ingestd` (lands SMTP telemetry in ClickHouse), `dmarcd`, `correld`, `repd`, `incidentd`, `aid`, `abused` (outbound-abuse guard), `rbld` (DNSBL self-monitor + auto-pull), `anomalyd` (send-volume anomalies), `fbld` (feedback-loop + Postmaster reputation), `authwatchd` (SASL compromise detection), and the `mxctl` operator CLI. |
| [`internal/`](internal/) | Implementation packages: `api`, `dns`, `telemetry`, `dmarc`, `correlate`, `reputation`, `ratelimit`, `rbl`, `anomaly`, `fbl`, `authwatch`, `ai`, `auth`, `config`, `store/*`. |
| [`web/`](web/) | Next.js dashboard (domains, messages, top senders, IP health, velocity, reputation, auth security, DMARC, incidents, SMTP users, settings, docs, account). |
| [`deploy/`](deploy/) | Docker Compose stack, Caddy, the [`install.sh`](deploy/install.sh) installer, and the host [`mxctl`](deploy/mxctl) wrapper. |
| [`docs/api-v1.md`](docs/api-v1.md) | REST API reference (auth, scopes, every `/v1` endpoint). |
| [`docs/deploy-vps.md`](docs/deploy-vps.md) · [`docs/deploy-relay.md`](docs/deploy-relay.md) · [`docs/smarthost.md`](docs/smarthost.md) | VPS runbook, Postfix relay setup, and smarthost client configuration. |

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
| **1 — Foundation** *(complete)* | Get data flowing | Relay telemetry, DNS validator, DMARC ingestion, Postgres schema, dashboard MVP. |
| **2 — Intelligence** *(complete)* | Make data mean something | Correlation engine, provider analytics, rejection analysis, reputation tracking. |
| **3 — AI Diagnostics** *(current)* | Explain & recommend | Root-cause analysis, anomaly detection, remediation recommendations. |
| **4 — Enterprise** *(in progress)* | Scale & isolate | RBAC, public REST API, and SMTP relay/submission management ship today; HA relay clusters, multi-region, and tenant federation remain. |

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
make run-apid      # REST API on :8080 (domain health, messages, DMARC, users, SMTP users, settings)
make apikey        # mint an API token for the demo tenant (printed once)
make web-dev       # Next.js dashboard dev server (web/) -> http://localhost:3000
make test          # unit tests (no services needed)
make restart       # restart the running dev stack
# or: make bootstrap   # up + migrate + bus-ensure in one shot
```

**Phase 2 (Intelligence) has begun:** `cmd/correld` is the correlation engine — it watches
SMTP telemetry for per-provider rejection spikes, classifies the dominant reason, and
correlates each spike against recent DNS changes to produce a root-cause hypothesis
(*"DKIM selector removed at 10:44 → Outlook auth rejections at 10:45"*), publishing a
`reputation.rate_anomaly` event. The pure logic lives in `internal/correlate`; the API
exposes `GET /v1/analytics/{deliverability,rejections}`. `cmd/repd` checks sending IPs
against DNSBLs (`internal/reputation`) and emits `reputation.blacklist_hit`. `cmd/incidentd`
turns those signals into queryable **incidents** (`GET /v1/incidents`).

**Phase 3 (AI Diagnostics) has begun:** `cmd/aid` reads incidents that need analysis,
asks a local OpenAI-compatible LLM (Ollama/vLLM — **metadata only, never message
bodies**) for a root-cause narrative + structured remediation, writes those back onto the
incident (surfaced in the `ai_*` fields of `GET /v1/incidents`), and publishes an `ai.rca`
event. The prompt-building and response-parsing logic lives in `internal/ai`; the LLM
endpoint/model are configured via `MXS_AI_*`.

The REST API (`cmd/apid`, see [`docs/api-v1.md`](docs/api-v1.md)) makes the collected
data queryable: domain health, DNS drift timeline, a message explorer over ClickHouse, a
ranked **Top Senders** view (volume / detected spam / rejections, broken down by IP,
sender, and sending domain), and DMARC reports with alignment — all tenant-scoped via
Bearer tokens. The **Next.js dashboard** in [`web/`](web/) renders those screens, plus the
outbound-security pages (below), **SMTP Users** (manage the relay's submission
credentials), **Settings** (SPF include endpoint, DKIM selector, DMARC defaults, relay
host, and the DNS resolver used for validation), an in-app **Docs** runbook, and
**Account** (self-service password change). Point it at the API with
`NEXT_PUBLIC_API_TOKEN` (from `make apikey`), or just log in.

**Outbound relay management.** Beyond observing mail, the optional on-box Postfix relay is
managed from the same control plane. SMTP submission users (the SASL credentials a
smarthost authenticates with) are created in the dashboard or via `mxctl smtp-user` and
authenticated by the relay through Dovecot's Postgres passdb — no flat password files.
Outbound abuse is filtered on the way out: **rspamd** scores spam and rate-limits both each
authenticated user and each sending domain, and **ClamAV** rejects malware — so one
compromised account can't blast the shared IP pool onto a blocklist. And `cmd/abused`
watches per-user telemetry and **auto-suspends** an account whose recipients are rejecting
its mail as spam/blocklisted (disabling its login + opening an incident). The DNS resolver,
SPF-include endpoint, and DMARC/DKIM defaults are tenant settings that feed both validation
and the generated setup guidance. See [`docs/smarthost.md`](docs/smarthost.md) for pointing
cPanel/Exim/Postfix/apps at the relay, and [`docs/deploy-relay.md`](docs/deploy-relay.md)
§9.8 for the spam/malware filters.

**Outbound security suite.** Four daemons defend the shared IP pool against its worst
case — one compromised account spamming everyone onto a blocklist — and each has a
dashboard page plus a `/v1` endpoint:

- **`cmd/rbld`** (IP Health) monitors the relay's *own* egress IPs against DNSBLs; on a
  listing it opens an incident and writes a healthy-IP set that a host hook uses to
  **auto-pull** the bad IP from the Postfix rotation — failing open, so a total-listing
  scare never halts all mail.
- **`cmd/anomalyd`** (Velocity) learns each sending domain's hourly volume baseline and
  opens an incident on a spike — the *relative* signal complementing rspamd's *absolute*
  rate caps.
- **`cmd/fbld`** (Reputation) ingests ARF feedback-loop complaints from an `abuse@` drop
  directory and pulls Gmail Postmaster reputation + spam rate.
- **`cmd/authwatchd`** (Auth Security) flags per-credential compromise behavior
  (recipient-blasting, bounce/volume spikes), with opt-in auto-lock.

See [`docs/deploy-relay.md`](docs/deploy-relay.md) §12 for the full runbook (env vars, the
RBL rotation hook, and the shared-relay caveats).

Three signal producers are now implemented:

- **`cmd/dnsd`** — polls monitored domains, validates SPF/DKIM/DMARC/MX, writes a new
  `dns_snapshots` row only when the posture changes, and publishes `dns.changed` /
  `dns.validation_failed`. Validation logic lives in `internal/dns`.
- **`cmd/telemetryd`** — parses Postfix maillogs into `smtp.*` events (metadata only —
  recipients hashed, no bodies), publishes them, and spools to disk if the bus is down so
  mail-flow telemetry is never lost. Parser lives in `internal/telemetry`. **`cmd/ingestd`**
  consumes those events off the bus and batch-writes them to ClickHouse (`smtp_events`) —
  the read path behind the Message Explorer and per-account history.
- **`cmd/dmarcd`** — ingests DMARC aggregate reports (xml/.gz/.zip) from a drop directory:
  archives the raw report to object storage, parses it (`internal/dmarc`), writes a
  pointer row (Postgres) + per-source alignment records (ClickHouse), and quarantines
  malformed reports instead of crashing.

Config comes from `deploy/config/mxsentinel.example.yaml`, overridable by `MXS_*` env
vars. The operator CLI is `cmd/mxctl` (`go run ./cmd/mxctl --help`). Code layout is
described in [`docs/tech-stack.md`](docs/tech-stack.md).

**Run the whole platform in containers.** Each service has a multi-stage build (the Go
binaries land in a distroless static image; the dashboard ships as a Next.js standalone
bundle). `make up` starts just the backing services for host development; `make up-app`
builds and runs everything — backing services, the Go daemons, and the dashboard — under
the compose `app` profile (a one-shot `migrate` service applies migrations first).
`telemetryd` is left out of the default profile since it tails a host maillog (see the
note in `deploy/docker-compose.yml`).

**Production / VPS.** The quickest path is the interactive installer — on the VPS, from a
clone of the repo:

```bash
bash deploy/install.sh   # or: make install
```

It prompts for your domain, AI model, relay option, and tenant/owner, auto-generates
strong secrets into `deploy/.env`, brings up the full stack behind **Caddy** (automatic
TLS, single domain), and bootstraps your tenant + owner login. Under the hood it uses the
prod overlay (`deploy/docker-compose.prod.yml`); all backing services stay on loopback and
Caddy is the only public service. For the manual path and operations, see the full runbook
[`docs/deploy-vps.md`](docs/deploy-vps.md) (and [`docs/deploy-relay.md`](docs/deploy-relay.md)
for running Postfix on the same box). To point a sending system (cPanel/Exim/Postfix/app)
at the relay — creating SMTP submission users and the DNS to publish — see
[`docs/smarthost.md`](docs/smarthost.md).

The installer puts a host **`mxctl`** on your PATH that wraps the deployed stack:
`mxctl restart` / `mxctl logs -f` / `mxctl ps` act on the Compose stack, while everything
else runs in the apid container — e.g. `mxctl user set-password --tenant <slug> --email
<you> --password <pw>` (reset a locked-out owner) or `mxctl smtp-user create …`.

---

## Getting oriented (for a new contributor or session)

1. Read [`ARCHITECTURE.md`](ARCHITECTURE.md) for the system shape.
2. Read [`docs/tech-stack.md`](docs/tech-stack.md) for *how* we build it.
3. Pick a workstream from [`docs/phase-1-plan.md`](docs/phase-1-plan.md).
4. The schemas in [`schemas/`](schemas/) are the contracts your service must honor.
