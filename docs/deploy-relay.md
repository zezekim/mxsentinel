# Postfix Outbound Relay — Same-VPS Deployment Runbook

Deploy a Postfix + OpenDKIM outbound mail relay on the same VPS that runs MX Sentinel.
MX Sentinel **observes** the relay — it is never in the mail path. The relay sends mail
regardless of whether MX Sentinel is up.

---

## 1. Overview

### How the pieces fit together

```
  cPanel / Exim / App (submission clients)
  ┌──────────────────────────────────────┐
  │  smarthost-1 … smarthost-N           │
  └───────────────────┬──────────────────┘
                      │  SMTP submission  587 / 25  (SASL or trusted network)
                      ▼
  ┌────────────────────────────────────────────────────────┐
  │  THIS VPS  (8 vCPU / 8 GB RAM / 11 public IPs)        │
  │                                                        │
  │  Postfix (host-installed)    OpenDKIM (host-installed) │
  │   ┌──────────────┐            ┌──────────────────┐     │
  │   │ IP pool TXN  │            │ DKIM milter       │     │
  │   │ 203.0.113.11 │◄──signs────│ (all domains)    │     │
  │   │ 203.0.113.12 │            └──────────────────┘     │
  │   │ 203.0.113.13 │                                      │
  │   ├──────────────┤                                      │
  │   │ IP pool MKT  │                                      │
  │   │ 203.0.113.14 │                                      │
  │   │ 203.0.113.15 │                                      │
  │   │ 203.0.113.16 │                                      │
  │   │ 203.0.113.17 │                                      │
  │   ├──────────────┤                                      │
  │   │ IP pool WARM │                                      │
  │   │ 203.0.113.18 │                                      │
  │   │ 203.0.113.19 │                                      │
  │   └──────────────┘                                      │
  │          │  outbound SMTP port 25                       │
  └──────────┼─────────────────────────────────────────────┘
             ▼
  Receiving MTAs — Gmail · Microsoft · Yahoo · Regional

  ─────────── MX Sentinel observability (NOT in mail path) ────────────
  ┌──────────────────────────────────────────────────────────────────┐
  │  telemetryd container (profile: relay)                           │
  │  tails /var/log/mail.log read-only → NATS → ClickHouse           │
  │                                                                  │
  │  repd    — queries DNSBLs for every registered pool IP           │
  │  correld — correlates rejection spikes to DNS/IP changes         │
  │  dnsd    — snapshots SPF / DKIM / DMARC / PTR                    │
  │  aid     — explains incidents (3B model, local Ollama)            │
  └──────────────────────────────────────────────────────────────────┘
```

**Key property:** if any MX Sentinel daemon crashes — or if you stop the whole Docker
Compose stack — Postfix keeps sending. `telemetryd` spools parsed events to disk and
replays them when NATS comes back, so no telemetry is permanently lost either.

---

## 2. IP Plan

The VPS has 11 public IPs. One is already bound as the primary host address. Assign the
remaining 10 to three reputation pools.

| IP | Role | Pool |
|----|------|------|
| 203.0.113.10 | Primary host / management — PTR = relay.example.com | (not a sending IP) |
| 203.0.113.11 | Transactional sending | transactional |
| 203.0.113.12 | Transactional sending | transactional |
| 203.0.113.13 | Transactional sending | transactional |
| 203.0.113.14 | Marketing sending | marketing |
| 203.0.113.15 | Marketing sending | marketing |
| 203.0.113.16 | Marketing sending | marketing |
| 203.0.113.17 | Marketing sending | marketing |
| 203.0.113.18 | Warmup | warmup |
| 203.0.113.19 | Warmup | warmup |
| 203.0.113.20 | Reserved / spare | — |

Replace these with your real IPs throughout the runbook.

**Rules every sending IP must satisfy (no exceptions):**

- **PTR record** — set at the VPS provider's control panel. Must be a fully-qualified
  hostname that forward-resolves back to the same IP (FCrDNS). Example:
  `203.0.113.11` → PTR `txn-1.relay.example.com` → A `203.0.113.11`.
- **SPF** — every IP must be listed in the `SPF` record of the envelope-sender domain.
  Use `ip4:` mechanisms or a single `/28` CIDR if your IPs are contiguous.
  Watch the 10-lookup limit — `dnsd` flags `SPF_LOOKUP_LIMIT` violations.
- **DKIM** — signing is per-domain, not per-IP. All sending IPs share the same key.
- **DMARC** — set to `p=quarantine` (or `p=reject`) once warmup and alignment are
  confirmed.

---

## 3. Network Interface Configuration

Add each IP to the host's primary network interface as secondary addresses. On Ubuntu/Debian:

```bash
# /etc/netplan/50-relay-ips.yaml  (Ubuntu 22.04+)
network:
  version: 2
  ethernets:
    eth0:
      addresses:
        - 203.0.113.10/24   # primary — already configured
        - 203.0.113.11/24
        - 203.0.113.12/24
        - 203.0.113.13/24
        - 203.0.113.14/24
        - 203.0.113.15/24
        - 203.0.113.16/24
        - 203.0.113.17/24
        - 203.0.113.18/24
        - 203.0.113.19/24
        - 203.0.113.20/24
```

