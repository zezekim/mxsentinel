# Connect a cPanel / WHM server to MX Sentinel

Relay all outbound mail from your cPanel/WHM server through MX Sentinel — a drop-in
replacement for MailChannels. cPanel keeps DKIM-signing each customer's mail; MX Sentinel
relays it across your IP pool, authenticates it, filters outbound abuse, and gives you
per-message visibility. **This same runbook is available inside the dashboard under
"Docs".**

## Connection settings

| Setting | Value |
|---------|-------|
| Host | your relay hostname (e.g. `relay.example.com`) |
| Port | `587` (submission) |
| Encryption | STARTTLS (required) |
| Auth | AUTH LOGIN / PLAIN |
| Username / Password | the SMTP user you create in step 1 |

## Step 1 — Create one SMTP user

In the dashboard go to **SMTP Users** → add a user (a full address like
`relay@relay.example.com` is conventional) with the suggested strong password. **Your whole
cPanel server authenticates with this single credential** — you do not create one per
customer. (CLI equivalent: `mxctl smtp-user create --tenant <slug> --username … --password …`.)

## Step 2 — Point WHM / Exim at the relay

WHM → **Service Configuration → Exim Configuration Manager → Advanced Editor**. Add a
router, transport, and authenticator (replace host/user/password), then restart Exim. This
replaces the MailChannels config.

```
# --- ROUTERS START ---
send_via_mxsentinel:
  driver = manualroute
  domains = ! +local_domains
  transport = mxsentinel_smtp
  route_list = * relay.example.com::587
  no_more

# --- TRANSPORTS START ---
mxsentinel_smtp:
  driver = smtp
  hosts_require_auth = relay.example.com
  hosts_require_tls = relay.example.com

# --- AUTHS START ---
mxsentinel_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : relay@relay.example.com : YOUR_SMTP_PASSWORD
```

## Step 3 — DKIM: nothing to do

cPanel already DKIM-signs each account's mail and publishes the key in the customer's DNS.
MX Sentinel passes that signature through untouched, so mail stays authenticated for the
customer's own domain (`dmarc=pass` via DKIM).

## Step 4 — Per-domain DNS (SPF + DMARC)

For each sending domain, publish (DKIM already handled by cPanel):

- **SPF** (TXT `@`): add the relay — `v=spf1 include:<your SPF endpoint> ~all` (set the
  endpoint under **Settings**).
- **DMARC** (TXT `_dmarc`): `v=DMARC1; p=none; rua=mailto:dmarc@yourdomain.com` — start at
  `p=none`, tighten to `quarantine`/`reject` later.

To do the SPF change across **every** zone on the server at once, use
[`deploy/scripts/cpanel-spf-relay-swap.sh`](../deploy/scripts/cpanel-spf-relay-swap.sh): it
ensures each SPF record contains the relay include (swapping an old MailChannels include,
adding it before `all` where missing, or `--add-missing` to create SPF for zones that have
none). Dry-run by default; writes via `whmapi1` so cPanel bumps the serial and syncs any DNS
cluster. (DirectAdmin equivalent: `deploy/scripts/da-spf-include.py`.)

To monitor a domain's DNS posture in the dashboard, register it (bulk-import the whole
server's list straight from cPanel):

```bash
cat /etc/trueuserdomains | mxctl domain import --tenant <slug>
```

## Step 5 — Send a test & verify

```bash
swaks --to you@gmail.com --from test@yourdomain.com \
  --server relay.example.com --port 587 --tls \
  --auth LOGIN --auth-user relay@relay.example.com --auth-password 'YOUR_SMTP_PASSWORD'
```

- Watch it in **Message Explorer** (filter by domain or SMTP user).
- Open the received message's headers — confirm `spf=pass`, `dkim=pass`, `dmarc=pass`.

## Step 6 — Warm up & monitor

- **Warm up** new sending IPs: ramp volume gradually over days/weeks — a cold IP that
  suddenly blasts gets throttled or spam-foldered.
- **Domains** flags broken SPF/DKIM/DMARC; **Incidents** surfaces abuse (a customer whose
  mail recipients reject as spam is auto-flagged), and outbound rate limits cap any single
  sender — so one compromised account can't burn the shared IP pool.

---

The relay-side setup (rspamd + ClamAV filtering, per-domain rate limits, random IP
rotation, SASL via Dovecot) is configured on the relay host by `deploy/install.sh` — see
[deploy-relay.md](deploy-relay.md) and [smarthost.md](smarthost.md).
