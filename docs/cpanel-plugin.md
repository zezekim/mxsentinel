# cPanel / WHM Plugin — Outbound Relay Installer

A plugin you install **on your WHM/cPanel server** that points the server's outbound mail
at the MX Sentinel relay — the same idea as the MailChannels cPanel plugin. From WHM you
provision a sending credential and enable/disable relay routing with one click; the plugin
rewrites Exim's configuration so all outbound mail egresses through the relay's
reputation-managed, DKIM-signing IPs and is observed by MX Sentinel.

It ships two surfaces from one binary:

| Surface | Who | What |
|---|---|---|
| **WHM admin tool** | server operator (root) | provision the relay credential, **Enable/Disable** server-wide relay routing, **Test** the path, view the **DNS** records to publish |
| **cPanel end-user status** | each hosting account | read-only: that account's domains' SPF/DKIM/DMARC health, incidents, and DNS to publish |

What "Enable" does: all outbound mail for non-local domains is routed through the relay on
port 587 with STARTTLS + AUTH. Local mail delivery is unaffected. "Disable" returns the
server to direct delivery. See `docs/smarthost.md` for the relay contract.

## How enabling rewrites Exim (and why it's safe)

The WHM tool edits cPanel's rebuild-safe overlay `/etc/exim.conf.local`, inserting three
**sentinel-wrapped** blocks under cPanel's section markers:

- a `manualroute` **router** under `@POSTMAILCOUNT@` — placed there (not `@ROUTERSTART@`)
  so cPanel's "Max hourly emails per domain" limits still apply before mail is relayed;
- an smtp **transport** under `@TRANSPORTSTART@` (`hosts_require_auth` + `hosts_require_tls`);
- a plaintext **authenticator** under `@AUTH@` carrying the SASL credential.

Every change is guarded:

1. The section markers must exist, or the tool **refuses** (it never guesses the layout).
2. `/etc/exim.conf.local` is **backed up** (timestamped) before any write.
3. cPanel rebuilds via `/usr/local/cpanel/scripts/buildeximconf`, then the result is
   **validated with `exim -bV`**.
4. If the rebuild or validation fails, the tool **restores the backup and rebuilds** — it
   never leaves Exim in a broken state.
5. `restartsrv_exim` applies the change.

Manual rollback is always possible: delete the `# BEGIN mxsentinel … # END mxsentinel`
blocks from `/etc/exim.conf.local` and run `buildeximconf && restartsrv_exim`.

## Security model

The privileged work (provision credential, rewrite Exim) runs in the **WHM CGI as root**,
which is inherently admin-only because it lives under the whostmgr docroot — cpsrvd only
serves it to an authenticated WHM operator. The API token is read directly from the
root-only config; it never reaches user space.

The **cPanel end-user status page** must not see the token or other accounts' data, so it
runs as the account's uid and only talks to a small **root broker daemon** over a unix
socket. The broker scopes every response by the connection's **kernel-reported peer uid**
(`SO_PEERCRED`) — unforgeable by a user process — mapping uid → cPanel account → its own
domains.

```
WHM admin ─cpsrvd(root)→ index.cgi  (MXS_PLUGIN_MODE=whm) ─reads token, rewrites Exim, calls apid
cPanel user ─cpsrvd(uid)→ index.live.cgi ─unix socket→ broker(root) ─SO_PEERCRED→ scope→ apid (read-only)
```

## Components

```
cmd/cpanel-plugin/          one binary: serve (broker) | cgi (whm admin | user status)
internal/cpanelplugin/
  relay.go      provision credential, enable/disable, test, status, DNS records
  exim.go       /etc/exim.conf.local overlay edit + backup + validate + rollback (+ _test.go)
  upstream.go   apid client (settings, smtp-users, domains, incidents)
  broker.go     root daemon for the user status page (peer-uid scoped, read-only)
  cgi.go        WHM admin handler + user proxy handler
  scope.go      uid → cPanel account → owned domains
  assets/whm.html    WHM admin UI        assets/index.html   user status UI
plugins/cpanel/
  whm/{mxsentinel.conf,index.cgi}      WHM AppConfig + entrypoint (sets MXS_PLUGIN_MODE=whm)
  user/{index.live.cgi,dynamicui_*}    cPanel user entrypoint + menu
  systemd/mxsentinel-plugin.service    broker unit
  install.sh / uninstall.sh            installers
```

## Prerequisites

- A WHM/cPanel server (Jupiter theme; CloudLinux/AlmaLinux/CentOS), reachable to your apid.
- An MX Sentinel **relay** deployed, with **Relay Host** set in MX Sentinel → Settings
  (`docs/deploy-relay.md`). The WHM tool refuses to enable routing if no relay host is set.
- A tenant API token with **admin** scope (it provisions the relay SMTP user):
  `mxctl apikey create --tenant <slug> --scopes admin --name cpanel-plugin`.
- The plugin binary built for the server's OS/arch (`make build-cpanel-plugin`).

## Install

```bash
make build-cpanel-plugin                 # → bin/mxsentinel-plugin (linux/amd64)
# copy bin/mxsentinel-plugin + plugins/cpanel/ to the server, then as root:
cd plugins/cpanel
./install.sh --bin ./mxsentinel-plugin \
  --api-base https://sentinel.example.com \
  --token mxs_xxxxxxxx_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

The installer drops the binary, writes `/etc/mxsentinel/plugin.conf` (0600), installs +
starts the broker service, registers the WHM tool and the cPanel user page, and (on
CloudLinux) exposes the broker socket inside user cages. Flags: `--whm-only`,
`--user-only`, `--no-verify-ssl`, `--keep-config`.

Then in WHM → **MX Sentinel**: provisioning happens automatically on first **Enable**.

## Verify

```bash
systemctl status mxsentinel-plugin                 # broker (user status page)
# In WHM → MX Sentinel: click Enable, then:
grep -n 'mxsentinel' /etc/exim.conf.local          # the three managed blocks
exim -bP routers | grep mxsentinel_smarthost       # router present
exim -bV                                            # config parses cleanly
```

Send a message from an account and confirm in MX Sentinel's **Message Explorer** that it
egressed via the relay (`Authentication-Results: spf=pass dkim=pass dmarc=pass`). The
WHM **Test** button should report `Authenticated successfully`. **Disable** removes the
blocks and returns to direct delivery.

## CloudLinux / CageFS

CageFS jails users, so the broker socket under `/run` isn't visible to the user status
page by default. `install.sh` adds `/run/mxsentinel-plugin` to `/etc/cagefs/cagefs.mp` and
runs `cagefsctl --remount-all`. If the user page reports "broker unavailable," apply that
manually. (The WHM tool does not use the socket, so it is unaffected.)

## Limitations / follow-ups

- **Server-wide routing, single server credential** (chosen). All outbound mail uses one
  SASL user; per-account credentials + sender-keyed auth is a future option.
- **TLS to the relay** is encrypted but not certificate-verified (the relay's default is a
  snakeoil cert). Install a real cert and tighten per `docs/smarthost.md §6`.
- The SASL password is stored inline in `/etc/exim.conf.local` (root/mail readable, like
  Postfix `sasl_passwd`) and in `/etc/mxsentinel/relay-state.json` (root 0600).
- Reseller (non-root) WHM logins are not the target; the tool is for the root operator.