```bash
sudo netplan apply
ip addr show eth0   # verify all secondaries appear
```

---

## 4. Postfix Install and Base Configuration

### 4.1 Install

```bash
sudo apt-get update
sudo apt-get install -y postfix postfix-pcre libsasl2-modules
# Debconf prompt: select "No configuration" or "Internet Site" — we overwrite main.cf below
```

### 4.2 `/etc/postfix/main.cf`

Replace `relay.example.com` and IP addresses with your real values.

```ini
# Identity
myhostname = relay.example.com
myorigin = $myhostname
mydestination =                          # deliver nothing locally
local_recipient_maps =
local_transport = error:local delivery disabled

# Listen on all IPs for submission from trusted clients
inet_interfaces = all
inet_protocols = ipv4

# Accept mail only from trusted networks / SASL — never act as open relay
mynetworks = 127.0.0.0/8 [::1]/128
smtpd_relay_restrictions =
    permit_mynetworks,
    permit_sasl_authenticated,
    reject

# Outbound TLS (opportunistic — upgrade to "may" once tested; "dane" when DNSSEC is ready)
smtp_tls_security_level = may
smtp_tls_loglevel = 1
smtp_tls_CAfile = /etc/ssl/certs/ca-certificates.crt

# Inbound TLS for submission port (see master.cf)
smtpd_tls_cert_file = /etc/ssl/certs/relay.example.com.pem
smtpd_tls_key_file  = /etc/ssl/private/relay.example.com.key
smtpd_tls_security_level = may

# Queue and retry
maximal_queue_lifetime = 5d
bounce_queue_lifetime  = 1d
smtp_destination_concurrency_limit = 20
smtp_destination_rate_delay = 0

# Message size (100 MB)
message_size_limit = 104857600

# Logging — telemetryd reads this file
maillog_file = /var/log/mail.log

# DKIM milter (configured in §5)
milter_protocol = 6
milter_default_action = accept
smtpd_milters = inet:127.0.0.1:8891
non_smtpd_milters = inet:127.0.0.1:8891

# Sender-dependent transport for IP pool routing (configured in §4.4)
sender_dependent_default_transport_maps = pcre:/etc/postfix/sender_transport
```

### 4.3 `/etc/postfix/master.cf` — submission port + pool transports

Add these entries. The default `smtp` service stays; each pool transport is a named
instance of the smtp(8) client bound to a specific source IP.

```
# Submission from smart hosts / cPanel (SASL or trusted-network)
submission inet n - y - - smtpd
  -o syslog_name=postfix/submission
  -o smtpd_tls_security_level=encrypt
  -o smtpd_sasl_auth_enable=yes
  -o smtpd_relay_restrictions=permit_sasl_authenticated,permit_mynetworks,reject
  -o smtpd_recipient_restrictions=permit_sasl_authenticated,permit_mynetworks,reject

# Transactional pool transport — pick one IP as the primary bind address for this pool.
# Postfix round-robins across IPs at the system level via ip_source_routing or by adding
# multiple smtp_bind_address lines via transport maps.  The simplest approach: one
# transport per IP within the pool.
smtp-txn1 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/txn1
  -o smtp_bind_address=203.0.113.11

smtp-txn2 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/txn2
  -o smtp_bind_address=203.0.113.12

smtp-txn3 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/txn3
  -o smtp_bind_address=203.0.113.13

# Marketing pool transports
smtp-mkt1 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/mkt1
  -o smtp_bind_address=203.0.113.14

smtp-mkt2 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/mkt2
  -o smtp_bind_address=203.0.113.15

smtp-mkt3 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/mkt3
  -o smtp_bind_address=203.0.113.16

smtp-mkt4 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/mkt4
  -o smtp_bind_address=203.0.113.17

# Warmup pool transports
smtp-warm1 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/warm1
  -o smtp_bind_address=203.0.113.18
  -o smtp_destination_concurrency_limit=5
  -o smtp_destination_rate_delay=1s

smtp-warm2 unix  -  -  n  -  -  smtp
  -o syslog_name=postfix/warm2
  -o smtp_bind_address=203.0.113.19
  -o smtp_destination_concurrency_limit=5
  -o smtp_destination_rate_delay=1s
```

### 4.4 Sender-dependent transport map

This is the routing core: map envelope-sender addresses (or domain patterns) to the
transport that binds the desired pool IP.

```
# /etc/postfix/sender_transport
# Format: pcre — first match wins.

# Warmup senders — explicit list wins first
/^.*@warmup\.example\.com$/   smtp-warm1

# Marketing senders — by subdomain or explicit
/^.*@newsletter\.example\.com$/   smtp-mkt1
/^.*@campaigns\.example\.com$/    smtp-mkt2

# Transactional — everything else in example.com
/^.*@example\.com$/   smtp-txn1

# Second tenant — transactional on txn2, marketing on mkt3
/^.*@acme\.io$/        smtp-txn2
/^.*@deals\.acme\.io$/ smtp-mkt3
```

