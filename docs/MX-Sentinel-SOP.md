# Mail Captain — Operations Wiki & SOP

**Version:** 1.0 | **Status:** Current | **Audience:** Platform operators, sysadmins, support engineers

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [Architecture](#2-architecture)
3. [Service Inventory](#3-service-inventory)
4. [Initial Server Deployment](#4-initial-server-deployment)
5. [Configuration Reference](#5-configuration-reference)
6. [First-Time Bootstrap](#6-first-time-bootstrap)
7. [Postfix Relay Setup](#7-postfix-relay-setup)
8. [Connecting cPanel / WHM Servers](#8-connecting-cpanel--whm-servers)
9. [Connecting Other Senders](#9-connecting-other-senders)
10. [AI Diagnostics (Ollama)](#10-ai-diagnostics-ollama)
11. [Day-to-Day Operations](#11-day-to-day-operations)
12. [Dashboard User Guide](#12-dashboard-user-guide)
13. [API Reference Summary](#13-api-reference-summary)
14. [Backup & Recovery](#14-backup--recovery)
15. [Security Hardening](#15-security-hardening)
16. [Troubleshooting](#16-troubleshooting)
17. [Glossary](#17-glossary)

---

## 1. System Overview

Mail Captain is an **AI-powered email infrastructure observability and deliverability intelligence platform**. It sits alongside your outbound mail relay — it is never in the mail path — and collects, correlates, and explains everything that happens to your outbound mail.

### What it does

| Capability | Description |
|---|---|
| **SMTP telemetry** | Parses Postfix maillogs in real time into per-message delivery events (outcome, SMTP code, provider, relay IP, bounce reason). |
| **DNS intelligence** | Continuously validates and snapshots SPF, DKIM, DMARC, MX, MTA-STS, TLS-RPT, BIMI for every monitored domain. Detects drift. |
| **DMARC ingestion** | Ingests aggregate DMARC XML reports and shows per-source alignment. |
| **Correlation** | Joins rejection spikes against DNS change events to produce root-cause hypotheses (*"Outlook auth rejections began 60 seconds after the DKIM selector was removed"*). |
| **Reputation monitoring** | Checks relay IPs against DNSBLs. Auto-pulls a listed IP from the Postfix rotation. Ingests FBL complaints. Fetches Gmail Postmaster reputation. |
| **Outbound abuse detection** | Auto-suspends SMTP submission credentials whose mail is being rejected as spam. Detects volume anomalies and credential compromise. |
| **AI diagnostics** | Uses a local LLM to write human-readable root-cause summaries and remediation steps for each incident. |
| **REST API + dashboard** | Full web UI and API for domains, messages, incidents, settings, SMTP users, and more. |

### What it is NOT

- Mail Captain is **not** in the mail path. Removing or stopping it does not affect mail delivery.
- It is **not** an MTA. It observes Postfix; it does not replace it.
- It does **not** store message bodies or subject lines. Only headers, metadata, and SMTP transaction results are retained.

### Core pipeline

```
Collect → Normalize → Correlate → Analyze → Explain → Remediate
```

---

## 2. Architecture

### Network topology (production)

```
                         Internet
                            │
                     ┌──────▼──────┐
                     │  Caddy :443 │  (Let's Encrypt TLS — automatic)
                     │  Caddy :80  │  (redirects to 443, ACME challenge)
                     └──────┬──────┘
                            │
              ┌─────────────┴──────────────┐
              │    Same origin — no CORS   │
              ▼                            ▼
      /v1/* → apid:8080          /* → dashboard:3000
      (REST API)                 (Next.js UI)

  ────────────── Docker internal network ──────────────────

  postgres   clickhouse   redis   nats   minio
  (127.0.0.1 only — not internet-reachable)

  dnsd  correld  incidentd  repd  aid  dmarcd
  rbld  anomalyd  fbld  authwatchd  abused
  (no public ports)

  ─────────────────── Host (optional) ─────────────────────

  Postfix + OpenDKIM + Rspamd + ClamAV + Dovecot
  Ollama (LLM — port 11434)
```

### Storage roles

| Store | What lives there |
|---|---|
| **PostgreSQL** | Tenants, users, domains, DNS snapshots, API credentials, SMTP submission users, incidents, settings, audit log |
| **ClickHouse** | SMTP telemetry events (`smtp_events`), DMARC alignment records, analytics rollups |
| **Redis** | Session tokens (24h TTL), rate-limit counters, caches |
| **NATS JetStream** | Event bus — decouples producers from consumers. Streams: `SMTP`, `DNS`, `REPUTATION`, `AI` |
| **MinIO / S3** | Raw DMARC XML archives, compressed forensic reports |

### Event bus subjects

All inter-service communication goes through NATS. Subject format: `mxs.<family>.<tenant_id>.<event>`

| Subject pattern | Published by | Consumed by |
|---|---|---|
| `mxs.smtp.<tenant>.delivered/bounced/rejected/deferred/received` | `telemetryd` | `ingestd`, `correld`, `abused`, `anomalyd`, `authwatchd` |
| `mxs.dns.<tenant>.changed` / `.validation_failed` | `dnsd` | `correld`, `incidentd` |
| `mxs.reputation.<tenant>.blacklist_hit` | `repd`, `rbld` | `incidentd` |
| `mxs.ai.<tenant>.rca` | `aid` | API, dashboard |

---

## 3. Service Inventory

| Container | Binary | Role |
|---|---|---|
| `apid` | `/usr/local/bin/apid` | REST API server on `:8080`. All `/v1` endpoints. |
| `dnsd` | `/usr/local/bin/dnsd` | Polls monitored domains every 5 min; validates SPF/DKIM/DMARC/MX; writes DNS snapshots; emits `dns.*` events. |
| `telemetryd` | `/usr/local/bin/telemetryd` | Tails Postfix maillog; parses into `smtp.*` events; spools to disk if NATS is down. Requires `relay` profile. |
| `ingestd` | `/usr/local/bin/ingestd` | Consumes `smtp.*` from NATS; batch-writes to ClickHouse `smtp_events`. |
| `correld` | `/usr/local/bin/correld` | Watches SMTP telemetry for per-provider rejection spikes; correlates against recent DNS changes; produces root-cause hypothesis. |
| `incidentd` | `/usr/local/bin/incidentd` | Turns `dns.*` and `reputation.*` events into queryable incidents in Postgres. |
| `repd` | `/usr/local/bin/repd` | Checks sending IPs against public DNSBLs; emits `reputation.blacklist_hit`. |
| `rbld` | `/usr/local/bin/rbld` | Monitors the relay's own egress IPs against DNSBLs; on a listing, opens an incident and writes a healthy-IP file that a host cron hook uses to remove the bad IP from Postfix. |
| `abused` | `/usr/local/bin/abused` | Watches per-SMTP-user telemetry; auto-suspends credentials whose recipients are rejecting as spam. |
| `anomalyd` | `/usr/local/bin/anomalyd` | Learns per-domain hourly volume baselines; opens incidents on spikes. |
| `fbld` | `/usr/local/bin/fbld` | Ingests ARF feedback-loop complaints from a drop directory; fetches Gmail Postmaster reputation. |
| `authwatchd` | `/usr/local/bin/authwatchd` | Detects per-credential compromise signals (recipient blasting, bounce spikes); supports opt-in auto-lock. |
| `aid` | `/usr/local/bin/aid` | Reads unanalyzed incidents; sends to local LLM; writes `ai_summary` and `ai_remediation` back to the incident. |
| `dmarcd` | `/usr/local/bin/dmarcd` | Watches a drop directory for DMARC aggregate XML; archives to MinIO; parses and writes to Postgres + ClickHouse. |
| `dashboard` | Node.js `server.js` | Next.js standalone server on `:3000`. |
| `caddy` | Caddy v2 | TLS termination + reverse proxy. `/v1/*` → `apid`; `/*` → `dashboard`. |
| `mxctl` | `/usr/local/bin/mxctl` | Operator CLI — not a long-running service. Used with `docker compose run --rm apid /usr/local/bin/mxctl …`. |

---

## 4. Initial Server Deployment

### 4.1 Prerequisites

| Requirement | Notes |
|---|---|
| VPS | Ubuntu 22.04 LTS or Debian 12. 2+ vCPU, 4 GB+ RAM (8 GB recommended if running Ollama). |
| Docker Engine + Compose v2 | Run `docker compose version` to verify. |
| Domain name | An A record pointing to the VPS public IP — e.g. `sentinel.example.com`. DNS must resolve before launch (Let's Encrypt needs it). |
| Open ports | 22 (SSH), 80 (ACME HTTP challenge), 443 (HTTPS). Nothing else needs to be public. |
| `make` (optional) | `apt install make` — shorthand for the longer `docker compose` commands. |

### 4.2 Install Docker

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER
newgrp docker
docker compose version   # must show v2.x
```

### 4.3 Clone the repository

```bash
git clone https://github.com/zezekim/mxsentinel.git
cd mxsentinel
```

### 4.4 Create the secrets file

```bash
cp deploy/.env.prod.example deploy/.env
nano deploy/.env
```

Fill in every value. Use `openssl rand -hex 32` to generate strong passwords. **Never commit `deploy/.env` to git.**

| Variable | What it's for | Example |
|---|---|---|
| `MXS_DOMAIN` | Your public hostname — must match your DNS A record | `sentinel.example.com` |
| `MXS_ACME_EMAIL` | Let's Encrypt expiry notices | `ops@example.com` |
| `PG_PASSWORD` | PostgreSQL password | `openssl rand -hex 32` |
| `MINIO_ROOT_PASSWORD` | MinIO password | `openssl rand -hex 32` |
| `MXS_AI_ENDPOINT` | Local LLM URL (optional) | `http://host.docker.internal:11434/v1` |
| `MXS_AI_MODEL` | Model name | `llama3.2:3b` |
| `RELAY_NODE_IP` | Relay egress IP (if relay is on this box) | `203.0.113.1` |
| `MXS_TELEMETRY_HASHKEY` | Hex key for recipient address hashing | `openssl rand -hex 32` |

### 4.5 Launch the stack

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  up -d --build
```

**What happens on first launch:**

1. Docker builds all images (Go services → distroless static image; dashboard → Next.js standalone bundle with `NEXT_PUBLIC_API_BASE` baked in as `https://<MXS_DOMAIN>`).
2. The `migrate` one-shot container applies all Postgres + ClickHouse migrations and exits with code 0.
3. All application services wait for `migrate` to succeed before starting.
4. Caddy obtains a Let's Encrypt TLS certificate. This takes 30–60 seconds and requires DNS + ports 80/443 to be reachable.

### 4.6 Verify the stack

```bash
# Check container states
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env ps

# migrate → Exited (0)  ✓
# All others → Up / healthy  ✓

# Confirm TLS cert was issued
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env logs caddy | grep -i "certificate"
```

Then open `https://sentinel.example.com` in a browser — you should see the login page.

### 4.7 Firewall

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw --force enable
sudo ufw status
```

Verify backing services are NOT public:

```bash
ss -tlnp | grep -E '5432|9000|6379|4222'
# Every line must show 127.0.0.1:PORT — never 0.0.0.0:PORT
```

---

## 5. Configuration Reference

All services share a single config struct. Values load in this precedence order (highest wins):

```
MXS_* environment variables  >  YAML config file  >  built-in defaults
```

The default YAML is `deploy/config/mxsentinel.example.yaml`. In production, all values are set via `MXS_*` env vars in `deploy/.env`.

### Full environment variable reference

| Variable | Default | Description |
|---|---|---|
| `MXS_POSTGRES_DSN` | `postgres://mxsentinel:mxsentinel@localhost:5432/mxsentinel?sslmode=disable` | PostgreSQL connection string |
| `MXS_CLICKHOUSE_ADDR` | `localhost:9000` | ClickHouse native protocol address |
| `MXS_CLICKHOUSE_DATABASE` | `mxsentinel` | ClickHouse database name |
| `MXS_REDIS_ADDR` | `localhost:6379` | Redis address |
| `MXS_NATS_URL` | `nats://localhost:4222` | NATS server URL |
| `MXS_OBJECTSTORE_ENDPOINT` | `localhost:9001` | MinIO/S3 endpoint |
| `MXS_OBJECTSTORE_BUCKET` | `mxsentinel` | Object storage bucket |
| `MXS_OBJECTSTORE_ACCESSKEY` | `minioadmin` | S3 access key |
| `MXS_OBJECTSTORE_SECRETKEY` | `minioadmin` | S3 secret key |
| `MXS_AI_ENDPOINT` | `http://localhost:11434/v1` | OpenAI-compatible LLM base URL |
| `MXS_AI_MODEL` | `llama3` | Model name to request |
| `MXS_AI_APIKEY` | _(empty)_ | API key (optional; most local servers ignore it) |
| `MXS_LOGLEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `MXS_HTTPADDR` | `:9090` | Health/metrics endpoint (internal — not the API) |
| `RELAY_NODE_IP` | _(empty)_ | Relay's outbound IP (used by `rbld`, `repd`) |
| `RELAY_EGRESS_IPS` | _(empty)_ | Comma-separated list of all egress IPs |
| `MXS_TELEMETRY_HASHKEY` | _(empty)_ | Hex key for recipient address HMAC-SHA256 hashing |

---

## 6. First-Time Bootstrap

### 6.1 Create the first owner account

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env \
  run --rm apid \
  /usr/local/bin/mxctl user create \
    --tenant demo \
    --email admin@yourcompany.com \
    --password 'StrongPassword123!' \
    --role owner
```

Roles: `owner` / `admin` → full access; `operator` → read + write; `viewer` → read only.

### 6.2 Create an API token

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env \
  run --rm apid \
  /usr/local/bin/mxctl apikey create \
    --tenant demo \
    --scopes read,write
```

The token (`mxs_<prefix>_<secret>`) is printed **once**. Only a SHA-256 hash is stored. Copy it immediately.

**Scope guidance:**
- `read` — monitoring dashboards, read-only integrations
- `read,write` — integrations that trigger rechecks or resolve incidents
- `admin` — user management, domain deletion, settings changes

### 6.3 Add your domains

**Via dashboard:** Log in → Domains → click **+ Add Domain** → enter the domain name → click **Add**.

**Via CLI (bulk import from cPanel):**
```bash
cat /etc/trueuserdomains | docker compose -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml --profile app --env-file deploy/.env \
  run --rm -i apid \
  /usr/local/bin/mxctl domain import --tenant demo
```

**Via CLI (single domain):**
```bash
docker compose … run --rm apid \
  /usr/local/bin/mxctl domain add --tenant demo --name example.com
```

Once a domain is registered, `dnsd` picks it up on the next poll cycle (default: every 5 minutes) and captures the first DNS snapshot.

### 6.4 Seed demo data (optional)

For a fresh install that needs sample data immediately:

```bash
docker compose … run --rm apid /usr/local/bin/mxctl seed
```

Creates the `demo` tenant, registers `example.com`, and publishes a sample set of SMTP events.

### 6.5 Reset a locked-out owner password

```bash
docker compose … run --rm apid \
  /usr/local/bin/mxctl user set-password \
    --tenant demo \
    --email admin@yourcompany.com \
    --password 'NewStrongPassword!'
```

---

## 7. Postfix Relay Setup

This section applies only if you are running Postfix on the same VPS. Skip if you are connecting an external relay.

### 7.1 What the installer configures

Running `bash deploy/install.sh` (or `make install` after installing `make`) sets up:

- Postfix as an outbound relay with IP pool rotation
- OpenDKIM for per-domain DKIM signing
- Rspamd for outbound spam scoring and per-user rate limiting
- ClamAV for malware rejection
- Dovecot with Postgres passdb for SASL authentication (no flat password files)
- `telemetryd` in the `relay` Docker profile to parse Postfix maillogs in real time

### 7.2 Key Postfix settings applied

| Setting | Value | Purpose |
|---|---|---|
| `smtpd_sasl_type` | `dovecot` | Delegate SASL auth to Dovecot |
| `smtpd_tls_auth_only` | `yes` | Never offer AUTH before TLS |
| `smtpd_relay_restrictions` | `permit_sasl_authenticated, reject` | Only authenticated clients can relay |
| `smtp_sender_dependent_routing` | enables IP pool selection per sender domain | |

### 7.3 Enable the relay profile

After the installer runs, telemetryd needs the `relay` profile:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app --profile relay \
  --env-file deploy/.env \
  up -d
```

Set `RELAY_NODE_IP` and `MAILLOG_PATH` in `deploy/.env` before starting.

### 7.4 RBL rotation hook

`rbld` writes a file of healthy IPs to `/var/lib/mxsentinel/healthy-ips` (inside the container, bind-mounted to the host). A host-side cron hook reads this file and rebuilds the Postfix `randmap` source, then runs `postfix reload`. This means a listed IP is automatically removed from rotation without halting all mail.

The hook script is at `deploy/hooks/rbl-rotation-hook.sh`. Install it as a cron job:

```bash
# Example: run every 15 minutes
*/15 * * * * /opt/mxsentinel/deploy/hooks/rbl-rotation-hook.sh >> /var/log/rbl-rotation.log 2>&1
```

---

## 8. Connecting cPanel / WHM Servers

This is the most common integration scenario. One SMTP submission credential is created for the entire cPanel server; cPanel/Exim continues DKIM-signing each customer's mail.

### Step 1 — Create one SMTP submission user

In the dashboard: **SMTP Users → Add user**

- **Username:** a full address like `relay@relay.example.com` (globally unique SASL login)
- **Password:** strong, at least 16 characters
- **Sending domain:** your relay hostname (optional, for filtering)

Or via CLI:
```bash
docker compose … run --rm apid \
  /usr/local/bin/mxctl smtp-user create \
    --tenant demo \
    --username relay@relay.example.com \
    --password 'your-strong-password' \
    --domain relay.example.com
```

### Step 2 — Configure Exim in WHM

WHM → **Service Configuration → Exim Configuration Manager → Advanced Editor**

Add the following blocks (replace `relay.example.com`, username, and password):

```
# ROUTERS START
send_via_mxsentinel:
  driver = manualroute
  domains = ! +local_domains
  transport = mxsentinel_smtp
  route_list = * relay.example.com::587
  no_more

# TRANSPORTS START
mxsentinel_smtp:
  driver = smtp
  hosts_require_auth = relay.example.com
  hosts_require_tls = relay.example.com

# AUTHS START
mxsentinel_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : relay@relay.example.com : YOUR_SMTP_PASSWORD
```

Save and restart Exim.

### Step 3 — DKIM: no changes needed

cPanel already DKIM-signs each account's mail and publishes the selector in the customer's DNS. Mail Captain's relay passes the signature through untouched.

### Step 4 — Publish SPF + DMARC per sending domain

For each customer domain:

**SPF** — add the relay to the existing SPF record:
```
example.com.  TXT  "v=spf1 include:<your-spf-endpoint> ~all"
```
The SPF include endpoint is set in **Settings** in the dashboard.

**DMARC** — start at `p=none`, tighten once SPF/DKIM are confirmed passing:
```
_dmarc.example.com.  TXT  "v=DMARC1; p=none; rua=mailto:dmarc@example.com"
```

### Step 5 — Import domain list for monitoring

```bash
ssh root@cpanel-server "cat /etc/trueuserdomains" | \
  docker compose … run --rm -i apid \
  /usr/local/bin/mxctl domain import --tenant demo
```

### Step 6 — Test and verify

```bash
swaks --to test@gmail.com --from sender@customerdomain.com \
  --server relay.example.com --port 587 --tls \
  --auth LOGIN \
  --auth-user relay@relay.example.com \
  --auth-password 'YOUR_SMTP_PASSWORD'
```

Check:
- Dashboard → **Message Explorer** → find the test message → click to expand → confirm outcome `delivered`
- Open the received email's headers: `Authentication-Results` should show `spf=pass dkim=pass dmarc=pass`
- Dashboard → **Domains** → no critical findings on the sending domain

---

## 9. Connecting Other Senders

### Standalone Postfix (as a client)

```ini
# /etc/postfix/main.cf
relayhost = [relay.example.com]:587
smtp_sasl_auth_enable = yes
smtp_sasl_password_maps = hash:/etc/postfix/sasl_passwd
smtp_sasl_security_options = noanonymous
smtp_tls_security_level = encrypt
```

```bash
# /etc/postfix/sasl_passwd
[relay.example.com]:587    mailer@send.example.com:your-password

sudo postmap /etc/postfix/sasl_passwd
sudo chmod 600 /etc/postfix/sasl_passwd /etc/postfix/sasl_passwd.db
sudo systemctl reload postfix
```

### Standalone Exim

```
# Router
smarthost:
  driver = manualroute
  domains = ! +local_domains
  transport = relay_smtp
  route_list = * relay.example.com::587
  no_more

# Transport
relay_smtp:
  driver = smtp
  hosts_require_auth = relay.example.com
  hosts_require_tls  = relay.example.com

# Authenticator
relay_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : mailer@send.example.com : your-password
```

### Application SMTP (Nodemailer, PHPMailer, Python, etc.)

| Setting | Value |
|---|---|
| Host | `relay.example.com` |
| Port | `587` |
| Secure | `false` (STARTTLS, not implicit TLS) |
| requireTLS | `true` |
| auth.user | `mailer@send.example.com` |
| auth.pass | your password |

**Important:** Always use port `587` (submission). Port `25` is for trusted network/loopback only and does not accept SASL authentication.

### Production TLS certificate for the relay

The installer uses a self-signed certificate. For production, issue a real cert:

```bash
sudo certbot certonly --standalone -d relay.example.com \
  --pre-hook "systemctl stop postfix" \
  --post-hook "systemctl start postfix"

sudo postconf -e \
  "smtpd_tls_cert_file = /etc/letsencrypt/live/relay.example.com/fullchain.pem" \
  "smtpd_tls_key_file  = /etc/letsencrypt/live/relay.example.com/privkey.pem"

sudo postconf -P "submission/inet/smtpd_tls_security_level=encrypt"
sudo systemctl reload postfix
```

---

## 10. AI Diagnostics (Ollama)

The `aid` daemon reads open incidents and calls a local OpenAI-compatible LLM to produce:
- `ai_summary` — a human-readable root-cause narrative
- `ai_remediation` — a prioritized list of specific remediation steps

**Mail delivery never depends on this.** If Ollama is not running, incidents are still recorded; they just won't have AI-generated explanations.

### Install Ollama and pull a model

```bash
# Install Ollama (see https://ollama.com)
curl -fsSL https://ollama.com/install.sh | sh

# Pull a model (3B is comfortable on 8GB RAM alongside other services)
ollama pull llama3.2:3b
# or for better reasoning on 16GB+:
ollama pull llama3:8b

ollama serve
```

### Connect aid to Ollama on Linux

On Linux, `host.docker.internal` is not automatically available. Use the Docker bridge IP:

```bash
# Find the bridge IP (usually 172.17.0.1)
ip route show | grep docker0

# Set in deploy/.env:
MXS_AI_ENDPOINT=http://172.17.0.1:11434/v1
MXS_AI_MODEL=llama3.2:3b
```

Then restart the `aid` container:
```bash
docker compose … restart aid
```

`aid` retries on a backoff loop until the endpoint is reachable.

### Verify AI is working

In the dashboard, open **Incidents**. Any open incident older than ~30 seconds should have the `ai_summary` field populated. If not, check aid logs:

```bash
docker compose … logs aid --tail 50
```

---

## 11. Day-to-Day Operations

### Updating to a new version

```bash
cd ~/mxsentinel
git pull
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env \
  up -d --build
```

Migrations run automatically. Data in named volumes is preserved.

### Viewing logs

```bash
# All services
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env logs -f

# Single service
docker compose … logs -f apid
docker compose … logs -f aid
docker compose … logs -f dnsd
```

### Restarting a single service

```bash
docker compose … restart apid
docker compose … restart dnsd
```

### Stopping the entire stack

```bash
docker compose … down
# Add --volumes only to DESTROY all data (irreversible)
```

### Checking service health

```bash
docker compose … ps
# All services should show "Up" or "healthy"
# migrate should show "Exited (0)"
```

### Manually triggering a DNS recheck

Via API:
```bash
curl -X POST https://sentinel.example.com/v1/domains/<domain-id>/dns/recheck \
  -H "Authorization: Bearer mxs_..."
```

Via dashboard: **Domains → click domain → Recheck now**

### Creating a new SMTP user

Dashboard: **SMTP Users → Add user**

CLI:
```bash
docker compose … run --rm apid \
  /usr/local/bin/mxctl smtp-user create \
    --tenant demo \
    --username mailer@send.example.com \
    --password 'strong-password' \
    --domain send.example.com
```

### Disabling a compromised SMTP user

Dashboard: **SMTP Users → toggle the Enabled switch** (takes effect on next auth attempt)

CLI:
```bash
# Disable
curl -X PATCH https://sentinel.example.com/v1/smtp-users/<id> \
  -H "Authorization: Bearer mxs_..." \
  -H "Content-Type: application/json" \
  -d '{"enabled": false}'
```

Or via **Auth Security** page → lock the credential directly.

### Resolving an incident

Dashboard: **Incidents → Resolve**

API:
```bash
curl -X POST https://sentinel.example.com/v1/incidents/<id>/resolve \
  -H "Authorization: Bearer mxs_..."
```

### Pausing DNS monitoring for a domain

Dashboard: **Domains → click domain → Pause monitoring**

API:
```bash
curl -X PATCH https://sentinel.example.com/v1/domains/<id> \
  -H "Authorization: Bearer mxs_..." \
  -H "Content-Type: application/json" \
  -d '{"status": "paused"}'
```

---

## 12. Dashboard User Guide

### Login

Navigate to `https://sentinel.example.com/login`. Log in with your email and password. Sessions last 24 hours; you can log out explicitly via **Account → Log out**.

### Domains

The home screen. Shows every monitored domain with its current health status for SPF, DKIM, DMARC, and MX.

- **Green (ok):** no findings for this category
- **Yellow (warning):** non-critical findings (e.g. SPF approaching the lookup limit)
- **Red (critical):** action required (e.g. DKIM selector missing, SPF failing)
- **Grey (unknown):** domain has not been checked yet

Click a domain to open the detail view with full findings, the DNS drift timeline, and the Recheck / Pause / Delete controls.

### Message Explorer

Search and browse every SMTP transaction. Filters: domain, sender, message-ID, provider, SMTP user, outcome, date range.

Click any row to expand it and see the full details: complete response text, bounce class, enhanced status code, event ID, relay IP, and all other fields.

**Outcome codes:**
| Outcome | Meaning |
|---|---|
| `delivered` | `2xx` — accepted by the receiving server |
| `deferred` | `4xx` — temporary failure; Postfix will retry |
| `bounced` | Permanent delivery failure (`5xx`) after acceptance |
| `rejected` | Rejected at SMTP connection time |
| `received` | Accepted into the relay queue (inbound to queue) |

### Incidents

Active intelligence-layer alerts. Each incident has a severity (`info`, `warning`, `critical`), a kind (`rejection_spike`, `blacklist`, `dns_validation`, `other`), and — once analyzed — an `ai_summary` and `ai_remediation`.

Filter by status (`open` / `acknowledged` / `resolved`) or domain.

### IP Health

Shows the relay's own egress IPs checked against DNSBLs by `rbld`. A red row means the IP is currently listed — an incident is automatically opened and the IP is queued for removal from Postfix rotation.

### Velocity

Volume anomaly detection. Shows sending domains whose hourly message count spiked beyond their learned baseline. `factor` is how many times above baseline (e.g. `10×`).

### Reputation

Feedback-loop complaint counts and Gmail Postmaster reputation per sending domain. High complaint rates or a `BAD`/`LOW` Postmaster rating need immediate attention.

### Auth Security

Per-credential compromise signals. Flags SMTP submission users showing behavioral indicators: recipient blasting, unusual bounce rates, sudden volume spikes. Admin users can lock a credential directly from this page.

### SMTP Users

Create, enable/disable, reset passwords for, and delete SMTP submission credentials.

### Settings

Tenant-level mail settings that drive DNS validation guidance and setup instructions:

| Setting | Purpose |
|---|---|
| SPF include endpoint | The `include:` mechanism your tenants add to their SPF records |
| DKIM selector | The selector name OpenDKIM signs with (default: `mxs`) |
| DMARC policy | `none` / `quarantine` / `reject` — shown in setup docs |
| DMARC RUA | Where aggregate reports are sent |
| Relay host / port | Smarthost connection settings shown in setup docs |
| DNS resolver | Which resolver `dnsd` and on-demand rechecks use |

### Account

Change your login password.

---

## 13. API Reference Summary

All endpoints are under `https://sentinel.example.com/v1/`. Authentication: `Authorization: Bearer <token>`.

### Domain management

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET` | `/v1/domains` | read | List all domains with health summary |
| `POST` | `/v1/domains` | write | Add a domain for monitoring |
| `PATCH` | `/v1/domains/{id}` | write | Update status (`monitored` or `paused`) |
| `DELETE` | `/v1/domains/{id}` | admin | Remove domain and all its data |
| `GET` | `/v1/domains/{id}/health` | read | Full health + findings for one domain |
| `GET` | `/v1/domains/{id}/dns/snapshots` | read | DNS drift timeline |
| `POST` | `/v1/domains/{id}/dns/recheck` | write | Trigger immediate DNS recheck |

### Messages & analytics

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET` | `/v1/messages` | read | Message explorer (ClickHouse) |
| `GET` | `/v1/analytics/deliverability` | read | Per-provider outcome counts |
| `GET` | `/v1/analytics/rejections` | read | Classified rejection reasons |
| `GET` | `/v1/analytics/top-senders` | read | Top senders by volume/spam/rejections |
| `GET` | `/v1/dmarc/reports` | read | DMARC aggregate reports + alignment |

### Incidents & outbound security

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET` | `/v1/incidents` | read | List incidents (filter by status, domain) |
| `POST` | `/v1/incidents/{id}/resolve` | write | Mark incident resolved |
| `GET` | `/v1/rbl/status` | read | Relay egress IP DNSBL status |
| `GET` | `/v1/anomaly/recent` | read | Recent volume anomalies |
| `GET` | `/v1/reputation` | read | FBL complaints + Postmaster reputation |
| `GET` | `/v1/auth-security` | read | Credential compromise signals |
| `POST` | `/v1/auth-security/{user}/lock` | admin | Lock/unlock a compromised credential |

### Users & auth

| Method | Path | Scope | Description |
|---|---|---|---|
| `POST` | `/v1/auth/login` | public | Email + password login → session token |
| `POST` | `/v1/auth/logout` | — | Revoke session |
| `GET` | `/v1/me` | read | Current caller info (tenant, scopes, role) |
| `POST` | `/v1/me/password` | — | Change own password |
| `GET` | `/v1/users` | admin | List tenant users |
| `POST` | `/v1/users` | admin | Create a user |

### SMTP users & settings

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET` | `/v1/smtp-users` | admin | List SMTP submission credentials |
| `POST` | `/v1/smtp-users` | admin | Create credential |
| `PATCH` | `/v1/smtp-users/{id}` | admin | Enable/disable or reset password |
| `DELETE` | `/v1/smtp-users/{id}` | admin | Delete credential |
| `GET` | `/v1/settings` | read | Get tenant mail settings |
| `PUT` | `/v1/settings` | admin | Update tenant mail settings |
| `GET` | `/v1/audit` | read | Audit log of mutating API calls |

---

## 14. Backup & Recovery

### PostgreSQL backup (manual)

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env \
  exec postgres \
  pg_dump -U mxsentinel mxsentinel \
  | gzip > mxsentinel-pg-$(date +%Y%m%d-%H%M%S).sql.gz
```

### PostgreSQL restore

```bash
gunzip -c mxsentinel-pg-YYYYMMDD-HHMMSS.sql.gz | \
  docker compose … exec -T postgres \
  psql -U mxsentinel mxsentinel
```

### PostgreSQL automated backups (cron)

```bash
# /etc/cron.d/mxsentinel-backup
0 3 * * * root docker compose -f /root/mxsentinel/deploy/docker-compose.yml \
  -f /root/mxsentinel/deploy/docker-compose.prod.yml \
  --profile app --env-file /root/mxsentinel/deploy/.env \
  exec -T postgres pg_dump -U mxsentinel mxsentinel \
  | gzip > /var/backups/mxsentinel/pg-$(date +\%Y\%m\%d).sql.gz
```

### ClickHouse backup

ClickHouse data is telemetry — high volume, reconstructible from maillogs. Formal backups are optional; the ClickHouse native backup command can be used if needed:

```bash
docker compose … exec clickhouse \
  clickhouse-client --query="BACKUP DATABASE mxsentinel TO Disk('default', 'backup.zip')"
```

### What data is critical

| Store | Criticality | Notes |
|---|---|---|
| PostgreSQL | **Critical** — back up daily | Tenants, users, domains, incidents, settings — all configuration |
| ClickHouse | Medium | Telemetry is reconstructible from maillogs; loses message explorer history if not backed up |
| MinIO | Low | Raw DMARC XML — useful forensically but not operationally critical |
| Redis | None | Ephemeral — sessions and rate limit counters; auto-rebuilds |

---

## 15. Security Hardening

### Pre-launch checklist

- [ ] All secrets in `deploy/.env` are strong and unique. Not the example defaults. Use `openssl rand -hex 32`.
- [ ] `deploy/.env` is NOT committed to git. Verify: `git status deploy/.env` → should be ignored.
- [ ] `sudo ufw status` shows only ports 22, 80, 443 open to the internet.
- [ ] `ss -tlnp | grep -E '5432|9000|6379|4222'` shows `127.0.0.1:PORT` on every line — not `0.0.0.0`.
- [ ] TLS is active: `curl -I https://sentinel.example.com` returns `HTTP/2 200` with no certificate warnings.
- [ ] At least one owner account created with a strong password.
- [ ] SSH on port 22 uses key-based authentication only (`PasswordAuthentication no` in `/etc/ssh/sshd_config`).

### API token discipline

- Issue tokens with the minimum necessary scope.
- `read`-only tokens for dashboards and monitoring systems.
- `read,write` for integrations that trigger rechecks or resolve incidents.
- `admin` only for provisioning scripts.
- Rotate tokens regularly. Revoke compromised tokens immediately via the database:

```bash
# Revoke a specific token by prefix
docker compose … exec postgres \
  psql -U mxsentinel -c "UPDATE api_credentials SET revoked_at = now() WHERE token_prefix = 'mxs_abc123';"
```

### Privacy guarantee

Mail Captain never stores or logs:
- Email message bodies
- Email subject lines
- Full recipient addresses (recipient domains are stored; individual addresses are HMAC-SHA256-hashed with `MXS_TELEMETRY_HASHKEY` before storage)

This is enforced at the `telemetryd` parser boundary — nothing downstream ever sees this data.

---

## 16. Troubleshooting

### "Failed to fetch" in the dashboard

The browser can't reach the API. Most common cause: the dashboard was built with `NEXT_PUBLIC_API_BASE=http://localhost:8080` (the dev default), which points to the browser's localhost, not the server.

**Fix:** Rebuild using the prod overlay, which sets `NEXT_PUBLIC_API_BASE=https://<MXS_DOMAIN>`:
```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --profile app --env-file deploy/.env up -d --build
```

Verify what URL is baked in:
```bash
docker inspect mxsentinel-dashboard-1 | grep NEXT_PUBLIC_API_BASE
```

### TLS certificate not issued

Caddy logs show an ACME error. Common causes:

| Cause | Fix |
|---|---|
| DNS A record not yet propagated | Wait for propagation; `dig +short sentinel.example.com` should return the VPS IP |
| Port 80 blocked | Open port 80: `sudo ufw allow 80/tcp` |
| Wrong `MXS_DOMAIN` | Must exactly match the DNS hostname — no trailing dots, no protocol prefix |
| Rate limited by Let's Encrypt | Wait 1 hour; check Caddy logs for the specific error |

### SMTP authentication failure (`535 5.7.8`)

| Cause | Fix |
|---|---|
| Wrong username or password | Check SMTP Users page; username must match exactly including case |
| User is disabled | Enable via SMTP Users page or `PATCH /v1/smtp-users/{id}` `{"enabled": true}` |
| User deleted | Recreate the credential |
| Submitting on port 25 | Use port 587 — port 25 does not accept SASL |

### `530 5.7.0 Must issue a STARTTLS command first`

The client tried to authenticate before negotiating TLS. Enable STARTTLS / `requireTLS` on the client. The relay never offers `AUTH` in cleartext (`smtpd_tls_auth_only = yes`).

### Mail accepted but `dmarc=fail` at receiver

SPF or DKIM is not aligned with the `From:` domain. Check:
1. Dashboard → **Domains** → look for SPF or DKIM findings
2. Verify the SPF `include:` is published for the sending domain
3. Verify the DKIM selector TXT record is published and matches the relay's signing selector (Settings → DKIM selector)

### DNS findings not updating

`dnsd` polls every 5 minutes by default. For an immediate check: Dashboard → **Domains → click domain → Recheck now**.

If `dnsd` is not running:
```bash
docker compose … ps dnsd   # should be "Up"
docker compose … logs dnsd --tail 30
```

### Incidents not being created

Check `incidentd` and the services that feed it:
```bash
docker compose … logs incidentd --tail 30
docker compose … logs correld --tail 30
docker compose … logs repd --tail 30
```

NATS must be healthy — all these services consume from the event bus:
```bash
docker compose … ps nats
```

### Message Explorer is empty

`ingestd` writes SMTP events to ClickHouse. Check:
```bash
docker compose … logs ingestd --tail 30
docker compose … ps clickhouse
```

If `telemetryd` is not running (relay profile not enabled), no SMTP events are published to the bus. Replay a sample maillog to verify the pipeline:
```bash
docker compose … run --rm apid \
  /usr/local/bin/mxctl replay   # uses test/fixtures/maillog.sample
```

### AI summaries not appearing on incidents

```bash
docker compose … logs aid --tail 50
```

Common causes:
- Ollama not running: `curl http://localhost:11434/health`
- Wrong `MXS_AI_ENDPOINT` — on Linux, `host.docker.internal` may not resolve; use the bridge IP instead
- Model not pulled: `ollama list` on the host

### Container fails to start — port conflict

`apid` defaults to `:8080`. If something else is using that port on the host, either stop it or change the compose port mapping. Check:
```bash
ss -tlnp | grep 8080
```

---

## 17. Glossary

| Term | Definition |
|---|---|
| **SMTP submission user** | A credential (username + bcrypt-hashed password) stored in Postgres that a smarthost authenticates with to relay mail through Postfix. Created in the Dashboard or via `mxctl smtp-user create`. |
| **Tenant** | An isolated account in Mail Captain. All domains, users, telemetry, and incidents belong to a tenant. Hosting providers would create one tenant per customer. |
| **Snapshot** | A timestamped, checksummed capture of a domain's DNS state (SPF, DKIM, DMARC, MX, etc.). A new snapshot is only written when the posture changes. |
| **Finding** | A specific DNS validation issue attached to a snapshot. Has a `category` (spf, dkim, dmarc, mx), a `severity` (info, warning, critical), a `code` (e.g. `SPF_LOOKUP_LIMIT`), and a human-readable `message`. |
| **Incident** | A persisted intelligence-layer alert produced by `incidentd` from `correld`, `repd`, `rbld`, `anomalyd`, or `authwatchd` events. Has a status (`open`, `acknowledged`, `resolved`) and optionally AI-generated analysis. |
| **Bounce class** | Mail Captain's classification of a permanent delivery failure reason: e.g. `auth` (authentication/policy rejection), `reputation` (spam/RBL), `content`, `quota`, etc. |
| **DNSBL / RBL** | DNS-based blocklist. A publicly-queryable list of IP addresses known to send spam. Being listed causes deliverability to degrade across major providers. |
| **FBL** | Feedback Loop. ISPs (e.g. Yahoo, Microsoft) forward spam complaint reports (ARF format) to the sending domain's `abuse@` address when recipients hit the spam button. |
| **DMARC** | Domain-based Message Authentication, Reporting, and Conformance. A policy published in DNS that tells receivers what to do when SPF or DKIM fail, and where to send aggregate reports. |
| **DKIM selector** | A label used to identify which DKIM key signed a message. Published at `<selector>._domainkey.example.com`. |
| **SPF include endpoint** | An `include:` mechanism your tenants reference in their SPF records to authorize your relay's IPs without listing them directly. |
| **mxctl** | The operator CLI. Baked into the Go service image. Used for migrations, seeding, API key creation, user management, and domain management. Run via `docker compose run --rm apid /usr/local/bin/mxctl …`. |
| **JetStream** | NATS's durable streaming layer. Mail Captain uses JetStream streams (`SMTP`, `DNS`, `REPUTATION`, `AI`) for at-least-once event delivery between services. |
| **Telemetry** | Structured per-message metadata emitted by `telemetryd` from Postfix maillogs. Includes outcome, SMTP code, provider, relay IP, bounce class, TLS info, queue timing. Never includes message body or subject. |
