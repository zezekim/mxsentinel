# Bounce classification & suppression-list management

This is MX Sentinel's **Remediate** step. It turns the SMTP bounces/DSNs that already flow
through telemetry into (a) actionable, classified bounce analytics and (b) a per-tenant
**suppression list** that can be synced back to the relay so bad recipients stop being
retried.

Pipeline position: `Collect → Normalize → Correlate → Analyze → Explain → **Remediate**`.

## Parts

1. **Classification** — a PURE, table-driven function that maps
   `(smtp_code, enhanced_code, response_text)` to a `Category`. No I/O, no state,
   exhaustively unit-tested. Reused identically by the daemon, the API feed, and any
   relay-side hook so classification never diverges.
2. **Suppression list** — a per-tenant list of `recipient_hash + reason + category + source
   + expiry`, auto-populated from terminal bounces and manageable via the API, with an
   export suitable for syncing to a Postfix relay.

## Privacy boundary

The classifier never sees message bodies or subject lines — only the remote-MTA response
text (already truncated by `internal/telemetry`), the codes, and the recipient **hash**.
Suppression entries are keyed **only** by the keyed HMAC-SHA256 recipient hash produced at
the telemetry parser boundary; **no plaintext address is ever stored or exported.**

## Categories (`internal/bounce`)

| Category | Meaning | Auto-suppress |
|---|---|---|
| `invalid_recipient` | Non-existent / moved mailbox (DSN x.1.1/x.1.2/x.1.6/x.1.10, "user unknown") | Yes, permanent |
| `hard` | Permanent failure, no more specific cause (5xx / DSN 5.x.x) | Yes, permanent |
| `spam_block` | Rejected as spam/bulk by content filter | Yes, 30-day TTL |
| `mailbox_full` | Recipient over quota (DSN x.2.2, "mailbox full") | No (transient) |
| `reputation` | IP/domain reputation or DNSBL listing ("spamhaus", "blocklist") | No (relay-scoped) |
| `block` | Administrative/policy block (DSN x.7.1, "access denied") | No (relay-scoped) |
| `soft` | Transient 4xx / greylisting | No (retryable) |
| `unknown` | No usable signal (e.g. a 2xx or garbled response) | No |

Classification order (first match wins): greylisting → invalid recipient → mailbox full →
reputation → spam → policy block → permanence fallback (5xx=hard, 4xx=soft). Reputation is
checked before spam so DNSBL wording containing the substring "spam" (e.g. "spamhaus") is
not miscategorised as content spam.

Only terminal, **recipient-scoped** failures auto-suppress. Reputation/block failures are
relay/IP problems, not per-recipient, so they never populate the suppression list.

## Data model

**Postgres** (`migrations/postgres/00019_bounce_suppression.sql`):

- `suppression_entries(tenant_id, recipient_hash, reason, category, source, created_at,
  expires_at, UNIQUE(tenant_id, recipient_hash))` — the list. `expires_at IS NULL` ⇒
  permanent. `source` ∈ `bounce | complaint | manual | import`.
- `bounce_rollup(tenant_id, day, from_domain, category, count, UNIQUE(tenant_id, day,
  from_domain, category))` — per-day classified counts, written by the daemon (the Go
  category isn't expressible in SQL, so it recomputes authoritatively over a lookback
  window each pass).

**ClickHouse** (`migrations/clickhouse/00005_bounce_events.sql`):

- `bounce_daily` (SummingMergeTree) + `bounce_daily_mv` — per-day, per-(tenant, domain)
  delivery/bounce counts maintained automatically at ingest, used for cheap per-domain
  bounce-**rate** trends. It stays at the "rate" granularity SQL can maintain correctly; the
  fine-grained Go classifier remains authoritative for categories.

## Daemon (`cmd/bounced`)

On an interval (default 5m) it:

1. Reads recent bounced/rejected rows (all tenants) from ClickHouse over a lookback window
   (default 48h).
2. Classifies each row with `bounce.Classify`.
3. Recomputes `bounce_rollup` per (tenant, day, from_domain, category) — idempotent.
4. Upserts `suppression_entries` for suppressable categories (keyed by recipient hash;
   invalid-recipient/hard outrank spam_block when a recipient hits multiple).

It writes no ClickHouse rows (the MV maintains rates) and publishes no events.

Config env vars: `MXS_BOUNCE_INTERVAL` (5m), `MXS_BOUNCE_LOOKBACK` (48h),
`MXS_BOUNCE_MAXROWS` (100000). The API's email→hash helper uses `MXS_RECIPIENT_HASH_KEY`
(must match `telemetryd`'s recipient hash key).

## API (`internal/api/handlers_bounce.go`)

| Method & path | Scope | Purpose |
|---|---|---|
| `GET /v1/bounces?window=1h\|24h\|7d\|30d` | read | classified feed + per-domain rates + category totals |
| `GET /v1/suppression?include_expired=` | read | list suppression entries |
| `POST /v1/suppression` | write | add/refresh (`recipient_hash` **or** `email`, `reason?`, `category?`, `source?`, `ttl_hours?`) |
| `DELETE /v1/suppression/{hash}` | write | remove by recipient hash |
| `GET /v1/suppression/export?format=plain\|postfix` | read | relay-syncable export (`text/plain`) |

`POST /v1/suppression` accepts either a raw `recipient_hash` or an `email` that is hashed
server-side with the same keyed HMAC the relay/telemetry uses, so a manually-suppressed
address matches what the relay computes.

## Relay sync

Two export formats:

- **plain** — one recipient hash per line. The canonical set for a relay membership check.
- **postfix** — a Postfix `access(5)`-style map: `<hash> REJECT MX Sentinel suppressed:
  <reason>` per line, for `check_recipient_access` once the relay maps the recipient to its
  hash.

`cmd/suppressionsync` is a small standalone hook (cron/systemd on the relay) that pulls the
export and writes it atomically:

```
suppressionsync -api https://sentinel.squidix.net -token "$MXS_API_TOKEN" \
  -format postfix -out /etc/postfix/mxs_suppression -postmap
```

The relay must compute the recipient hash with the **same** `MXS_RECIPIENT_HASH_KEY` that
`telemetryd` uses; otherwise exported hashes won't match incoming recipients.

## Web

`/suppression` (nav section **Deliverability**) — window-scoped category summary, per-domain
bounce-rate table, recent classified feed, and suppression-list management (add by
email/hash, remove, export).
