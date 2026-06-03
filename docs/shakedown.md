# MX Sentinel — End-to-End Shakedown Guide

This guide walks you through starting every service, driving real data through the
pipeline, and confirming that the Phase 3 AI layer populates `ai_summary` and
`ai_remediation` on an incident. Two paths are described: **Path A** runs everything in
Docker containers; **Path B** runs the Go daemons on the host with only the backing
services containerised (better for active development).

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Docker (with Compose v2) | `docker compose version` must succeed |
| Go 1.22+ | Required for Path B (host dev) only |
| Node.js 18+ / npm | Required for Path B dashboard (`make web-dev`) only |
| Ollama | Required for Phase 3 AI analysis |

### Set up Ollama (required for AI step)

```bash
# Install from https://ollama.com if needed, then:
ollama pull llama3
ollama serve          # listens on http://localhost:11434 by default
```

**Docker Desktop (macOS / Windows):** the compose `aid` service is already configured to
reach the host Ollama at `http://host.docker.internal:11434/v1`. Nothing extra to do.

**Linux Docker:** the `host.docker.internal` hostname is not automatically available.
Either pass `--add-host host.docker.internal:host-gateway` or override the endpoint:

```bash
export MXS_AI_ENDPOINT="http://172.17.0.1:11434/v1"   # typical Linux Docker bridge IP
```

Set the same var when running `aid` on the host in Path B — the default
(`http://localhost:11434/v1`) already works there.

---

## Path A — Full container stack (`make up-app`)

Use this when you want a self-contained demo with no local Go toolchain needed.

### 1. Start everything

```bash
make up-app
```

This runs:

```
docker compose -f deploy/docker-compose.yml --profile app up --build -d
```

The compose file starts the following services under the `app` profile:

| Service | Port | Role |
|---|---|---|
| postgres | 5432 | Primary relational store |
| clickhouse | 9000 / 8123 | SMTP telemetry + analytics |
| redis | 6379 | Cache / rate-limit |
| nats | 4222 / 8222 | Event bus (JetStream) |
| minio | 9001 (S3 API) / 9091 (console) | Object storage (DMARC reports) |
| **migrate** | — | One-shot: applies Postgres + ClickHouse migrations, then exits |
| **apid** | **8080** | REST API |
| **dnsd** | — | DNS validator / snapshot daemon |
| **correld** | — | Correlation engine |
| **incidentd** | — | Incident recorder |
| **repd** | — | Reputation / DNSBL monitor |
| **aid** | — | AI diagnostics daemon |
| **dmarcd** | — | DMARC report ingestion watcher |
| **dashboard** | **3000** | Next.js UI |

The `migrate` service runs `mxctl migrate up` and is declared with
`restart: "no"` and `condition: service_completed_successfully` — every application
service waits for it before starting. You only need to wait for `docker compose ps` to
show `migrate` as `Exited (0)` before the rest of the stack is live.

### 2. Check that everything is up

```bash
docker compose -f deploy/docker-compose.yml --profile app ps
```

All services (except `migrate`, which exits 0) should show `running` (or `healthy`).

### 3. Mint an API token

```bash
# Inside the apid container (mxctl is baked in):
docker compose -f deploy/docker-compose.yml exec apid \
  /usr/local/bin/mxctl apikey create --tenant demo
```

Copy the printed token — it is shown **once** and only a hash is stored.

> **Dashboard caveat:** the `dashboard` image bakes `NEXT_PUBLIC_API_TOKEN` at build
> time as a Next.js public env var. To use the dashboard with a newly minted token you
> must rebuild with a build arg:
>
> ```bash
> docker compose -f deploy/docker-compose.yml --profile app build \
>   --build-arg NEXT_PUBLIC_API_TOKEN=<your-token> dashboard
> docker compose -f deploy/docker-compose.yml --profile app up -d dashboard
> ```
>
> Alternatively, switch to Path B and use `make web-dev` which picks up the env var at
> runtime.

### 4. Continue to the "Drive a result" section below.

---

## Path B — Host dev (`make up` + individual daemons)

Use this during development. The backing services run in Docker; the Go daemons run
directly on your machine so you can attach a debugger, read structured logs, or hot-reload.

### 1. Start backing services only

```bash
make up
# Equivalent to: docker compose -f deploy/docker-compose.yml up -d
```

This brings up postgres, clickhouse, redis, nats, and minio — no app services yet.

### 2. Apply migrations and seed demo data

