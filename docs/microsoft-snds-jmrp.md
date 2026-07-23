# Microsoft SNDS + JMRP integration

The Outlook/Hotmail half of deliverability observability. This is the Microsoft mirror of the
Gmail/Google subsystem (`internal/fbl` + `cmd/fbld`): same table shapes, same daemon-loop style,
same per-sender attribution, so the two feel like one subsystem.

Two independent data sources feed it, either of which runs alone:

1. **SNDS (Smart Network Data Services)** — per-IP reputation. `cmd/sndsd` polls Microsoft's
   SNDS automated-data endpoint (header-less CSV over HTTPS, keyed by an SNDS access key) for
   per-sending-IP data: RCPT/DATA counts, filter result (GREEN/YELLOW/RED), complaint-rate band,
   spam-trap hits, and sample HELO/MAIL FROM.
2. **JMRP (Junk Mail Reporting Program)** — complaint feedback. `cmd/sndsd` watches a drop
   directory for JMRP ARF complaint emails (the same RFC 5965 ARF format the Google FBL uses,
   parsed by `internal/fbl.ParseARF`) and attributes each complaint to the sending IP + domain.

## Pipeline

```
SNDS CSV  ─poll(6h)─▶  ParseSNDS ─▶ snds_ip_data (Postgres, authoritative)
                                 └▶ snds_ip_data (ClickHouse, long-horizon history)
                                 └▶ incident on RED / trap hits

JMRP ARF  ─drop dir─▶  ParseJMRP ─▶ jmrp_complaints (Postgres summary)
                                 └▶ incident on 24h complaint threshold
```

## Data model

### Postgres (authoritative — migration `00020_snds_jmrp.sql`)

- **`snds_ip_data`** — one row per `(tenant_id, ip, data_date)`. Filter result, complaint band,
  trap hits, RCPT/DATA/recipient counts, sample HELO/MAIL FROM, activity window. Attributed to
  the tenant that owns the egress IP (resolved from `relay_nodes` / `ip_pools`, same inventory
  `repd` scans). Unattributed IPs are skipped, not stored as orphans.
- **`jmrp_complaints`** — per `(tenant_id, sender_domain, sending_ip, complaint_date)` **summary**
  with a running `complaint_count`, upserted per complaint.

### ClickHouse (optional — migration `00006_snds.sql`)

- **`snds_ip_data`** (`ReplacingMergeTree`) — the long-horizon per-IP-per-day copy, retained
  **beyond** Microsoft's ~30-day window for trend charts. SNDS is low-volume (a handful of egress
  IPs × ~30 retained days), so **Postgres is the source of truth**; ClickHouse is never a hard
  dependency. The API reads trends from ClickHouse when it is deployed and falls back to the
  Postgres retention window otherwise. `cmd/sndsd` writes both (best-effort to ClickHouse).

## Privacy boundary

Only metadata is parsed and stored — never message bodies or subject lines. JMRP parsing reads
feedback-report fields and the embedded original message's **headers** only (`From`, `Message-ID`,
`Received`, `Source-IP`).

## API

Both endpoints are tenant-scoped and require `read` scope. Registered via
`registerMicrosoftRoutes(mux)` in `internal/api/handlers_snds.go`.

### `GET /v1/microsoft/snds?limit=&days=`

Per-IP current filter state plus a short per-IP trend (`days`, default 14).

```json
{
  "ips": [
    {
      "ip": "203.0.113.11",
      "data_date": "2026-07-21",
      "filter_result": "RED",
      "complaint_band": "1% - < 2%",
      "trap_hits": 7,
      "rcpt_count": 5000,
      "data_count": 4800,
      "message_recipients": 5000,
      "sample_helo": "relay.client.example",
      "sample_from": "news@shop.example",
      "fetched_at": "2026-07-21T09:00:00Z",
      "trend": [{ "date": "2026-07-20", "filter_result": "YELLOW", "trap_hits": 0, "rcpt_count": 4200 }]
    }
  ]
}
```

### `GET /v1/microsoft/jmrp?limit=`

JMRP complaint feed (per domain/IP/day summary, most recent first).

```json
{
  "complaints": [
    {
      "sender_domain": "client.example",
      "sending_ip": "203.0.113.11",
      "feedback_type": "abuse",
      "provider": "microsoft",
      "complaint_date": "2026-07-21",
      "complaint_count": 12,
      "last_seen": "2026-07-21T08:45:00Z"
    }
  ]
}
```

## Daemon — `cmd/sndsd`

Mirrors `cmd/fbld`. Two tickers: SNDS poll (`MXS_SNDS_INTERVAL`, default 6h) and JMRP drop-dir
scan (`MXS_JMRP_SCAN_INTERVAL`, default 30s). Runs an initial pass on startup. The SNDS half is
skipped cleanly (logged once) when `MXS_SNDS_KEY` is unset — exactly as `fbld` skips Postmaster
without `GOOGLE_POSTMASTER_TOKEN`. Postgres is required; ClickHouse is best-effort.

Incidents opened (idempotent, one per subject per day, `kind=other`, `severity=critical`):
- SNDS filter result **RED** or **trap hits > 0** for a sending IP.
- **JMRP** 24h complaint count ≥ threshold for a sending domain.

## Configuration

All via `MXS_*` env (read by `internal/snds.LoadConfig`):

| Env | Default | Meaning |
|---|---|---|
| `MXS_SNDS_KEY` | (unset → SNDS poll disabled) | SNDS access key (the `key` query param) |
| `MXS_SNDS_URL` | `https://postmaster.live.com/snds/data.aspx` | SNDS automated-data endpoint |
| `MXS_SNDS_INTERVAL` | `6h` | SNDS CSV poll interval |
| `MXS_JMRP_DIR` | `/jmrp-drop` | JMRP ARF complaint drop directory |
| `MXS_JMRP_SCAN_INTERVAL` | `30s` | drop-directory scan interval |
| `MXS_JMRP_COMPLAINT_THRESHOLD` | `10` | 24h per-domain complaint count that trips an incident |

CLI flags `--dir`, `--scan-interval`, `--snds-interval` override the env for the JMRP dir and the
two intervals.

## Enrollment (operator)

1. Register your egress IP ranges at [Microsoft SNDS](https://sendersupport.olc.protection.outlook.com/snds/),
   obtain an automated-data access key, and set `MXS_SNDS_KEY`.
2. Enroll your sending IPs in the [JMRP](https://sendersupport.olc.protection.outlook.com/pm/),
   route the ARF feedback mailbox into `MXS_JMRP_DIR`.
3. Run `sndsd` (`make run-sndsd`). Confirm data under **Infrastructure → Microsoft** in the dashboard.
