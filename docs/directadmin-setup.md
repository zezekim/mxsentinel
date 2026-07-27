# Connect a DirectAdmin server to MX Sentinel

Relay all outbound mail from a DirectAdmin server through MX Sentinel. DirectAdmin keeps
DKIM-signing each domain; MX Sentinel relays across your reputation-managed IP pool,
authenticates it, and gives you per-message visibility. This is the DirectAdmin counterpart
to [cpanel-setup.md](cpanel-setup.md) — DirectAdmin uses **Exim**, so it's an authenticated
Exim smarthost.

**No MX Sentinel software runs on the DirectAdmin box.** It just routes outbound through the
relay on submission (587 + STARTTLS + AUTH); the relay observes every message, so it appears
in **Message Explorer** automatically.

## Connection settings

| Setting | Value |
|---------|-------|
| Host | your relay hostname (e.g. `relay.example.com`) |
| Port | `587` (submission) |
| Encryption | STARTTLS (required) |
| Auth | AUTH LOGIN / PLAIN |
| Username / Password | the SMTP user you create in step 1 |

## Step 1 — Create one SMTP user

The whole DirectAdmin server authenticates with a single credential. Dashboard → **SMTP
Users** → add a user (a full address like `directadmin@relay.example.com` is conventional).

CLI equivalent (one-shot container on the relay):

```bash
cd /opt/mxsentinel
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
  --env-file deploy/.env --profile app run --rm apid \
  /usr/local/bin/mxctl smtp-user create \
    --tenant <slug> --username directadmin@relay.example.com --password 'a-strong-password'
```

## Step 2 — Point DirectAdmin's Exim at the relay

DirectAdmin regenerates `/etc/exim.conf` on updates, so custom routing must go in its
**update-safe include files** (`.pre.conf` / `.post.conf`), which DirectAdmin includes into
the generated config. Create these three files, then restart Exim.

**`/etc/exim.routers.pre.conf`**
```
smart_route:
    driver = manualroute
    ignore_target_hosts = 127.0.0.0/8
    condition = ${if !match_domain{$domain}{+local_domains}}
    transport = auth_relay
    self = pass
    route_list = * relay.example.com::587
    no_more
```

**`/etc/exim.transports.pre.conf`**
```
auth_relay:
    driver = smtp
    port = 25
    message_linelength_limit = 52428800
    hosts_require_auth = *
    hosts_require_tls = *
    headers_add = "${if def:authenticated_id{X-Authenticated-Id: ${authenticated_id}}}"
    interface = <; ${if exists{/etc/virtual/domainips}{${lookup{$sender_address_domain}lsearch*{/etc/virtual/domainips}}}}
    helo_data = ${if exists{/etc/virtual/helo_data}{${lookup{$sending_ip_address}iplsearch{/etc/virtual/helo_data}{$value}{$primary_hostname}}}{$primary_hostname}}
    hosts_try_chunking =
    hosts_try_fastopen =
```

**`/etc/exim.authenticators.post.conf`**
```
auth_login:
    driver = plaintext
    public_name = LOGIN
    hide client_send = : directadmin@relay.example.com : a-strong-password
```

Then:
```bash
exim -bV && systemctl restart exim
```

### Two things that bite (learned the hard way)

- **Don't use DirectAdmin's stock `FORCED_MX_DNS_CHECK` smarthost condition.** That macro is
  not defined in all Exim builds (e.g. 4.99.x). If the condition can't expand, Exim logs
  `MAIN PANIC ... unrecognised boolean value "FORCED_MX_DNS_CHECK"`, **skips the router**, and
  mail goes out directly instead of through the relay. The `condition` above avoids the macro:
  it routes any non-local domain out through the smarthost.

- **`client_send` must start with `: ` (an empty initial response).** The format is
  `= : USERNAME : PASSWORD` — colon, user, colon, pass. If the leading colon is missing, Exim
  sends the username as the AUTH-LOGIN *initial* response, the password as the *username*, and
  then has nothing for the password prompt — the log shows it reach `334 UGFzc3dvcmQ6`
  (base64 `Password:`) and **defer**. The leading colon fixes the off-by-one.

## Step 3 — Verify routing (before sending)

```bash
exim -bt someone@gmail.com
```
Must show `router = smart_route, transport = auth_relay` and the relay host. If it shows
`lookuphost`/`dnslookup` → `remote_smtp`, the router isn't matching (check `exim -bV` for a
PANIC, and confirm `/etc/exim.conf` has `.include_if_exists` lines for the three files).

## Step 4 — Test a real send

```bash
swaks --to you@gmail.com --from test@a-domain-on-this-server.com \
  --server relay.example.com --port 587 --tls \
  --auth LOGIN --auth-user directadmin@relay.example.com --auth-password 'a-strong-password'
```
Watch it end-to-end (forces an immediate attempt with the SMTP dialogue printed):
```bash
exim -bp                                  # find the queued message id
exim -v -M <message-id>                   # force delivery, show the conversation
```
Success looks like `235 ... Authentication successful` → `250 ... queued as ...`, and the log
line `=> ... R=smart_route T=auth_relay ... A=auth_login C="250 ..."`.

> A `SSL verify error: self signed certificate` in the log is **not** fatal — the handshake
> completes and delivery proceeds (`CV=no`). Install a real cert on the relay
> ([smarthost.md §6](smarthost.md)) to silence it and satisfy stricter clients.

## Step 5 — DKIM, SPF, DMARC

- **DKIM** — DirectAdmin signs each domain when DKIM is enabled (modern DA: on by default).
  The signature passes through the relay untouched → `dmarc=pass` via DKIM for the customer's
  own domain. Nothing to do on the relay.
- **SPF** — every sending domain must authorize the relay, or receivers still see `spf=fail`
  despite the clean relay path. Add the relay's include to each domain:
  ```
  example.com.  IN TXT  "v=spf1 include:spf.example.net ~all"
  ```
  To do this across **all** domains on the server at once, use
  [`deploy/scripts/da-spf-include.py`](../deploy/scripts/da-spf-include.py) (dry-run by
  default; edits the BIND zones under `/var/named`, backs up, bumps SOA serials, reloads).
- **DMARC** — start `p=none`, tighten once SPF/DKIM align. `dnsd` validates all of these —
  watch the **Domains** page for findings.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `exim -bt` shows `dnslookup`/`remote_smtp`, not `smart_route` | Router not matching or not loaded. Check `exim -bV` for a PANIC (usually the `FORCED_MX_DNS_CHECK` macro), and that `/etc/exim.conf` includes the three files. |
| Delivery defers at `334 UGFzc3dvcmQ6` | `client_send` off-by-one — add the leading `: ` (empty initial response). |
| `535 authentication failed` | Wrong/missing relay SMTP user. Confirm it exists: `mxctl smtp-user list --tenant <slug>`. |
| Tested to a domain hosted on the same server | Local delivery — never routes out. Test to an external address. |
| `SSL verify error: self signed certificate` | Non-fatal (delivery still proceeds). Install a real relay cert — [smarthost.md §6](smarthost.md). |