```bash
make migrate   # go run ./cmd/mxctl migrate up
make seed      # go run ./cmd/mxctl seed  — creates demo tenant + example.com domain
```

### 3. Mint an API token

```bash
make apikey    # go run ./cmd/mxctl apikey create --tenant demo
```

Copy the printed token. Set it in your environment now:

```bash
export API_TOKEN="mxs_<prefix>_<secret>"   # paste the actual token
```

### 4. Start the daemons — each in a separate terminal

Open five terminals in the project root (all use the default config via `MXS_CONFIG`):

```bash
# Terminal 1
make run-apid       # REST API on :8080

# Terminal 2
make run-dnsd       # DNS validator (polls every 60s; also responds to recheck requests)

# Terminal 3
make run-correld    # Correlation engine

# Terminal 4
make run-incidentd  # Incident recorder

# Terminal 5
make run-aid        # AI diagnostics daemon
```

Optionally also run:

```bash
make run-repd       # Reputation / DNSBL monitor
```

### 5. Start the dashboard (optional)

```bash
NEXT_PUBLIC_API_TOKEN=$API_TOKEN make web-dev
# Opens http://localhost:3000
```

---

## Drive a Result: Triggering the Full Pipeline

The steps below work for both Path A and Path B. All `curl` examples assume:

```bash
export API=http://localhost:8080
export API_TOKEN="mxs_<prefix>_<secret>"   # from make apikey / step above
```

### Step 1 — List domains and grab the domain ID

```bash
curl -s -H "Authorization: Bearer $API_TOKEN" \
  "$API/v1/domains" | jq .
```

Expected shape:

```json
{
  "domains": [
    {
      "id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
      "name": "example.com",
      "status": "monitored",
      "categories": { "spf": "unknown", "dkim": "unknown", "dmarc": "unknown", "mx": "unknown" },
      "overall": "unknown",
      "last_checked_at": null,
      "finding_count": 0
    }
  ]
}
```

Save the domain ID:

```bash
DOMAIN_ID=$(curl -s -H "Authorization: Bearer $API_TOKEN" \
  "$API/v1/domains" | jq -r '.domains[0].id')
echo "Domain ID: $DOMAIN_ID"
```

### Step 2 — Trigger a DNS recheck

```bash
curl -s -X POST \
  -H "Authorization: Bearer $API_TOKEN" \
  "$API/v1/domains/$DOMAIN_ID/dns/recheck" | jq .
```

This call tells `dnsd` to immediately re-inspect the domain, write a new
`dns_snapshots` row, and — if any check fails — publish a `dns.validation_failed` event
onto the NATS bus.

Expected shape (example with findings):

```json
{
  "domain":   { "id": "...", "name": "example.com", "status": "monitored" },
  "snapshot": { "id": "...", "captured_at": "2026-06-03T10:45:00Z", "checksum": "...", "healthy": false },
  "categories": { "spf": "warning", "dkim": "critical", "dmarc": "ok", "mx": "ok" },
  "overall": "critical",
  "findings": [
    { "category": "dkim", "severity": "critical", "code": "DKIM_NO_RECORD",
      "message": "No DKIM TXT record found for selector default", "detail": {} }
  ],
  "changed": true
}
```

### Step 3 — Watch the event pipeline flow

Look for these log lines (within seconds of the recheck):

| Service | Log message to expect |
|---|---|
| `dnsd` | `dns.validation_failed published domain=example.com` |
| `correld` | `rate anomaly detected` or `dns change correlated` |
| `incidentd` | `incident recorded kind=dns_validation_failed` (or similar) |
| `aid` | `analyzing incident id=...` then `incident analyzed id=...` |

In Path B these appear in the respective terminal windows. In Path A:

```bash
docker compose -f deploy/docker-compose.yml --profile app logs -f \
  dnsd correld incidentd aid
```

### Step 4 — Poll for incidents with AI analysis

```bash
curl -s -H "Authorization: Bearer $API_TOKEN" \
  "$API/v1/incidents" | jq .
```

Wait up to ~2 minutes for `aid` to pick up the new incident, call the local LLM, and
write the result back. Once populated the response looks like:

