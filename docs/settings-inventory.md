# Settings inventory — what is (and isn't) web-manageable

This is the authoritative map of every configuration knob in MX Sentinel and where it is
meant to be edited. It backs the "manage settings in the dashboard, not the CLI" effort.

Each knob is one of four kinds:

| Kind | Meaning | Where it's edited |
|---|---|---|
| **WEB-NOW** | Already editable in the dashboard. | Dashboard |
| **WEB-MOVE** | Safe to move to the dashboard (per-tenant/operational, not needed at boot). | Being migrated to Dashboard |
| **CORE-INFRA** | Must stay in `deploy/.env`. Connection strings / crypto keys the process needs **before** the DB is reachable (chicken-and-egg), or deployment-wide secrets. | `deploy/.env` |
| **HOST-RELAY** | Must stay on the relay host. Needs root and touches Postfix/systemd directly; can't be driven from a container without a privileged on-host agent. | Relay host (`install.sh`, Postfix) |

> **Why not literally everything?** Two hard boundaries. (1) The app can't read its own
> Postgres/ClickHouse/NATS credentials *from* Postgres — those must be in `.env`. (2) Mail
> delivery must never depend on MX Sentinel being up (see `CLAUDE.md`); relay provisioning
> and IP rotation are root-level host operations. Everything outside those two boundaries can
> live in the dashboard.

---

## CORE-INFRA — stays in `deploy/.env` (boot-time / deployment secrets)

Connection strings and crypto keys the binaries need at startup, before (or independent of)
any tenant DB row:

`MXS_POSTGRES_DSN`, `MXS_POSTGRES_MAXCONNS`, `MXS_CLICKHOUSE_ADDR`, `MXS_CLICKHOUSE_DATABASE`,
`MXS_CLICKHOUSE_USERNAME`, `MXS_CLICKHOUSE_PASSWORD`, `MXS_NATS_URL`, `MXS_REDIS_ADDR`,
`MXS_REDIS_DB`, `MXS_REDIS_PASSWORD`, `MXS_OBJECTSTORE_*` (endpoint/bucket/region/keys/ssl),
`MXS_ENCRYPTION_KEY`, `MXS_RECIPIENT_HASH_KEY`, `MXS_TELEMETRY_HASHKEY`, `PG_*` (compose),
`MXS_CONFIG`, `MXS_SERVICE`, `MXS_HTTPADDR`, `MXS_SMTP_ADDR`, `MXS_LOGLEVEL`,
`MXS_PUBLIC_BASE_URL`, `MXS_API_BASE`, `MXS_API_TOKEN`,
`MXS_WEBMAIL_BASEURL`, `MXS_WEBMAIL_PLUGINSECRET`, `MXS_WEBMAIL_IMAPHOST`,
`MXS_WEBMAIL_IMAPPORT`, `MXS_WEBMAIL_IMAPTLS`, `MXS_WEBMAIL_TOKENTTL`.

`MXS_ENCRYPTION_KEY` is the master key that seals every other secret stored in the DB — it
can never itself live in the DB. It also gates webmail autologin: without it apid stores no
sealed SMTP password and the feature reports itself unavailable rather than falling back to
plaintext. `MXS_WEBMAIL_PLUGINSECRET` is the only credential guarding `/v1/webmail/redeem`,
which sits outside the tenant auth pipeline — treat it like an API token.

---

## HOST-RELAY — stays on the relay host (root + Postfix/systemd)

Set by `deploy/install.sh` flags and Postfix config; a container can surface **status** for
these (read-only) but cannot safely change them:

- **Relay identity / egress:** `RELAY_FQDN`, `RELAY_NODE_IP`, `RELAY_EGRESS_IPS`,
  `RELAY_HOST`, IP-pool rotation (`--wire-ip-rotation`, `transport_maps = randmap`).
- **RBL self-monitor host hook:** `RBL_ZONES`, `RBL_INTERVAL`, `RBL_LOOKUP_TIMEOUT`,
  `RBL_HEALTHY_IPS_FILE` (drives the host-side `rbl-rotation-hook.sh`).
- **Relay provisioning:** Postfix/Dovecot/rspamd/ClamAV/OpenDKIM setup, firewall, milters,
  submission SASL (`--wire-relay-sasl`, `--wire-relay-spam`), DKIM key generation.
- **Webmail mailboxes:** `WEBMAIL`, `WEBMAIL_IMAP_LISTEN`, `WEBMAIL_DOMAINS`, `VMAIL_UID`,
  `VMAIL_GID`, `VMAIL_HOME` (`--wire-webmail`; Dovecot IMAP + maildir store + optional LMTP
  delivery). See `docs/webmail-autologin.md`.
- **Auto-updater:** `deploy/self-update.sh` + the `mxsentinel-update.*` systemd units.

The dashboard's role here is **observability**, which the platform already provides
(`ip-health`, `reputation`, `smtp-probes`, RBL incidents).

---

## WEB-NOW — already editable in the dashboard

- **Tenant mail settings** (`GET/PUT /v1/settings`): `relay_host`, `relay_port`, `spf_include`,
  `dkim_selector`, `dmarc_policy`, `dmarc_rua`, `dmarc_ruf`, `resolver_address`,
  `resolver_timeout_secs`.
- **SMTP submission users** (`/v1/smtp-users` CRUD).
- **Alert channels** (`/v1/alert-channels` CRUD — Slack/webhook/PagerDuty/email; secrets
  sealed via `internal/crypto`).
- **Alert rules** (`/v1/alert-rules` CRUD).
- **Integrations** (`/v1/integrations/cpanel`, `/v1/integrations/whmcs` — API creds sealed
  at rest and managed in the UI).

---

## WEB-MOVE — moving to the dashboard

