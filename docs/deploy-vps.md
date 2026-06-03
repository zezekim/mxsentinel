# MX Sentinel — Single-VPS Production Deployment

This runbook deploys the full MX Sentinel platform on one Linux VPS using Docker Compose.
Everything is containerised; a Caddy reverse proxy handles TLS automatically. Follow the
steps in order. Commands are designed to be copy-pasteable.

---

## 1. Architecture Overview

```
                         Internet
                            │
                     ┌──────▼──────┐
                     │  Caddy :443 │  (Let's Encrypt TLS, automatic)
                     │  (Caddy :80 │   redirects to 443)
                     └──────┬──────┘
                            │
              ┌─────────────┴──────────────┐
              │  same origin / no CORS     │
              ▼                            ▼
       /v1/* → apid:8080          /* → dashboard:3000
       (REST API)                 (Next.js UI)

  ─────────────── Docker network (internal) ────────────────

  postgres  clickhouse  redis  nats  minio
  (bound to 127.0.0.1 only — not reachable from the internet)

  dnsd  correld  incidentd  repd  aid  dmarcd
  (no public ports)

  ─────────────────── Host (optional) ─────────────────────

  Ollama (ollama serve, port 11434)
  Reached by the aid container via host.docker.internal
  (or explicit host IP — see §7)
```

All backing-service ports are bound to `127.0.0.1` in the base compose file so they are
never exposed to the public internet. Only Caddy's 80 and 443 are published.

---

## 2. Prerequisites

| Requirement | Notes |
|---|---|
| VPS | 2+ vCPU / 4 GB+ RAM recommended; Ubuntu 22.04 LTS or Debian 12 |
| Docker Engine + Compose plugin | `docker compose version` must succeed (v2.x) |
| Domain name | An A record pointing to the VPS public IP, e.g. `sentinel.example.com` |
| Ports open | 22 (SSH), 80 (HTTP — required for ACME challenge), 443 (HTTPS) |
| git | To clone the repository |

Let's Encrypt will attempt an HTTP-01 challenge on port 80. Both 80 and 443 must be
reachable from the public internet before the first launch, or TLS certificate issuance
will fail.

---

## 3. Provision the Server

### 3.1 Install Docker Engine

```bash
# Ubuntu / Debian
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER
# Log out and back in so the group change takes effect
newgrp docker
```

Verify:

```bash
docker compose version
# Docker Compose version v2.x.x
```

### 3.2 Clone the Repository

```bash
git clone https://github.com/<your-org>/mxsentinel.git
cd mxsentinel
```

---

## 4. Configure Secrets

### 4.1 Create your production env file

```bash
cp deploy/.env.prod.example deploy/.env
```

Open `deploy/.env` in an editor and fill in every value:

```bash
nano deploy/.env
```

The file contains the following variables. Set each one to a strong, unique value:

```
# Postgres
PG_USER=mxsentinel
PG_PASSWORD=<strong-random-password>
PG_DB=mxsentinel

# MinIO object storage
MINIO_ROOT_USER=mxsadmin
MINIO_ROOT_PASSWORD=<strong-random-password>

# ClickHouse
CLICKHOUSE_USER=default
CLICKHOUSE_PASSWORD=<strong-random-password>

# Caddy / TLS
MXS_DOMAIN=sentinel.example.com
MXS_ACME_EMAIL=ops@example.com

# AI layer (optional — see §7)
MXS_AI_ENDPOINT=http://host.docker.internal:11434/v1
MXS_AI_MODEL=llama3
```

Guidelines for secrets:

- Use at least 32 random characters for passwords (e.g. `openssl rand -hex 32`).
- `MXS_DOMAIN` must exactly match the domain whose A record points at this server.
- `MXS_ACME_EMAIL` is used by Let's Encrypt for expiry notices — use a real address.

**`deploy/.env` is listed in `.gitignore` and must never be committed to version control.**
It contains production credentials. Treat it like a private key.

---

## 5. Launch the Platform