Multiple transactional IPs (txn1/txn2/txn3) can be used by splitting sender domains
across them, or by adding a round-robin at the application/cPanel level that changes
the envelope sender subdomain per message batch. For simplicity, one transport per pool
is sufficient to start; add more per-IP transports as volume grows.

After editing:

```bash
sudo postmap /etc/postfix/sender_transport   # only needed if using hash: map; pcre: maps are read directly
sudo postfix check
sudo systemctl reload postfix
```

### 4.5 SASL authentication for submission clients

`deploy/install.sh` (relay mode) wires submission auth to **Dovecot backed by Postgres**,
so SMTP submission users are managed in the dashboard (**SMTP Users**) or via
`mxctl smtp-user create` — no flat password files to edit or reload. The installer writes:

- `/etc/dovecot/dovecot.conf` — Dovecot as a SASL provider only (no IMAP/POP), exposing the
  auth socket at `/var/spool/postfix/private/auth`.
- `/etc/dovecot/dovecot-sql.conf.ext` — a `pgsql` passdb querying the `smtp_users` table
  (`default_pass_scheme = BLF-CRYPT`; passwords are bcrypt hashes).
- Postfix `submission/inet` (587) with `smtpd_sasl_type=dovecot`,
  `smtpd_sasl_path=private/auth`, `smtpd_tls_auth_only=yes`, and
  `permit_sasl_authenticated, reject` restrictions.

Create the first credential during install (it offers to), in the dashboard, or with:

```bash
mxctl smtp-user create --tenant demo \
  --username mailer@send.example.com --password 'a-strong-password' --domain send.example.com
```

Client-side configuration (cPanel/Exim/Postfix/app), DNS, and testing are covered in
**[smarthost.md](smarthost.md)**.

If you set this up by hand instead of via the installer, the equivalent password query is:

```
# /etc/dovecot/dovecot-sql.conf.ext
driver = pgsql
connect = host=127.0.0.1 port=5432 dbname=mxsentinel user=mxsentinel password=<from deploy/.env>
default_pass_scheme = BLF-CRYPT
password_query = SELECT username AS "user", password_hash AS password FROM smtp_users WHERE username = '%u' AND enabled = TRUE
```

Alternatively, restrict by trusted IP (`mynetworks`) if your cPanel servers have fixed IPs
and you do not want to manage SASL passwords at all.

---

## 5. DKIM with OpenDKIM

### 5.1 Install

```bash
sudo apt-get install -y opendkim opendkim-tools
```

### 5.2 Key generation

Generate one keypair per signing domain. Use a 2048-bit RSA key. The selector name
should encode the year and month so rotation is straightforward.

```bash
sudo mkdir -p /etc/opendkim/keys/example.com
sudo opendkim-genkey -b 2048 -d example.com -D /etc/opendkim/keys/example.com \
    -s 2026-06 -v
sudo chown -R opendkim:opendkim /etc/opendkim/keys
sudo chmod 600 /etc/opendkim/keys/example.com/2026-06.private
```

Repeat for each additional signing domain (acme.io, etc.).

### 5.3 `/etc/opendkim.conf`

```ini
Syslog              yes
SyslogSuccess       yes
LogWhy              yes

Mode                sv
Canonicalization    relaxed/simple
OversignHeaders     From

# Sign all outbound, verify all inbound
Socket              inet:8891@127.0.0.1
PidFile             /run/opendkim/opendkim.pid
UMask               002
UserID              opendkim

# Per-domain key table and signing table
KeyTable            /etc/opendkim/key.table
SigningTable        refile:/etc/opendkim/signing.table

# Trusted hosts that submit mail through this milter
InternalHosts       /etc/opendkim/trusted.hosts
```

### 5.4 Key table and signing table

```
# /etc/opendkim/key.table
# name   domain        selector   private-key-path
example-2026-06   example.com   2026-06   /etc/opendkim/keys/example.com/2026-06.private
acme-2026-06      acme.io       2026-06   /etc/opendkim/keys/acme.io/2026-06.private
```

```
# /etc/opendkim/signing.table
# Pattern (refile: = regex)        key-name
*@example.com                      example-2026-06
*@newsletter.example.com           example-2026-06
*@campaigns.example.com            example-2026-06
*@acme.io                          acme-2026-06
*@deals.acme.io                    acme-2026-06
```

```
# /etc/opendkim/trusted.hosts
127.0.0.1
localhost
203.0.113.10
203.0.113.11
203.0.113.12
203.0.113.13
203.0.113.14
203.0.113.15
203.0.113.16
203.0.113.17
203.0.113.18
203.0.113.19
203.0.113.20
```

### 5.5 DNS TXT record

Print the public key to copy into DNS:

```bash
cat /etc/opendkim/keys/example.com/2026-06.txt
```

Create a DNS TXT record at `2026-06._domainkey.example.com` with the value printed.
It looks like:

