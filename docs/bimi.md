# BIMI / VMC Readiness

BIMI (Brand Indicators for Message Identification) lets mailbox providers display a brand's
logo next to authenticated mail. It is the visible payoff of reaching DMARC enforcement: a
logo only shows once a domain publishes DMARC at `p=quarantine` or `p=reject`, a valid
SVG Tiny P/S logo, and — for Gmail and Apple Mail — a Verified Mark Certificate (VMC).

MX Sentinel treats BIMI as another DNS-record posture check on monitored domains (mirroring
the DNS Intelligence engine). For each domain it resolves the `default._bimi.<domain>` TXT
record, validates the referenced logo and certificate, cross-checks DMARC enforcement from the
domain's existing DNS snapshot, and produces a **readiness state** plus a **"what's blocking
BIMI" checklist**.

## What it validates

Given `v=BIMI1; l=<logo-svg-url>; a=<vmc-pem-url>`:

1. **DMARC at enforcement** — BIMI requires the domain's DMARC policy to be `p=quarantine` or
   `p=reject`. This is read from the latest DNS snapshot (`dns_snapshots.state->>'dmarc'`), not
   re-resolved.
2. **Logo (`l=`)** — the URL must serve a valid **SVG Tiny P/S** document: `baseProfile="tiny-ps"`,
   `version="1.2"`, a `<title>`, under 32 KB, and free of scripting, external references,
   raster `<image>`, hyperlinks, and animation.
3. **VMC (`a=`, optional)** — when present, the certificate must be fetchable, parseable, and
   not expired. The leaf certificate's `NotAfter` is recorded as `vmc_expiry`.

## Readiness states

| State | Meaning |
|---|---|
| `not_configured` | No `v=BIMI1` record published yet. |
| `blocked` | Record present but a hard prerequisite is unmet (DMARC not enforced, or logo missing/invalid, or VMC unfetchable). |
| `partial` | Logo valid + DMARC enforced, but no VMC. Logo shows in providers that don't require a VMC. |
| `vmc_expired` | VMC present but the certificate has expired — renew it. |
| `ready` | Record + valid logo + DMARC enforcement + valid, unexpired VMC. |

The checklist returns one item per prerequisite with a `code`, `label`, `status`
(`ok`/`warn`/`fail`), and human-readable `detail`.

## Data model

`bimi_snapshots` (Postgres, migration `00024_bimi.sql`): one row per assessment, written by
`bimid` only when the assessment changes (a drift timeline). Columns: `tenant_id`,
`domain_id`, `record`, `logo_url`, `vmc_url`, `vmc_expiry`, `dmarc_enforced`,
`readiness_state`, `checklist_json`, `checked_at`. The API serves the latest row per domain.

No ClickHouse table and no new event family — BIMI state is low-volume Postgres state.

## Daemon: `bimid`

`cmd/bimid` mirrors `cmd/dnsd`: it lists monitored domains, resolves + validates BIMI for each
on a poll interval, and snapshots the result when it changes. It is standalone and does not
depend on the event bus.

Config (environment, `internal/bimi.LoadConfig`):

| Env | Default | Meaning |
|---|---|---|
| `MXS_BIMI_INTERVAL` | `1h` | Poll cadence. |
| `MXS_BIMI_FETCH_TIMEOUT` | `10s` | Per-request timeout for logo/VMC HTTP GET. |

Run it: `go run ./cmd/bimid` (or `-interval 30m` to override).

## API

All endpoints are under `/v1`, authenticated like every other resource.

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/v1/bimi` | read | Readiness summary across all the tenant's domains. |
| GET | `/v1/domains/{id}/bimi` | read | One domain's readiness detail + checklist. |
| POST | `/v1/domains/{id}/bimi/recheck` | write | Perform a live assessment now and snapshot it. |

`GET /v1/bimi` returns `{ "domains": [ { domain_id, domain, readiness_state, dmarc_enforced,
logo_url, vmc_url, vmc_expiry, checked_at } ] }`. Domains never polled appear with
`readiness_state: "unknown"`.

The detail endpoints return `{ domain_id, domain, readiness_state, record, logo_url, vmc_url,
vmc_expiry, dmarc_enforced, checklist: [ { code, label, status, detail } ], checked_at }`.

## Dashboard

`web/app/bimi/page.tsx` (nav: **Security → BIMI**) lists every domain with a readiness badge
and per-record columns; expanding a row shows the blocking checklist and a **Check now** button
that triggers a live recheck.

## Design notes

The BIMI TXT parser, SVG Tiny P/S validation, VMC expiry extraction, and readiness/checklist
computation are pure functions in `internal/bimi` (`ParseRecord`, `DMARCEnforced`,
`ValidateSVG`, `ParseVMCExpiry`, `Assess`), exhaustively unit-tested against fixture strings.
Live DNS and HTTP sit behind the injectable `Resolver` and `Fetcher` interfaces, so `make test`
never touches the network.