### 5.1 Start the full stack

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  up -d --build
```

What happens on first launch:

1. Docker builds all images (Go services in distroless static images; dashboard as a
   Next.js standalone bundle with `NEXT_PUBLIC_API_BASE` baked in).
2. The `migrate` one-shot service starts, waits for Postgres and ClickHouse to be
   healthy, then applies all database migrations (`mxctl migrate up`). It exits with
   code 0 when complete.
3. Every application service (`apid`, `dnsd`, `correld`, `incidentd`, `repd`, `aid`,
   `dmarcd`, `dashboard`) waits for `migrate` to exit successfully before starting.
4. The `caddy` service (from the prod overlay) starts, reads `deploy/Caddyfile`
   (which uses `{$MXS_DOMAIN}` and `{$MXS_ACME_EMAIL}`), and obtains a Let's Encrypt
   TLS certificate via an HTTP-01 challenge. This requires DNS to resolve and ports
   80/443 to be reachable. Certificate issuance typically completes within 30–60 seconds.

### 5.2 Verify the stack is up

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  ps
```

Expected state:

- `migrate` — `Exited (0)` (one-shot, runs once)
- All other services — `running` or `healthy`
- `caddy` — `running`

Check Caddy logs to confirm TLS was issued:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  logs caddy
```

Look for a line containing `certificate obtained successfully` or similar. Once present,
`https://sentinel.example.com` is live.

---

## 6. Bootstrap Access

### 6.1 Create the first owner user

Run `mxctl` inside the `apid` container (it is baked into the image):

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  run --rm apid \
  /usr/local/bin/mxctl user create \
    --tenant demo \
    --email you@example.com \
    --password 'YourStrongPasswordHere' \
    --role owner
```

Use a strong, unique password. This account has full `admin` scope (read + write + admin).

### 6.2 Seed a demo tenant and domain (optional)

If starting fresh and you want a demo tenant with a sample domain pre-populated:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  run --rm apid \
  /usr/local/bin/mxctl seed
```

This creates the `demo` tenant and registers `example.com` as a monitored domain so the
pipeline has data to work with immediately.

### 6.3 Create an API token

For programmatic access or to configure external integrations:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  run --rm apid \
  /usr/local/bin/mxctl apikey create \
    --tenant demo \
    --scopes read,write
```

The token (`mxs_<prefix>_<secret>`) is printed **once** and only a SHA-256 hash is
stored. Copy it immediately. Use least-privilege scopes: prefer `read` for monitoring
dashboards, `read,write` for integrations that need to trigger rechecks or resolve
incidents, and `admin` only when necessary.

### 6.4 Log in

Open `https://sentinel.example.com/login` in a browser. Use the email and password
created in step 6.1. The session token (prefix `mxs_sess_`) is Redis-backed with a 24h
TTL.

---

## 7. Optional: AI Diagnostics (Ollama)

The `aid` service reads open incidents and calls a local LLM to produce a root-cause
narrative (`ai_summary`) and structured remediation steps (`ai_remediation`). Without it
the rest of the platform is fully functional — incidents are still recorded and surfaced;
they just lack AI-generated explanations.

### 7.1 Install and start Ollama on the host

```bash
# Install from https://ollama.com, then:
ollama pull llama3
ollama serve
# Ollama listens on http://localhost:11434 by default
```

### 7.2 Connectivity from the aid container

**Linux VPS:** `host.docker.internal` is not automatically available on Linux Docker.
Add an `extra_hosts` mapping in `deploy/docker-compose.prod.yml` for the `aid` service,
or set `MXS_AI_ENDPOINT` to the Docker bridge IP in `deploy/.env`:

```bash
# Find your Docker bridge IP (commonly 172.17.0.1)
ip route show | grep docker0

# Then in deploy/.env:
MXS_AI_ENDPOINT=http://172.17.0.1:11434/v1
```

Alternatively, add `--add-host host.docker.internal:host-gateway` to the `aid` service
definition in `deploy/docker-compose.prod.yml`.

**Model name:** the default is `llama3`. If you pulled a differently tagged model (e.g.
`llama3:8b`), set `MXS_AI_MODEL=llama3:8b` in `deploy/.env`.