### Flagship (this milestone): Outbound fallback smarthost + failover

Replaces hand-edited `/etc/postfix/mxs_failover*` files. Stored in the new
`fallback_smarthost` table (password sealed with `MXS_ENCRYPTION_KEY`), rendered onto the
relay by the existing host-side hook so Postfix stays authoritative and mail-path-independent.

| Was (env / host file) | Now (dashboard) |
|---|---|
| `/etc/postfix/mxs_failover_sasl` (host, port, user, pass) | Smarthost credentials form |
| `MXS_FAILOVER_ENABLED` | Enable toggle |
| `MXS_FAILOVER_DOMAINS` | Recipient-domain list |
| *(new)* routing mode | **Always route** / **Only on 4xx throttling** |
| `MXS_FAILOVER_PROVIDER`, `_TRIP_RATE`, `_WINDOW`, `_HOLD`, `_MIN_ATTEMPTS`, `_MIN_DEFERS`, `_MAX_DOMAINS` | Failover tuning (advanced) |

### Providers & keys (shipped): Settings → "Providers & keys"

Dashboard-managed via `GET/PUT /v1/settings/integrations`; secrets sealed at rest; the owning
daemon reads them at startup (dashboard value wins over env). Restart the daemon to apply.

- **AI backend** (aid): `MXS_AI_ENDPOINT`, `MXS_AI_MODEL`, `MXS_AI_APIKEY`, `MXS_AI_TIMEOUTSECS`.
- **Microsoft SNDS** (sndsd): `MXS_SNDS_KEY`.
- **Google Postmaster** (fbld): `GOOGLE_POSTMASTER_TOKEN`.
- **Notification email sender**: `MXS_SMTP_HOST/PORT/USERNAME/PASSWORD/FROM`.

### Delivery & data tuning (shipped): Settings → "Delivery & data tuning (advanced)"

The non-secret tuning knobs for the notification / data-pull daemons are now dashboard-managed
via `GET/PUT /v1/settings/tuning/delivery` (stored unsealed under the `tuning_delivery` key of
`tenants.settings`, since none of them are secrets). Each daemon overlays these onto its
env-based config at startup with precedence **dashboard (DB) > env > default**; a blank/zero
field means "not set" (keep env/default). Restart the daemon to apply.

- **Alert delivery** (notifyd): `MXS_NOTIFY_POLL_INTERVAL`, `MXS_NOTIFY_THROTTLE`,
  `MXS_NOTIFY_DEDUP`, `MXS_NOTIFY_LOOKBACK`, `MXS_NOTIFY_HTTP_TIMEOUT`, `MXS_NOTIFY_DASHBOARD_URL`.
- **Microsoft SNDS/JMRP** (sndsd): `MXS_SNDS_INTERVAL`, `MXS_JMRP_SCAN_INTERVAL`,
  `MXS_JMRP_COMPLAINT_THRESHOLD`. (`MXS_SNDS_KEY` stays under Providers & keys — it's a secret.)
- **Seed-list testing** (seedd): `MXS_SEEDTEST_INTERVAL`, `MXS_SEEDTEST_COLLECT_WINDOW`.
  (`MXS_SEEDTEST_SMTP_*` / `_IMAP_ACCOUNTS` stay in `.env` — credentials.)
- **DMARC pull** (dmarcpulld): `MXS_DMARCP_INTERVAL`, `MXS_DMARCP_LOOKBACKDAYS`.
  (`MXS_DMARCP_APIKEY` stays in `.env` — secret.)
- **NL analytics** (apid): `MXS_NLQUERY_MAX_TOOLS`.

Two more tuning groups ship alongside the delivery one above, same mechanism and precedence:

- **Abuse & bounce** — `GET/PUT /v1/settings/tuning/abuse` (`tuning_abuse` key), for authwatchd
  (`AUTHWATCH_*`) and bounced (`MXS_BOUNCE_*`). Settings → "Abuse & bounce tuning (advanced)".
- **Monitoring** — `GET/PUT /v1/settings/tuning/monitoring` (`tuning_monitoring` key), for
  tlsrptd/MTA-STS (`MXS_TLSRPT_*`, `MXS_MTASTS_*`), probed (`MXS_PROBE_*`, non-topology), and
  bimid (`MXS_BIMI_*`). Settings → "Monitoring tuning (advanced)".

### Follow-up (remaining — mostly secret creds not yet in the UI)

- **Seed-list SMTP/IMAP creds:** `MXS_SEEDTEST_SMTP_*`, `MXS_SEEDTEST_IMAP_ACCOUNTS` (secret).
- **DMARC pull endpoint/key:** `MXS_DMARCP_BASEURL/APIKEY/TENANTID` (`APIKEY` secret).
- **Probe endpoint topology:** `MXS_PROBE_ENDPOINTS/PORTS/HOST` (deployment layout; stays env).

---

## Mechanism for WEB-MOVE settings

1. **Storage:** a config row/table keyed by tenant; secret fields sealed with
   `internal/crypto` (`MXS_ENCRYPTION_KEY`), same as cPanel/WHMCS/alert-channel creds today.
2. **API:** typed `GET/PUT` under `/v1/settings/*`, `ScopeAdmin`; secrets are **write-only**
   (never returned; GET returns a "configured" boolean + non-secret fields).
3. **Consumption:** the owning daemon reads config from the DB, with precedence **dashboard
   (DB) value > env var > built-in default** — a value set in the UI is authoritative so edits
   take effect; env is the bootstrap/fallback for deploys that don't use the dashboard. (The
   smarthost re-reads every tick; the provider keys apply on daemon restart.)
4. **Relay hand-off (smarthost only):** the daemon renders Postfix fragments into the
   bind-mounted state dir; the host hook applies them and reloads. The container never touches
   host Postfix directly.