```
2026-06._domainkey.example.com.  IN TXT  "v=DKIM1; k=rsa; p=MIIBIjANBgkq..."
```

Verify publication:

```bash
dig TXT 2026-06._domainkey.example.com +short
```

### 5.6 Start OpenDKIM

```bash
sudo systemctl enable opendkim
sudo systemctl start opendkim
```

Verify Postfix is reaching the milter:

```bash
grep opendkim /var/log/mail.log | tail -20
# Should see: opendkim[...]: ... DKIM-Signature added
```

---

## 6. DNS Per-IP and Per-Domain Records

Every sending IP and every signing domain needs the full set before you send any real
volume. `dnsd` continuously validates and snapshots these.

### 6.1 PTR (reverse DNS)

Set at the VPS provider — not in your domain's zone file.

| IP | PTR value |
|----|-----------|
| 203.0.113.11 | txn-1.relay.example.com |
| 203.0.113.12 | txn-2.relay.example.com |
| 203.0.113.13 | txn-3.relay.example.com |
| 203.0.113.14 | mkt-1.relay.example.com |
| 203.0.113.15 | mkt-2.relay.example.com |
| 203.0.113.16 | mkt-3.relay.example.com |
| 203.0.113.17 | mkt-4.relay.example.com |
| 203.0.113.18 | warm-1.relay.example.com |
| 203.0.113.19 | warm-2.relay.example.com |

Each PTR hostname must also have a matching A record that resolves back to the same IP
(FCrDNS / forward-confirmed reverse DNS). Add A records for each in `relay.example.com`.

### 6.2 SPF

With 10 sending IPs in one /28-ish block, use `ip4:` mechanisms directly to stay well
inside the 10-lookup limit. Do not chain `include:` unless necessary.

```
example.com.  IN TXT  "v=spf1 ip4:203.0.113.11 ip4:203.0.113.12 ip4:203.0.113.13 ip4:203.0.113.14 ip4:203.0.113.15 ip4:203.0.113.16 ip4:203.0.113.17 ip4:203.0.113.18 ip4:203.0.113.19 ~all"
```

Or use a CIDR if the IPs are contiguous:

```
example.com.  IN TXT  "v=spf1 ip4:203.0.113.0/28 ~all"
```

`ip4:` mechanisms do not consume DNS lookups. Keeping the record flat avoids the
`SPF_LOOKUP_LIMIT` incident that `dnsd` will flag.

Repeat for each sending domain (acme.io, newsletter.example.com, etc.).

### 6.3 DKIM

Published in step 5.5. `dnsd` validates it. Make sure the selector name in DNS matches
the selector in `/etc/opendkim/key.table`.

### 6.4 DMARC

Start with `p=none` (monitoring only) until you confirm SPF and DKIM alignment, then
move to `p=quarantine` or `p=reject`.

```
_dmarc.example.com.  IN TXT  "v=DMARC1; p=none; rua=mailto:dmarc-rua@example.com; ruf=mailto:dmarc-ruf@example.com; adkim=r; aspf=r"
```

Point `rua` at whatever inbox or service you use for DMARC aggregate reports. If you
feed them to MX Sentinel's `dmarcd`, use a drop address that pipes into
`deploy/dmarc-drop/`.

---

## 7. Hook Up MX Sentinel Observation

MX Sentinel watches the relay through four channels: telemetryd (maillog), repd
(DNSBL), dnsd (DNS snapshots), and correld (correlation). The relay must already be
running and logging before you do this section.

### 7.1 Confirm Postfix is writing to /var/log/mail.log

```bash
tail -f /var/log/mail.log
# Send a test message (see §8) and confirm new lines appear here.
```

If Postfix is logging elsewhere (e.g. `/var/log/maillog` on some distros), set
`maillog_file = /var/log/mail.log` in `main.cf` (already included in §4.2) and
`sudo systemctl restart postfix`.

### 7.2 Add relay env vars to deploy/.env

Open `deploy/.env` and append:

```bash
# Relay telemetry
RELAY_NODE_IP=203.0.113.10
MAILLOG_PATH=/var/log/mail.log
MXS_TELEMETRY_HASHKEY=<32-byte-hex-secret>   # openssl rand -hex 32
```

`RELAY_NODE_IP` is the value telemetryd stamps onto every event as `relay_ip`. Use
the primary management IP so it is unique and stable. `MXS_TELEMETRY_HASHKEY` is the
HMAC key used to hash recipient addresses — required for privacy; generate a fresh
value with `openssl rand -hex 32`.

### 7.3 Enable the telemetryd container

The `telemetryd` service lives in the compose file under the `relay` profile. Bring it
up alongside the existing `app` stack:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app --profile relay \
  --env-file deploy/.env \
  up -d
```

Verify it started and is tailing the log:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app --profile relay \
  --env-file deploy/.env \
  logs -f telemetryd
# Expect: telemetryd following /maillog ...
```

If the log file path on your host differs from `/var/log/mail.log`, the `MAILLOG_PATH`
variable in `deploy/.env` controls the bind-mount source path. The container always
sees it as `/maillog`.

