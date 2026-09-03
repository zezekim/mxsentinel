# Webmail autologin (Roundcube)

One click on the **Webmail** button in *SMTP Users* opens Roundcube already signed in as
that credential — the cPanel-style handoff, without the operator ever handling the
password.

This document covers what the feature needs, how the handoff is secured, and how to turn it
on. Read [What this changes](#what-this-changes) first: enabling webmail turns relay
submission credentials into mailbox accounts, which is a real change in what the relay is.

---

## What this changes

MX Sentinel's `smtp_users` are, by default, **SASL submission credentials only**. The relay's
Dovecot is a passdb with `protocols =` empty — no IMAP, no POP, no mail storage. A customer's
actual mailboxes live on their own mail server; the relay just carries their outbound mail.

Turning webmail on adds two things that did not exist before:

1. **IMAP mailboxes.** `--wire-webmail` gives the relay's Dovecot an `imap` protocol and a
   maildir store, so every enabled SMTP user has a mailbox to log into.
2. **A reversible copy of each password.** IMAP cannot authenticate against a bcrypt hash,
   so apid stores an AES-256-GCM sealed copy of the password in `smtp_users.password_enc`
   and unseals it only at the moment of a handoff.

Point 2 is a deliberate departure from the "hash only, never read back" posture in
`internal/store/postgres/smtp_users.go`. The mitigations are that the ciphertext is written
**only** when `MXS_ENCRYPTION_KEY` is configured (no key ⇒ column stays NULL and webmail is
simply unavailable — apid never falls back to storing plaintext), the key lives outside the
database, and unsealing happens in exactly one code path guarded by a shared secret.

**Mailboxes start empty.** Provisioning a mailbox does not route any mail into it. The relay
still forwards mail onward as before, and it should: making the relay authoritative for a
domain it is supposed to forward would silently swallow that domain's mail. Inbound delivery
is opt-in per domain via `WEBMAIL_DOMAINS` — see [Inbound delivery](#inbound-delivery-opt-in).

---

## The handoff

```
 dashboard ──POST /v1/smtp-users/{id}/webmail-session──▶ apid        admin scope, audited
           ◀── { url: <roundcube>/?_mxs_autologin=mxw_… } ──┘        token lives ~60s
 browser   ──GET that url────────────────────────────▶ Roundcube    mxs_autologin plugin
 Roundcube ──POST /v1/webmail/redeem { token }───────▶ apid         X-MXS-Webmail-Secret
           ◀── { username, password, imap_host, … } ──┘             token consumed here
 Roundcube ──IMAP LOGIN──────────────────────────────▶ Dovecot      mailbox opens
```

Security properties:

| Property | How |
|---|---|
| Token is unguessable | 160-bit secret, `mxw_<8 hex>_<40 hex>` — a distinct scheme from API tokens (`mxs_`) and share links (`mxt_`), so none is ever accepted in another's place |
| Token is stored only as a hash | SHA-256; the row keeps the non-secret `token_prefix` for lookup |
| Token is single-use | Redemption is an `UPDATE … WHERE used_at IS NULL AND expires_at > now()`; a replay matches no row |
| Token is short-lived | `MXS_WEBMAIL_TOKENTTL`, default 60s — it is consumed by one redirect |
| A leaked token is not enough | `/v1/webmail/redeem` also requires `X-MXS-Webmail-Secret`, compared in constant time |
| Minting is privileged | Admin scope — the same bar as resetting the credential's password — and recorded in the audit log |
| Disabled users cannot open webmail | Checked at mint *and* re-checked inside the redeem statement |
| Nothing sensitive is logged | apid logs the token *prefix*, never the token or password; the plugin logs neither |

Suspending an SMTP user therefore closes the webmail door for new sessions immediately. It
does **not** kill an already-open Roundcube session — end that in Roundcube (or restart it)
if you are locking out a compromised credential.

---

## Setup

### 1. Give SMTP users mailboxes

On the relay host:

```bash
sudo bash deploy/install.sh --wire-webmail
```

This adds `imap lmtp` to the relay's Dovecot, creates the unprivileged `vmail` owner and the
maildir root (`/var/mail/vhosts/<login>/Maildir`), and binds the IMAP listener to loopback
plus the Docker bridge (`127.0.0.1,172.17.0.1` by default — override with
`WEBMAIL_IMAP_LISTEN`). Port 143 is never opened in the firewall: its only client is the
Roundcube container on the same host. SASL submission keeps working exactly as before.

### 2. Configure apid

In `deploy/.env`:

```bash
MXS_ENCRYPTION_KEY=$(openssl rand -hex 32)        # if not already set — required
MXS_WEBMAIL_BASEURL=https://sentinel.example.com/roundcube
MXS_WEBMAIL_PLUGINSECRET=$(openssl rand -hex 32)
MXS_WEBMAIL_IMAPHOST=host.docker.internal          # as resolved from ROUNDCUBE's network
MXS_WEBMAIL_IMAPPORT=143
MXS_WEBMAIL_IMAPTLS=starttls                       # starttls | tls | none
```

`MXS_WEBMAIL_IMAPHOST` is resolved by Roundcube, not by apid. With Dovecot on the host and
Roundcube in Docker that is `host.docker.internal` (or the bridge gateway, `172.17.0.1`).
apid renders it into the form Roundcube expects — `tls://host:143` for STARTTLS,
`ssl://host:993` for implicit TLS.

Restart apid. It logs `webmail autologin enabled` at startup, or a warning if only one of the
two required settings is present.

### 3. Install the Roundcube plugin

The Roundcube in this deployment is the `dmarc-roundcube` container from
`/opt/dmarcparser/compose.yaml`. Mount the plugin and enable it:

```yaml
  roundcube:
    volumes:
      - /opt/mxsentinel/deploy/roundcube/mxs_autologin:/var/www/html/plugins/mxs_autologin:ro
      - ./roundcube/custom.php:/var/roundcube/config/custom.php:ro
```

In the plugin directory, copy `config.inc.php.dist` to `config.inc.php` and set:

```php
$config['mxs_autologin_api']    = 'http://apid:8080';   // container-to-container
$config['mxs_autologin_secret'] = '…';                  // = MXS_WEBMAIL_PLUGINSECRET
```

Add it to Roundcube's plugin list (in `custom.php` for the containerised install):

```php
$config['plugins'][] = 'mxs_autologin';
```

If Dovecot is using the default snakeoil certificate, Roundcube must be told not to verify
it — otherwise STARTTLS fails:

```php
$config['imap_conn_options'] = [
    'ssl' => ['verify_peer' => false, 'verify_peer_name' => false],
];
```

Install a real certificate instead where you can.

### 4. Seal the existing credentials

`password_enc` can only be written when a password is known in the clear, which is at
creation or reset. Every SMTP user created **before** webmail was configured therefore has
no sealed copy, and its **Webmail** button stays hidden until you reset its password (SMTP
Users → *Reset password*). Remember to update the smarthost config with the new password.

New users get theirs automatically.

---

## Inbound delivery (opt-in)

Mailboxes exist but receive nothing until you make the relay authoritative for a domain.
That is deliberate — see [What this changes](#what-this-changes). To opt a domain in, set
this in `deploy/.env` and re-run `--wire-webmail`:

```bash
WEBMAIL_DOMAINS=mail.example.com,news.example.net
```

This sets `virtual_mailbox_domains` to that list, routes it to Dovecot over LMTP, and
resolves valid recipients from `smtp_users` via a Postgres map. **Only list domains whose
mail should stop here.** A domain you are supposed to forward will have its mail delivered
into a local mailbox instead of reaching its real destination.

---

## API

### `POST /v1/smtp-users/{id}/webmail-session` — scope: `admin`

Mints a one-shot autologin URL. Body is ignored.

```json
{
  "username": "mailer@send.example.com",
  "url": "https://sentinel.example.com/roundcube/?_mxs_autologin=mxw_1a2b3c4d_…",
  "token": "mxw_1a2b3c4d_…",
  "expires_at": "2026-08-21T17:41:07Z"
}
```

| Status | Meaning |
|---|---|
| `201` | Session minted |
| `404 not_found` | No such SMTP user in this tenant |
| `409 disabled` | The user is disabled |
| `409 no_webmail_credential` | No sealed password — reset the user's password |
| `503 not_configured` | `MXS_WEBMAIL_BASEURL` / `MXS_WEBMAIL_PLUGINSECRET` unset |

### `POST /v1/webmail/redeem` — no tenant auth; `X-MXS-Webmail-Secret` required

Called only by the Roundcube plugin. Consumes the token and returns the IMAP credentials.

```json
{ "token": "mxw_1a2b3c4d_…" }
→
{ "username": "mailer@send.example.com", "password": "…", "imap_host": "tls://host.docker.internal", "imap_port": 143 }
```

Returns `401` for a bad secret and `401 invalid_token` for anything else wrong with the
token — expired, already redeemed, unknown, or belonging to a since-disabled user. The two
cases are deliberately indistinguishable to a caller.

`GET /v1/smtp-users` gains a `webmail_available` field per user: true when the feature is
configured *and* that user has a sealed password. The dashboard uses it to decide whether to
show the button.

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| No **Webmail** button on any row | apid has no `MXS_WEBMAIL_BASEURL`/`MXS_WEBMAIL_PLUGINSECRET`, or was not restarted |
| No button on *some* rows | Those users predate the feature — reset their password |
| Button opens the Roundcube login form | Redeem failed. Check Roundcube's `mxs_autologin` log: usually a secret mismatch or apid unreachable from the container |
| `Login failed` after redeem | IMAP rejected the credentials — check Dovecot's log; a certificate error here needs `imap_conn_options` (step 3) |
| `503 not_configured` from the API | Only one of the two required settings is present; apid logs `webmail autologin half-configured` at startup |
| Mailbox is empty | Expected unless the domain is opted into `WEBMAIL_DOMAINS` |

## Files

| Path | Role |
|---|---|
| `internal/api/handlers_webmail.go` | Mint + redeem handlers, IMAP host rendering |
| `internal/api/token.go` | `mxw_` token scheme |
| `internal/store/postgres/smtp_user_webmail.go` | Token rows, single-use redemption |
| `migrations/postgres/00028_smtp_user_webmail.sql` | `password_enc` + `smtp_user_webmail_tokens` |
| `deploy/roundcube/mxs_autologin/` | The Roundcube plugin |
| `deploy/install.sh` (`provision_dovecot`) | Dovecot IMAP + maildir store |
| `web/app/smtp-users/page.tsx` | The **Webmail** button |
