# Deliverability Health Score

A composite **0–100 score per domain** (and a per-tenant roll-up) that fuses signals MX
Sentinel already collects into a single, explainable number, a letter grade (A–F), a trend over
time, and a breakdown of which components are dragging the score down. It is designed to feed
naturally into incident and AI explanations.

- Pure scorer + read-only collector: `internal/healthscore`
- Snapshot daemon: `cmd/scored`
- Store: `internal/store/postgres/health_score.go`, table `health_score_snapshots`
- API: `internal/api/handlers_healthscore.go` (`GET /v1/health-score`,
  `GET /v1/domains/{id}/health-score`, `GET /v1/domains/{id}/health-score/history`)
- Dashboard: `web/app/health-score/page.tsx` (nav section **Deliverability**)

## The score

`internal/healthscore.Compute(Inputs, Weights) Result` is a **pure function** — no clocks, no
I/O — so it is exhaustively table-tested (`score_test.go`). Each component maps a raw signal to a
0–100 sub-score; the composite is the **weight-normalized average over the components that are
present**.

### Components, sources, and mapping

| Component | Source (existing store, read-only) | Sub-score mapping |
|---|---|---|
| `dmarc_alignment` | ClickHouse `dmarc_records` via `DMARCAlignmentSummary` | `max(dkim_rate, spf_rate) × 100`. A message passes DMARC if *either* identifier aligns; the aggregate reports the two rates separately, so `max()` is the tightest available lower bound. |
| `bounce_rate` | ClickHouse `smtp_events` via `DeliverabilityByProvider` | `rate = (bounced+rejected)/total`. `≤2%` → 100, degrading linearly to 0 at `20%`. Deferred (transient) is **not** counted as failure. |
| `blocklist` | Postgres `ip_blocklist_status` via `rbl.Store.List` | `100 × (1 − listed_ips/total_ips)`, then **capped at 50 whenever any IP is listed** so a single Spamhaus hit can't be diluted to a pass. |
| `complaint_rate` | Postgres `fbl_complaints` via `fbl.Store.ListReputation` | `100 − 6 × complaints_24h` (≈17 complaints floors it). |
| `postmaster_reputation` | Postgres `domain_reputation` via `fbl.Store.ListReputation` | Grade `HIGH/GOOD→100, MEDIUM→60, LOW→30, BAD→0`. If a spam rate is present, take `min(grade_score, 100×(1 − spam_rate/0.003))` (Gmail flags at 0.3%). |
| `volume_anomaly` | Postgres `volume_anomaly` via `anomaly.Store.TopMovers` | No active trip in the window → 100. Active spike → `100 − (ratio−1)×25` (5× floors it). |

### Default weights

`dmarc 0.25 · bounce 0.20 · blocklist 0.20 · complaints 0.15 · postmaster 0.10 · anomaly 0.10`.

Auth alignment and bounce/rejection ratio dominate (strongest first-order deliverability
signals); anomaly and Postmaster are lighter (noisier / less universally available).

### Grades

`A ≥ 90, B ≥ 80, C ≥ 70, D ≥ 60, else F`. When no component has data the result is
`HasData=false`, `Grade="N/A"`.

## Graceful degradation (missing = neutral, never a penalty)

Any component whose input is missing is **excluded** from the weighted average and the surviving
weights are **renormalized** — a missing signal is neutral, it does not drag the score toward
zero. `Result.Coverage` reports the fraction of total weight that had data (1.0 = every component
present). Specific degradations, all intentional and documented:

- **No ClickHouse** (or empty window): `dmarc_alignment` and `bounce_rate` are absent.
- **Bounce is tenant-level, not per-domain.** No existing ClickHouse method aggregates outcomes
  per sending domain, and this feature adds no ClickHouse schema, so the bounce component is
  computed once per tenant (summed across providers) and **shared across the tenant's domains**.
- **Blocklist is relay-global.** `ip_blocklist_status` is keyed on `(ip, zone)` with no tenant —
  it is the relay watching its own egress IPs — so the same posture applies to every domain.
- **No feedback-loop / Postmaster data:** `complaint_rate` present only if the sending domain
  appears in the reputation index; `postmaster_reputation` present only with a grade or spam rate.
- **Anomaly baseline cold:** a domain with no trip in the window scores 100 for that component
  (behaving normally), which is present-and-neutral, not absent.

## Persistence & trends

`cmd/scored` recomputes every non-paused domain on an interval (default 1h) and **appends** a row
to `health_score_snapshots` (score, grade, coverage, and the full component breakdown as JSONB).
History is therefore append-only; the API reads the latest row per domain for the summary and the
ordered rows for the trend.

```
health_score_snapshots(
  id, tenant_id, domain_id, domain_name,
  score NUMERIC(5,2), grade TEXT, has_data BOOL, coverage NUMERIC(4,3),
  components JSONB,        -- []healthscore.ComponentScore
  computed_at TIMESTAMPTZ)
```

Indexes: `(domain_id, computed_at DESC)` for per-domain trend/latest, `(tenant_id, computed_at
DESC)` for the tenant summary.

## API

All endpoints require `read` scope.

- `GET /v1/health-score` → `{tenant:{score,grade,domains_total,domains_rated}, domains:[{domain_id,
  domain,score,grade,has_data,coverage,pending,computed_at}]}`. `pending=true` means the domain
  has no snapshot yet. The tenant score is the mean of rated domains.
- `GET /v1/domains/{id}/health-score` → one domain's latest snapshot with the full `components`
  breakdown. If the domain has never been snapshotted it is computed **live** (and marked
  `pending`) so the view is never empty.
- `GET /v1/domains/{id}/health-score/history?limit=N` → `{history:[{score,grade,has_data,coverage,
  computed_at}]}` newest first (default 100, max 1000).

## Incident / AI integration

`Result.Explain()` renders a compact, **body-free** summary (aggregate metrics only — it never
touches message content) such as:

> Deliverability health score 62/100 (grade D), computed over 83% of weighted signals. Top drags:
> Blocklist / reputation (-20, 1 of 2 egress IP(s) blocklisted); Bounce & rejection ratio (-9,
> 11.20% bounce+reject over 4,301 messages).

`Result.Drags()` returns the present components sorted by points-lost, for the same purpose. These
are the building blocks for feeding the score into an incident's AI context; wiring into
`incidentd`/`aid` is left to the orchestrator (see `INTEGRATION_health-score.md`).

## Configuration

`cmd/scored` flags: `-interval` (default `1h`), `-window` (default `168h` / 7 days). No new central
config keys are required; it reuses `MXS_POSTGRES_DSN`, `MXS_CLICKHOUSE_*`, `MXS_HTTPADDR`,
`MXS_LOGLEVEL`. Optional central keys are listed in the integration file.