### 7.4 Register the relay node and IP pools with mxctl

Run each command inside a one-shot `apid` container (mxctl is baked into the image).
Replace `<tenant-slug>` with your tenant identifier (e.g. `demo` for the default seed).

```bash
# Transactional pool
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  run --rm apid \
  /usr/local/bin/mxctl ip-pool create \
    --tenant demo \
    --name "transactional" \
    --purpose transactional \
    --addresses 203.0.113.11,203.0.113.12,203.0.113.13

# Marketing pool
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  run --rm apid \
  /usr/local/bin/mxctl ip-pool create \
    --tenant demo \
    --name "marketing" \
    --purpose marketing \
    --addresses 203.0.113.14,203.0.113.15,203.0.113.16,203.0.113.17

# Warmup pool
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  run --rm apid \
  /usr/local/bin/mxctl ip-pool create \
    --tenant demo \
    --name "warmup" \
    --purpose warmup \
    --addresses 203.0.113.18,203.0.113.19

# Register the relay node itself
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  run --rm apid \
  /usr/local/bin/mxctl relay-node add \
    --tenant demo \
    --hostname relay.example.com \
    --ip 203.0.113.10 \
    --software postfix
```

Once registered, `repd` will begin polling all 9 sending IPs against DNSBLs, and
`correld` will start correlating rejection spikes back to this node.

### 7.5 Verify events are flowing

Wait a minute after telemetryd starts, then send a test message (§8) and check the
Message Explorer:

```bash
# Using your API token:
curl -s -H "Authorization: Bearer mxs_<your-token>" \
  "https://sentinel.example.com/v1/messages?limit=5" | jq .
```

