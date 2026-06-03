# Smarthost Configuration — Relaying Through MX Sentinel

Point a sending system (cPanel/WHM, a standalone Exim or Postfix, or an application)
at the MX Sentinel Postfix relay so all outbound mail egresses from the relay's
reputation-managed IPs, signed with DKIM and observed by MX Sentinel.

This guide covers the **client (smarthost) side**: creating submission credentials,
publishing the right DNS, and configuring common senders. For standing the relay up on
the VPS, see [deploy-relay.md](deploy-relay.md).

---

## 1. The submission endpoint

| Setting | Value |
|---------|-------|
| Host | your relay hostname (e.g. `relay.example.com`) |
| Port | `587` (submission) |
| Encryption | `STARTTLS` (required — AUTH is only offered after TLS) |
| Auth | `AUTH PLAIN` / `AUTH LOGIN` |
| Username | the SMTP user you create (below) |
| Password | the password you set for that user |

Port `25` is **not** for submission — it is trusted-network/loopback only and does not
accept SASL logins. Always submit on `587`.

> **TLS certificate:** a fresh install configures submission with Postfix's default
> self-signed (snakeoil) certificate so it works out of the box. Clients that verify
> certificates will reject it. For production, install a real certificate for the relay
> hostname and require encryption — see [§6](#6-production-tls-certificate).

---

## 2. Create an SMTP submission user

Each smarthost authenticates with its own credential. Create one per sending system (or
per customer) so you can disable a single compromised credential without affecting others.

### Option A — Dashboard (recommended)

1. Open the dashboard and go to **SMTP Users**.
2. Enter a **username** (a full address such as `mailer@send.example.com` is conventional
   and keeps usernames globally unique), a **password** (≥ 8 characters), and optionally
   the associated **sending domain**.
3. Click **Add user**. The credential is active immediately — the relay authenticates
   against it on the next login.

You can **disable**, **reset the password**, or **delete** a user from the same page.
Disabling or deleting takes effect on the next authentication attempt.

### Option B — CLI

Run inside a one-shot `apid` container (mxctl is baked into the image):

```bash
docker compose \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.prod.yml \
  --env-file deploy/.env \
  run --rm apid \
  /usr/local/bin/mxctl smtp-user create \
    --tenant demo \
    --username mailer@send.example.com \
    --password 'a-strong-password' \
    --domain send.example.com
```

List or delete:

```bash
mxctl smtp-user list   --tenant demo
mxctl smtp-user delete --tenant demo --username mailer@send.example.com
```

---

## 3. Publish DNS so mail authenticates

Open **Settings** in the dashboard to set your tenant defaults; the page renders the exact
records to publish.

- **SPF** — authorize the relay for your envelope-sender domain. If your relay provider
  publishes a managed SPF include (configured as the **SPF include endpoint** in Settings,
  e.g. `spf.squidix.net`):

  ```
  example.com.  IN TXT  "v=spf1 include:spf.squidix.net ~all"
  ```

  If you instead list the relay IPs directly, prefer flat `ip4:` mechanisms to stay under
  the 10-lookup limit `dnsd` enforces (see [deploy-relay.md §6.2](deploy-relay.md)).

- **DKIM** — the relay signs with the selector shown in Settings (default `mxs`). Publish
  the public key the installer prints (or `cat /etc/opendkim/keys/<domain>/<selector>.txt`)
  at `<selector>._domainkey.example.com`.

- **DMARC** — start at `p=none` and tighten once SPF/DKIM align:

  ```
  _dmarc.example.com.  IN TXT  "v=DMARC1; p=none; rua=mailto:dmarc@example.com"
  ```

MX Sentinel's `dnsd` validates and snapshots all of these — watch the **Domains** page for
findings (missing SPF, lookup-limit, misaligned DMARC, etc.).

---

## 4. Configure the sender

Replace `relay.example.com`, the username, and the password with your values in every
example below.

### cPanel / WHM (Exim)

WHM → **Exim Configuration Manager** is the supported path, but the simplest per-account
route is a SmartHost. In WHM → **Service Configuration → Exim Configuration Manager →
Advanced Editor**, add a router + transport, or use the *Basic Editor*'s "smarthost"
section. A minimal Advanced Editor config:

```
# Section: ROUTERS START
send_via_relay:
  driver = manualroute
  domains = ! +local_domains
  transport = relay_smtp
  route_list = * relay.example.com::587
  no_more

# Section: TRANSPORTS START
relay_smtp:
  driver = smtp
  hosts_require_auth = relay.example.com
  hosts_require_tls = relay.example.com

# Section: AUTHS START
relay_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : mailer@send.example.com : a-strong-password
```

After saving, restart Exim. Test from the server with `swaks` (§5).

### Standalone Exim

```
# /etc/exim4/exim4.conf.localmacros (or your split-config equivalent)
# Router
smarthost:
  driver = manualroute
  domains = ! +local_domains
  transport = remote_smtp_smarthost
  route_list = * relay.example.com::587
  no_more

# Transport
remote_smtp_smarthost:
  driver = smtp
  hosts_require_auth = relay.example.com
  hosts_require_tls  = relay.example.com

# Authenticator
smarthost_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : mailer@send.example.com : a-strong-password
```

### Postfix (as a client relaying to MX Sentinel)

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
[relay.example.com]:587    mailer@send.example.com:a-strong-password
```

```bash
sudo postmap /etc/postfix/sasl_passwd
sudo chmod 600 /etc/postfix/sasl_passwd /etc/postfix/sasl_passwd.db
sudo systemctl reload postfix
```

### Application SMTP libraries

Use these settings with any SMTP client (Nodemailer, PHPMailer, Python `smtplib`,
SwiftMailer, etc.):

```
host:       relay.example.com
port:       587
secure:     false          # STARTTLS, not implicit TLS
requireTLS: true
auth.user:  mailer@send.example.com
auth.pass:  a-strong-password
```

---

## 5. Test the path

From any host that can reach the relay:

```bash
swaks \
  --to you@gmail.com \
  --from mailer@send.example.com \
  --server relay.example.com \
  --port 587 \
  --tls \
  --auth LOGIN \
  --auth-user mailer@send.example.com \
  --auth-password 'a-strong-password'
```

Expect `235 2.7.0 Authentication successful` then `250` on the final dot. Then:

- Watch the relay log: `tail -f /var/log/mail.log` — `postfix/submission` accepts the
  message, `opendkim` adds a DKIM-Signature, `postfix/smtp` delivers it.
- In the dashboard, open **Message Explorer** and find the message (outcome, provider,
  SMTP code).
- Check the received message's `Authentication-Results` header for `spf=pass`,
  `dkim=pass`, `dmarc=pass`.

---

## 6. Production TLS certificate

The installer leaves submission on the snakeoil cert. Issue a real one and point Postfix
at it:

```bash
sudo apt-get install -y certbot
sudo certbot certonly --standalone -d relay.example.com \
  --pre-hook "systemctl stop postfix" \
  --post-hook "systemctl start postfix"
```

```bash
sudo postconf -e \
  "smtpd_tls_cert_file = /etc/letsencrypt/live/relay.example.com/fullchain.pem" \
  "smtpd_tls_key_file  = /etc/letsencrypt/live/relay.example.com/privkey.pem"
# Require TLS on submission now that the cert is valid:
sudo postconf -P "submission/inet/smtpd_tls_security_level=encrypt"
sudo systemctl reload postfix
```

---

## 7. Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `535 5.7.8 Error: authentication failed` | Wrong username/password, or the user is **disabled**/deleted. Check the SMTP Users page. The username must match exactly (it is the SASL login). |
| `530 5.7.0 Must issue a STARTTLS command first` | The client tried to AUTH before STARTTLS. Enable STARTTLS/`requireTLS` on the client. The relay never offers AUTH in cleartext (`smtpd_tls_auth_only = yes`). |
| TLS handshake / certificate errors | The client is verifying the snakeoil cert. Install a real cert (§6) or, for testing only, disable cert verification on the client. |
| `554 5.7.1 … Relay access denied` | Authenticated correctly but submitting on port 25 instead of 587, or auth silently failed. Use port 587 with AUTH. |
| Mail accepted but `dmarc=fail` at the receiver | SPF/DKIM not aligned with the From domain. Re-check the records in §3 and the **Domains** page findings. |
| Auth works but Dovecot errors in `/var/log/dovecot.log` | Dovecot can't reach Postgres. Confirm Postgres is published on `127.0.0.1:5432` and the DSN in `/etc/dovecot/dovecot-sql.conf.ext` matches `deploy/.env`. |

---

## How it works

The relay does not store passwords in a flat file. Postfix's submission service delegates
SASL to Dovecot (`smtpd_sasl_type = dovecot`), and Dovecot's SQL passdb queries the
`smtp_users` table in Postgres directly:

```
smarthost ──AUTH (587, TLS)──▶ Postfix submission
                                    │ smtpd_sasl_path = private/auth
                                    ▼
                               Dovecot auth ──SQL──▶ Postgres.smtp_users
                                                      (username, bcrypt hash, enabled)
```

So a credential created in the dashboard or via `mxctl` is usable on the relay
immediately — there is no file to regenerate or service to reload. Passwords are stored
as bcrypt hashes and verified by Dovecot as `BLF-CRYPT`; the plaintext is never persisted.