```json
{
  "incidents": [
    {
      "id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
      "source_event_id": "...",
      "kind": "dns_validation_failed",
      "severity": "critical",
      "domain": "example.com",
      "subject": "example.com",
      "title": "DKIM record missing for example.com",
      "detail": {},
      "status": "open",
      "confidence": 0.90,
      "created_at": "2026-06-03T10:45:01Z",
      "resolved_at": null,
      "ai_summary": "The DKIM TXT record for the 'default' selector is absent from DNS. Microsoft 365 and Gmail will reject messages from example.com with a 550 5.7.26 authentication error until the record is restored.",
      "ai_remediation": [
        {
          "action": "restore_dkim_record",
          "summary": "Re-publish the DKIM TXT record for selector 'default' at default._domainkey.example.com",
          "target": "default._domainkey.example.com",
          "priority": "urgent"
        }
      ],
      "ai_model": "llama3",
      "ai_analyzed_at": "2026-06-03T10:45:42Z"
    }
  ]
}
```

When `ai_summary` and `ai_remediation` are non-null the full pipeline — including the
Phase 3 AI layer — is confirmed working.

Use the automated script for a hands-free version of steps 1–4:

```bash
bash scripts/shakedown.sh
```

---

## Bonus: Replay SMTP Telemetry

To populate the message explorer and analytics endpoints with sample data:

```bash
make replay
```

This replays `test/fixtures/maillog.sample` as `smtp.*` events onto the bus (the demo
tenant ID is baked into the fixture; no database write for `telemetryd` itself). Then
query:

```bash
curl -s -H "Authorization: Bearer $API_TOKEN" "$API/v1/messages" | jq .
curl -s -H "Authorization: Bearer $API_TOKEN" "$API/v1/analytics/deliverability" | jq .
curl -s -H "Authorization: Bearer $API_TOKEN" "$API/v1/analytics/rejections" | jq .
```

## Bonus: Ingest a Sample DMARC Report

```bash
make ingest-dmarc
```

Then query:

```bash
curl -s -H "Authorization: Bearer $API_TOKEN" \
  "$API/v1/dmarc/reports?domain=example.com" | jq .
```

---

## Troubleshooting

**No domains returned from `GET /v1/domains`**
- Path B: did you run `make seed`? It creates the demo tenant and `example.com`.
- Path A: confirm the `migrate` service exited 0 (`docker compose ps`). If not, check
  its logs: `docker compose -f deploy/docker-compose.yml logs migrate`.

**`POST /v1/domains/{id}/dns/recheck` returns 404**
- The DOMAIN_ID is wrong or belongs to a different tenant. Re-run the `GET /v1/domains`
  step with the same `$API_TOKEN`.

**No incidents appearing after recheck**
- Check `dnsd` logs for `dns.validation_failed` — if it did not publish, the DNS checks
  all passed (possible for `example.com` which has valid public DNS). In that case the
  pipeline still works but no incident is raised for a healthy domain. Use a domain with
  missing DKIM/SPF records for a more dramatic test.
- Check `correld` and `incidentd` logs for errors connecting to NATS or Postgres.
- Verify NATS JetStream streams exist: in Path B run `make bus-ensure`.

**`ai_summary` / `ai_remediation` remain null after several minutes**
- Check `aid` logs — it logs the LLM endpoint it is trying to reach and any HTTP errors.
- Verify Ollama is running: `curl http://localhost:11434/v1/models` should return a JSON
  list including `llama3`.
- Path A on Linux: `host.docker.internal` may not resolve. Set `MXS_AI_ENDPOINT` to the
  Docker bridge IP (commonly `172.17.0.1`) before running `make up-app`, or pass it as
  an override env var to the `aid` container.
- The model name in the compose file and example config is `llama3`. If you pulled a
  differently tagged model, override: `MXS_AI_MODEL=llama3:8b`.
- `aid` retries harmlessly — once the LLM is reachable it will pick up any queued
  unanalysed incidents automatically.

**Dashboard shows no data / auth errors**
- Path A: the `NEXT_PUBLIC_API_TOKEN` build arg must match the minted token. Rebuild the
  dashboard image if you minted a new token after the initial `make up-app`.
- Path B: ensure `NEXT_PUBLIC_API_TOKEN` is exported in the shell before `make web-dev`.

**Port conflicts**
- `apid` binds `:8080`; `minio` console binds `:9091`. Check for conflicting local
  processes: `lsof -i :8080`.

**Services crash-looping in Path A**
- Check logs: `docker compose -f deploy/docker-compose.yml --profile app logs <service>`.
- Most crashes on first start are due to `migrate` not having completed yet. The
  `depends_on: condition: service_completed_successfully` guard handles this, but if
  Docker restarts `migrate` for any reason the app services will wait.