You should see `smtp.delivered` (or `smtp.deferred`) events. Note: `relay_ip` is the
single value you set in `RELAY_NODE_IP` (telemetryd records one configured node IP — the
default maillog doesn't reliably attribute the egress IP per message); `mx_host` /
`provider` reflect the destination. Per-IP *reputation* across all pool IPs is handled
separately by `repd`.

Confirm reputation monitoring: `repd` checks every registered pool IP against the DNSBLs
on its interval and, on a listing, emits a `reputation.blacklist_hit` that `incidentd`
persists. There's no dedicated IP-list endpoint — check `repd`'s logs, or query incidents:

```bash
curl -s -H "Authorization: Bearer mxs_<your-token>" \
  "https://sentinel.example.com/v1/incidents?domain=" | jq '.incidents[] | select(.kind=="blacklist")'
```

---

## 8. End-to-End Verification

### 8.1 Send a test message

```bash
# From the relay host itself (loopback, trusted network):
sendmail -f sender@example.com test@gmail.com <<'EOF'
Subject: MX Sentinel relay test
From: sender@example.com
To: test@gmail.com

Test message from relay.example.com
EOF
```

Or via SMTP from a cPanel/Exim server:

```bash
swaks \
  --to test@gmail.com \
  --from sender@example.com \
  --server relay.example.com \
  --port 587 \
  --auth PLAIN \
  --auth-user senduser@relay.example.com \
  --auth-password '<password>'
```

### 8.2 Watch the mail log in real time

```bash
tail -f /var/log/mail.log
```

You should see lines from `postfix/smtp` (not `smtpd`) showing connection to the
receiving MTA, TLS negotiation, and a `250 OK` response (for delivery) or `4xx`/`5xx`.

The `syslog_name=postfix/txn1` (or `mkt1`, `warm1`, etc.) prefix identifies which pool
transport handled the message, which is visible in the log.

### 8.3 Confirm DKIM signing

```bash
grep "DKIM-Signature" /var/log/mail.log | tail -5
# opendkim[...]: ... DKIM-Signature added (example.com, 2026-06)
```

Check the received message headers in the receiving mailbox — Gmail shows
`dkim=pass (example.com)` in the Authentication-Results header.

### 8.4 Check the MX Sentinel dashboard

1. Open `https://sentinel.example.com` and go to **Message Explorer**.
2. Filter by the sending domain or recipient. Locate your test message.
3. Confirm the event shows the outcome (delivered/deferred/bounced), `relay_ip`,
   `provider`, `smtp_code`, and `bounce_class`.

### 8.5 Check deliverability analytics

Navigate to **Deliverability Analytics** in the dashboard. After a few messages:

- Per-provider delivery rate should appear (Gmail, Microsoft, etc.)
- Any deferrals show up with the 4xx reason code
- Bounce classification appears for any hard failures

### 8.6 Simulate a reputation event

To confirm repd → incidentd → aid works:

```bash
# Temporarily add a known DNSBL-listed IP to a pool and force a repd poll.
# Do this in a test tenant only — do not use a real sending IP.
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  run --rm apid \
  /usr/local/bin/mxctl ip-pool create \
    --tenant demo \
    --name "test-dnsbl" \
    --purpose mixed \
    --addresses 127.0.0.2
# 127.0.0.2 is listed on most test DNSBLs (e.g. dnsbl.example).
```

Within the next poll cycle, `repd` emits a `reputation.blacklist_hit` event,
`incidentd` opens an incident, and `aid` generates a root-cause narrative. Verify:

```bash
curl -s -H "Authorization: Bearer mxs_<your-token>" \
  "https://sentinel.example.com/v1/incidents?status=open" | jq .
```

---

## 9. Caveats and Operational Notes

### 9.1 Per-IP log attribution

`telemetryd` labels every event with a single `relay_ip` value (`RELAY_NODE_IP`).
Within a node, Postfix logs the bound source IP in the `connect from` / `to=` lines
via the `syslog_name` prefix. telemetryd parses these into the `relay_ip` field in
`smtp_events`, so per-IP attribution in ClickHouse is accurate — but it depends on
Postfix logging the actual bound address. The `syslog_name=postfix/<transport-name>`
entries in `master.cf` make it easy to grep the log per pool even without telemetryd.

`repd` is the authoritative source for per-IP reputation data. It polls every IP
registered in the pool's `addresses` column independently, so reputation events are
always per-IP regardless of how the maillog labels them.

### 9.2 Warmup discipline

Warmup IPs (203.0.113.18 and .19) have intentionally low `smtp_destination_concurrency_limit`
and `smtp_destination_rate_delay` in `master.cf`. Start each warmup IP at a few hundred
messages per day to a single inbox provider and double weekly. MX Sentinel's repd and
correld will surface early rejection spikes before a reputation hole forms.

Do not route real transactional or marketing volume through warmup IPs until their
daily ceiling reaches the target volume without rejection spikes.

### 9.3 MX Sentinel is not a dependency of the relay

Postfix, OpenDKIM, and the sending infrastructure have no code dependency on MX
Sentinel. If the Docker Compose stack is stopped for maintenance, the relay continues
to accept and deliver mail. telemetryd spools events to local disk and replays them
when NATS reconnects — telemetry is not lost. This is the most important operational
property of the architecture.

### 9.4 Abuse and rate controls

Add these to `main.cf` to limit abuse from compromised submission accounts:

```ini
# Limit messages per sender per unit time (per-IP rate limiting via postscreen or
# smtpd_client_message_rate_limit)
smtpd_client_message_rate_limit = 500
smtpd_client_recipient_rate_limit = 500
smtpd_client_connection_rate_limit = 100

# Reject mail to unroutable destinations
smtpd_recipient_restrictions =
    permit_mynetworks,
    permit_sasl_authenticated,
    reject_unauth_destination,
    reject_unknown_recipient_domain
```

Consider adding `postscreen` in front of `smtpd` to drop zombie spambots before they
consume queue resources.

### 9.5 Blocklisted IP procedure

When `repd` opens a `reputation.blacklist_hit` incident for a sending IP:

1. Note which pool the IP belongs to (visible in the incident's `pool_name` field).
2. Remove the IP from active sending by setting `smtp_bind_address` on that transport
   to a different IP, or by removing the transport entry in `master.cf` and reloading
   Postfix (`sudo postfix reload`).
3. Update the pool record: remove the blocklisted IP from `addresses` in Postgres (via
   `mxctl ip-pool update` or directly via `psql`).
4. Requeue any mail that was queued to that transport:
   ```bash
   sudo postsuper -r ALL   # requeue all deferred messages to re-route
   ```
5. Investigate the abuse source. Request delisting from the DNSBL once the source is
   cleaned.

The remaining pool IPs continue sending without interruption.

### 9.6 SPF lookup limit

If future configuration adds `include:` mechanisms to the SPF record (e.g. to authorize
a third-party ESP), count DNS lookups carefully. `ip4:` and `ip6:` mechanisms do not
consume lookups. `include:`, `a:`, `mx:`, `exists:`, and `redirect=` each consume at
least one. `dnsd` flags `SPF_LOOKUP_LIMIT` when the total exceeds 10 during validation.
Flatten the SPF record (expand `include:` chains into explicit `ip4:` blocks) when
approaching the limit.

### 9.7 TLS certificate for the submission port

The certificate referenced in `smtpd_tls_cert_file` must be valid for `relay.example.com`.
Use Let's Encrypt via certbot:

```bash
sudo apt-get install -y certbot
sudo certbot certonly --standalone -d relay.example.com \
    --pre-hook "systemctl stop postfix" \
    --post-hook "systemctl start postfix && systemctl reload opendkim"
```

Update `main.cf` to use the certbot paths:

```ini
smtpd_tls_cert_file = /etc/letsencrypt/live/relay.example.com/fullchain.pem
smtpd_tls_key_file  = /etc/letsencrypt/live/relay.example.com/privkey.pem
```

Certbot auto-renews via systemd timer — no manual intervention needed.

### 9.8 Outbound spam & malware filtering

A shared-IP relay's biggest risk is a compromised or abusive customer account blasting
spam and dragging the whole pool onto a blocklist. `deploy/install.sh` (relay mode)
installs two Postfix milters, chained **after** OpenDKIM, so every outbound message is
scanned on the way out:

- **rspamd** — content scoring plus **per-authenticated-user rate limiting**. Clear spam
  (score ≥ 15, incl. the GTUBE test) is rejected; each SASL user is capped (default
  **200/hour, 1000/day**). It uses the stack's loopback Redis (`127.0.0.1:6379`, db 1).
- **ClamAV** — `clamav-milter` rejects mail carrying malware (`OnInfected Reject`).

The resulting chain is `smtpd_milters = OpenDKIM(8891), rspamd(11332), ClamAV(7357)`.
`milter_default_action = accept` means if a milter is down the relay keeps sending
(availability over a hard block) — monitor the milters so an outage isn't silent.

Install or retune on an existing relay (idempotent, no redeploy):

```bash
sudo bash deploy/install.sh --wire-relay-spam
```

**Tuning the per-user caps** — edit `/etc/rspamd/local.d/ratelimit.conf` and
`sudo systemctl reload rspamd`. The `reject`/`add_header` thresholds live in
`/etc/rspamd/local.d/actions.conf`.

#### Verify it works

Authenticate as a real SMTP user and send the two standard test payloads — both should be
**rejected with a 5xx** at submission:

```bash
# Spam (GTUBE) — rspamd rejects:
swaks --to probe@example.net --from mailer@send.example.com \
  --server relay.example.com --port 587 --tls \
  --auth LOGIN --auth-user mailer@send.example.com --auth-password '<password>' \
  --header "Subject: gtube test" \
  --body 'XJS*C4JDBQADN1.NSBN3*2IDNEN*GTUBE-STANDARD-ANTI-UBE-TEST-EMAIL*C.34X'

# Malware (EICAR) — ClamAV rejects:
swaks --to probe@example.net --from mailer@send.example.com \
  --server relay.example.com --port 587 --tls \
  --auth LOGIN --auth-user mailer@send.example.com --auth-password '<password>' \
  --attach-type text/plain --attach - \
  --body 'eicar test' <<<'X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*'
```

Check the rejections and rspamd's verdict:

```bash
grep -E 'milter-reject|rspamd|clamav' /var/log/mail.log | tail
rspamc stat            # rspamd counters
journalctl -u clamav-milter -n 20
```

A normal (non-spam) test message from the same user should still deliver — confirm you
haven't set the thresholds so tight that legitimate mail is caught.

### 9.9 Auto-suspending abusive accounts (`abused`)

rspamd stops spam it recognizes on the way out; the **`abused`** daemon catches the
account that *receivers* are already rejecting — and amputates it before the shared pool's
reputation is spent. It's the containment layer behind the prevention layer.

How it works: `telemetryd` now stamps every outbound event with the authenticated
`sasl_username` (parsed from the submission log line). `abused` consumes that SMTP
telemetry from the bus and keeps a rolling per-user window of **reputation-harming
bounces** — recipients returning `spam` / `block` / `reputation` rejections. When an
account crosses the threshold it:

1. disables that SMTP submission user in Postgres — Dovecot blocks its next login
   instantly (same `enabled` flag the dashboard's **SMTP Users** page shows), and
2. opens a **critical incident** (visible under Incidents / `GET /v1/incidents`) recording
   the user, counts, and rate.

Delivered mail and transient deferrals never count against an account, so a legitimate
high-volume sender isn't suspended for sending a lot — only for receivers rejecting it as
spam/blocklisted. Suspension is idempotent (a restart won't reopen incidents).

Thresholds are flags on the daemon (defaults shown):

| Flag | Default | Meaning |
|------|---------|---------|
| `--window` | `1h` | rolling accounting window |
| `--abuse-count` | `25` | absolute spam/block bounces that trip suspension |
| `--abuse-rate` | `0.30` | fraction of windowed mail bounced as spam/block that trips it |
| `--min-volume` | `50` | minimum messages before the rate trigger applies |

`abused` runs as a container in the `app` profile — no relay-host install needed (it reads
the bus, not the maillog directly). Re-enable a cleared account from the dashboard (SMTP
Users → Enable) or `psql`. Tune thresholds by editing the `abused` service command in
`deploy/docker-compose.yml`, e.g. `["/usr/local/bin/abused","--abuse-count","15"]`.

### 9.10 Telemetry not appearing in the Message Explorer

The path is `telemetryd` (tails the maillog) → bus → `ingestd` → ClickHouse → Explorer. Two
common breaks:

- **`telemetryd: open /maillog: permission denied`** — the host maillog is `0640 root:adm`
  and the container isn't in the `adm` group. The compose `telemetryd` service sets
  `group_add: ["4"]` (adm on Debian/Ubuntu) to fix this without making the log
  world-readable. If `getent group adm` shows a different GID, change it to match and
  recreate the container. Quick one-off check: `chmod 0644 /var/log/mail.log` (resets on
  logrotate — the `group_add` is the durable fix).
- **Events silently dropped** — `telemetryd` attributes each message to a tenant by its
  **From domain** and drops mail from domains it doesn't know (`no tenant for sending
  domain` in its logs). Register each sending domain with `mxctl domain add` (§10).

On a `compose restart`, daemons can momentarily start before NATS is resolvable
(`lookup nats … no such host`); the services carry `restart: unless-stopped` so they
self-heal — if one is stuck down, `… up -d` brings it back respecting dependencies.

---

## 10. Shared-hosting: onboarding a client domain

For a shared web/email-hosting relay, clients send **person-to-person** mail **From their
own domains** (`user@clientdomain.com`) through this relay — not marketing blasts. cPanel
(Exim) already **DKIM-signs** each client's mail with that client's own domain key and
publishes the key in the client's DNS. So the relay's job is simply to **pass that
signature through unmodified** — it does **not** sign client domains (OpenDKIM only signs
the relay's own `MAIL_DOMAIN`, leaving other domains' signatures intact). Don't enable
anything that rewrites signed content (e.g. subject rewriting) or you'll break the
pass-through signature; rspamd here only *adds* headers, which is DKIM-safe.

Because DMARC passes on aligned SPF **or** aligned DKIM, cPanel's aligned DKIM alone gets a
client domain to `dmarc=pass`. So onboarding a client is just:

```bash
# Register the domain (telemetry attribution + dnsd monitoring) and print the records:
mxctl domain add --tenant <slug> --name clientdomain.com
```

That prints the **SPF** and **DMARC** records to hand the client. DKIM is already handled by
cPanel — nothing to do on the relay. Recommended client records:

- **SPF** (TXT `@`): add the relay so SPF also passes/aligns —
  `v=spf1 include:spf.example.net ~all` (or `ip4:<relay-ip>`). Not strictly required when
  DKIM passes, but it avoids SPF-fail penalties.
- **DMARC** (TXT `_dmarc`): `v=DMARC1; p=none; rua=mailto:dmarc@…` — start at `p=none`,
  tighten later.
- **DKIM**: none on the relay — cPanel signs and publishes the client's key.

Verify a client's first send the same way as §8/§9.8: check `dkim=pass header.d=clientdomain.com`
and `dmarc=pass` in the receiver's `Authentication-Results`, and watch the message in the
Message Explorer (filter by the client's SMTP user).

---

## 11. Connecting a whole hosting server (cPanel/WHM, 500+ domains)

When the relay fronts a shared hosting box (e.g. `host1.example.net` with hundreds of
client domains) replacing MailChannels, the hosting server authenticates with **one**
submission credential and relays *all* its clients' mail through it. You do **not** create
per-client SMTP users.

### 11.1 Point the hosting server at the relay

```bash
# One credential for the whole server:
mxctl smtp-user create --tenant <slug> --username host1@sentinel.example.net --password '<pw>'
```

In WHM → Exim Configuration Manager, route outbound through `sentinel.example.net:587` with
that login (the Exim router/transport/authenticator from §4 / `docs/smarthost.md`, replacing
the MailChannels config). cPanel keeps DKIM-signing each client domain and the relay passes
those signatures through untouched (§10), so client mail stays authenticated (`dmarc=pass`
via the client's own aligned DKIM).

### 11.2 Telemetry without registering 500 domains

`telemetryd` attributes each message to a tenant by its From-domain and would otherwise
**drop** mail from un-registered domains. For a shared relay it's given a **fallback
tenant** so it attributes everything to the provider's tenant instead:

- The installer sets `MXS_FALLBACK_TENANT_SLUG=<your tenant slug>` in `deploy/.env` on relay
  installs (telemetryd runs `--fallback-tenant-slug …`). Existing deployments: add that line
  and redeploy. Now **all** outbound telemetry lands in the Message Explorer — filter by the
  client's From-domain or SMTP user — with zero per-domain setup.

To also have `dnsd` **monitor** specific client domains' SPF/DKIM/DMARC posture, bulk-import
them (idempotent) — e.g. straight from cPanel's domain list:

```bash
cat /etc/trueuserdomains | mxctl domain import --tenant <slug>
```

### 11.3 Abuse isolation with one shared credential

The danger of a shared login: per-*credential* controls would punish all 500 clients for
one bad actor. So the relay isolates abuse per **sending domain**:

- **rspamd rate limits are keyed on the envelope-from domain** (not the login), so a
  single compromised client domain is throttled (default 300/h, 3000/d — tune
  `/etc/rspamd/local.d/ratelimit.conf`) without affecting the others.
- **`abused` detects abuse per sending domain** and opens a **critical incident** naming the
  domain — it does **not** disable the shared credential (that would take down everyone).
  Remediate at the source: suspend or clean the offending cPanel account on the hosting
  server. (`-suspend-credential` exists only for dedicated per-sender logins; never enable
  it with a shared relay credential.)

### 11.4 SPF for client domains (optional but recommended)

cPanel's pass-through DKIM already carries DMARC, so you don't *have* to touch 500 SPF
records to deliver. To also get `spf=pass`, have each client (or a bulk DNS update, if their
zones are on your nameservers) swap the old `include:relay.mailchannels.net` for your
`include:spf.example.net`.