`aid` retries harmlessly when the LLM endpoint is unreachable. Once Ollama is running,
it automatically picks up any unanalysed incidents.

---

## 8. Firewall

Configure `ufw` to allow only the ports you need:

```bash
sudo ufw allow 22/tcp    # SSH — do not lock yourself out
sudo ufw allow 80/tcp    # HTTP (ACME challenge + redirect to HTTPS)
sudo ufw allow 443/tcp   # HTTPS
sudo ufw --force enable
sudo ufw status
```

All backing-service ports (Postgres 5432, ClickHouse 9000/8123, Redis 6379, NATS
4222/8222, MinIO 9000/9001) are bound to `127.0.0.1` in `deploy/docker-compose.yml` and
are therefore not reachable from the public internet. Verify:

```bash
ss -tlnp | grep -E '5432|9000|6379|4222|8222|9001'
# Each line should show 127.0.0.1:PORT, not 0.0.0.0:PORT
```

---

## 9. Operations

### Viewing logs

```bash
# All services
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  logs -f

# Specific services (e.g. API and AI daemon)
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  logs -f apid aid
```

### Updating to a new version

```bash
git pull

docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  up -d --build
```

The `migrate` service runs automatically on each `up`, applying any new migrations before
the daemons start. Existing data in named volumes is preserved across rebuilds.

### Restarting a single service

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  restart apid
```

### PostgreSQL backup

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  exec postgres \
  pg_dump -U mxsentinel mxsentinel \
  | gzip > backup-$(date +%Y%m%d-%H%M%S).sql.gz
```

Replace `mxsentinel` (the `-U` flag value) with your `PG_USER` from `deploy/.env` if you
changed it.

### PostgreSQL restore

```bash
gunzip -c backup-YYYYMMDD-HHMMSS.sql.gz | \
  docker compose \
    -f deploy/docker-compose.yml \
    -f deploy/docker-compose.prod.yml \
    --profile app \
    --env-file deploy/.env \
    exec -T postgres \
    psql -U mxsentinel mxsentinel
```

### Stopping the stack

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  down
```

Add `--volumes` only if you want to delete all persistent data (irreversible).

---

## 10. Registry Option: Pre-built Images

Building images on the VPS on every update is straightforward but uses CPU and disk.
As an alternative, the `.github/workflows/release.yml` workflow builds all images and
pushes them to GitHub Container Registry (`ghcr.io`) whenever a `v*` tag is pushed:

```bash
git tag v1.2.3
git push origin v1.2.3
```

After the workflow completes, pull the pre-built images on the VPS instead of building:

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  pull

docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --profile app \
  --env-file deploy/.env \
  up -d
```

(Omit `--build` when using pre-built registry images.)

Log in to the GitHub registry on the VPS first if the repository is private:

```bash
echo $GITHUB_PAT | docker login ghcr.io -u <github-username> --password-stdin
```

---

## 11. Security Checklist

Before considering this deployment production-ready, verify each item:

- [ ] All secrets in `deploy/.env` are strong and unique (not the example defaults).
      Generate passwords with `openssl rand -hex 32`.
- [ ] `deploy/.env` is **not** committed to git. Confirm: `git status deploy/.env` should
      show it as ignored or untracked, never staged or committed.
- [ ] Only ports 22, 80, and 443 are publicly reachable (`sudo ufw status`).
- [ ] All backing-service ports are bound to `127.0.0.1`, not `0.0.0.0`
      (`ss -tlnp | grep -E '5432|9000|6379|4222'`).
- [ ] TLS is active and the certificate is valid. Test with:
      `curl -I https://sentinel.example.com` — expect `HTTP/2 200` and no cert warnings.
- [ ] At least one owner account has been created with `mxctl user create`.
- [ ] Any API tokens issued are scoped to the minimum necessary (`read` or `read,write`);
      `admin`-scoped tokens exist only where required.
- [ ] The SSH port (22) is protected — consider key-only auth and fail2ban.
- [ ] Regular PostgreSQL backups are scheduled (cron or a backup service).
