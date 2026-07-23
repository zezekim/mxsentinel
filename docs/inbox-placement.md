# Inbox-placement / seed-list testing

MX Sentinel's inbox-placement feature is the GlockApps/Litmus-style capability: send a
uniquely-tagged synthetic probe message to a configured list of seed mailboxes across
providers (Gmail, Outlook, Yahoo, ...), then measure **inbox vs. spam vs. missing** placement
per provider and per sending IP for each "seed test run".

Pipeline slot: this rides on the relay's real send path, so placement reflects the production
sending IP and reputation — not a synthetic sandbox.

## Concepts

| Object | Meaning |
|---|---|
| **Seed list** | A per-tenant collection of seed mailboxes, each tagged with its provider. |
| **Seed address** | One seed mailbox (`seed@gmail.com`) with a provider label (`gmail`). |
| **Seed run** | One execution of a list: sends a tagged probe to each seed, then collects results. |
| **Seed result** | One seed's outcome within a run: placement (inbox/spam/missing) + auth verdicts. |

## Privacy boundary

Probe messages are **synthetic test content** that MX Sentinel generates (benign boilerplate,
no personal data), so they are safe to transmit and to read back. The collector only ever
matches **our own probes** — it searches each seed mailbox for the run's unique
`X-MXS-Seed-Tag` header and reads nothing else. No real user mail is ingested, stored, or
logged. Seed-mailbox IMAP credentials live only in `seedd`'s environment, never in the
database.

## How a run works

1. **Create** (`POST /v1/seed-tests`) — the API creates a `seed_runs` row plus one
   `seed_results` row per enabled seed, each with a unique `probe_tag`. The run starts in
   `pending`; nothing is sent synchronously.
2. **Send** (`seedd`) — the daemon builds a probe per pending result (`internal/seedtest`
   `BuildProbe`) with the tag in both the `X-MXS-Seed-Tag` header and a `+tag` plus-address,
   and delivers it through the configured SMTP relay. Each result moves to `sent`. When every
   probe is sent the run moves to `collecting`.
3. **Collect** (`seedd`) — for each `sent` result with configured IMAP credentials, the
   collector polls `INBOX` then the provider's junk folders for the tag, classifies the folder
   (`ClassifyMailbox`), and parses `Authentication-Results` for SPF/DKIM/DMARC verdicts. The
   result is finalized as `inbox`, `spam`, or (after the collection window) `missing`.
4. **Complete** — once all results are terminal, or the collection window elapses, the run
   moves to `completed`. Seeds with no configured IMAP account remain `sent` ("sent, pending")
   — placement is never guessed.

Finalized results are also appended to ClickHouse (`seed_placement_results`) for long-horizon
trend analytics.

## Placement summary

`GET /v1/seed-tests/{id}` returns per-seed results plus an aggregated `summary`:

- Per provider and overall: counts of inbox / spam / missing / pending.
- `inbox_rate`, `spam_rate`, `missing_rate` computed over **resolved** seeds (pending seeds are
  excluded from the denominator so rates aren't diluted mid-run).
- `spf_pass_rate`, `dkim_pass_rate`, `dmarc_pass_rate` computed over seeds with a known verdict.

The aggregation (`seedtest.Summarize`) and header/auth parsing (`seedtest.ParseAuthResults`)
are pure, table-driven-tested functions.

## Data model

Postgres (`migrations/postgres/00021_seed_testing.sql`), all tenant-scoped:

- `seed_lists` — named lists.
- `seed_addresses` — seed mailboxes + provider, `UNIQUE(list_id, address)`.
- `seed_runs` — run state machine (`pending → sending → collecting → completed|failed`),
  `run_tag`, `seed_count`, `sent_count`.
- `seed_results` — per-seed placement + auth flags, `UNIQUE(run_id, address)`.

ClickHouse (`migrations/clickhouse/00007_seed_placement.sql`):

- `seed_placement_results` — one immutable row per finalized result
  (`ReplacingMergeTree(ingested_at)` keyed by result), the read path for placement trends.
  Postgres holds authoritative mutable current-run state; ClickHouse holds append-only history
  (same split as `smtp_events` vs. operational tables).

## API

| Method | Path | Scope | Purpose |
|---|---|---|---|
| GET | `/v1/seed-lists` | read | List seed lists (with address counts). |
| POST | `/v1/seed-lists` | write | Create a list; optional inline `addresses[]`. |
| GET | `/v1/seed-lists/{id}` | read | List detail with addresses. |
| DELETE | `/v1/seed-lists/{id}` | admin | Delete a list (cascades to addresses). |
| GET | `/v1/seed-tests` | read | List runs (newest first). |
| POST | `/v1/seed-tests` | write | Start a run for a `list_id`. |
| GET | `/v1/seed-tests/{id}` | read | Run detail: results + placement summary. |

`POST /v1/seed-lists` body:

```json
{
  "name": "Q3 Panel",
  "description": "optional",
  "addresses": [
    { "address": "seed1@gmail.com", "provider": "gmail" },
    { "address": "seed2@outlook.com" }
  ]
}
```

`provider` is optional per address — inferred from the address domain when omitted.

`POST /v1/seed-tests` body: `{ "list_id": "...", "name": "...", "from_address": "...", "ip_pool": "..." }`.

## Configuration (seedd, `MXS_SEEDTEST_*`)

| Env var | Meaning | Default |
|---|---|---|
| `MXS_SEEDTEST_SMTP_HOST` | Relay submission host (empty = runs stay pending) | — |
| `MXS_SEEDTEST_SMTP_PORT` | Submission port | 587 |
| `MXS_SEEDTEST_SMTP_USERNAME` / `_PASSWORD` | SMTP AUTH (optional) | — |
| `MXS_SEEDTEST_SMTP_FROM` | Default probe From when a run omits `from_address` | — |
| `MXS_SEEDTEST_SMTP_STARTTLS` | Use STARTTLS | true |
| `MXS_SEEDTEST_IMAP_ACCOUNTS` | JSON array of seed IMAP accounts (see below) | `[]` |
| `MXS_SEEDTEST_INTERVAL` | How often seedd advances runs | 2m |
| `MXS_SEEDTEST_COLLECT_WINDOW` | Poll window before a probe is "missing" | 30m |

`MXS_SEEDTEST_IMAP_ACCOUNTS` example:

```json
[
  {"address":"seed1@gmail.com","host":"imap.gmail.com","username":"seed1@gmail.com","password":"app-pw"},
  {"address":"seed2@outlook.com","host":"outlook.office365.com","port":993,"password":"pw","spam_mailbox":"Junk"}
]
```

`port` defaults to 993 (implicit TLS); `username` defaults to `address`; `spam_mailbox`
defaults to a provider-specific junk-folder list.

## Daemon

`cmd/seedd` — scheduled worker. On each tick it advances every non-terminal run (send pending
probes, poll collectors, finalize). SMTP send (`Sender`) and IMAP fetch (`IMAPFetcher`) are
interfaces; the real implementations use `net/smtp` and a minimal IMAPS client, and tests
inject fakes so `make test` touches no network.

Run it with `make run-seedd` (see INTEGRATION note).
