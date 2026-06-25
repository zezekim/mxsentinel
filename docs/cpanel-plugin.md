# cPanel / WHM Plugin

A plugin you install **on your WHM/cPanel server** that surfaces MX Sentinel's
email-infrastructure data directly inside the cPanel and WHM UIs. It is the inverse of
the `cpaneld` integration (which pulls account data *into* MX Sentinel — see
[`integrations-api.md`](integrations-api.md)): this plugin reads MX Sentinel's API and
renders deliverability, DNS-auth posture (SPF/DKIM/DMARC/MX), and incidents.

It ships two surfaces from one binary:

| Surface | Who sees it | Scope |
|---|---|---|
| **WHM admin plugin** | server admin / operator | the whole tenant: all domains, all open incidents, egress-IP reputation, provider deliverability |
| **cPanel end-user plugin** | each hosting account | only that account's domains + their incidents — never other accounts' data or tenant aggregates |

## Why a broker daemon (the security model)

The end-user plugin runs **as the cPanel account's uid**. If it held the API token,
any user could read it and pull the entire tenant's data. So the token never goes near
user space:

```
                      root-only /etc/mxsentinel/plugin.conf  (token)
                                     │
  cPanel user ──cpsrvd──> index.live.cgi ──unix socket──> mxsentinel-plugin (broker, root)
   (uid 1234)            (runs as uid 1234)        │              │
                                                    │              ├─ peer uid via SO_PEERCRED
  WHM admin ──cpsrvd──>  index.cgi ────────────────┘              │   (kernel-reported, unforgeable)
   (uid 0)               (runs as root)                           ├─ uid 0   → whole tenant
                                                                  └─ uid !=0 → that account's domains only
                                                                                   │
                                                                                   └─HTTPS+token─> apid
```

The broker decides scope from the **kernel-reported uid** of the socket peer
(`SO_PEERCRED`), not from anything the client sends — a user process cannot impersonate
another account or root. The CGI is a dumb relay: it serves the static dashboard and
forwards `?api=summary` to the socket. For a user scope, the broker maps uid → cPanel
username (`getpwuid`) → owned domains (from `/var/cpanel/users/<user>` and
`/etc/userdatadomains`), fetches only those domains from apid, and strips tenant-wide
fields (provider deliverability, egress-IP health).

## Components

```
cmd/cpanel-plugin/          one binary: `serve` (broker) | `cgi`
internal/cpanelplugin/      broker, CGI, scope resolution, upstream client, embedded UI
plugins/cpanel/
  plugin.conf.example       broker config template (-> /etc/mxsentinel/plugin.conf, 0600)
  systemd/…service          broker unit (RuntimeDirectory=/run/mxsentinel-plugin)
  whm/mxsentinel.conf        WHM AppConfig
  whm/index.cgi              WHM entrypoint (runs as root → admin view)
  user/index.live.cgi        cPanel entrypoint (runs as account → scoped view)
  user/dynamicui_…conf       cPanel Jupiter menu registration
  install.sh / uninstall.sh  installers
```

## Prerequisites

- A WHM/cPanel server (Jupiter theme; CloudLinux/AlmaLinux/CentOS).
- Network reachability from the server to your MX Sentinel `apid` over HTTPS.
- A tenant API token with **read** scope: `mxctl apikey create --tenant <slug> --scope read`
  (or `make apikey` in dev).
- The plugin binary built for the server's OS/arch.

## Install

1. **Build the binary** (on any machine with Go, then copy it to the server):

   ```bash
   GOOS=linux GOARCH=amd64 go build -o mxsentinel-plugin ./cmd/cpanel-plugin
   # or, on the repo's Makefile host:  make build-cpanel-plugin
   ```

2. **Copy `plugins/cpanel/` and the binary to the server**, then run as root:

   ```bash
   cd plugins/cpanel
   ./install.sh --bin /path/to/mxsentinel-plugin \
     --api-base https://api.mxsentinel.example.com \
     --token mxs_xxxxxxxx_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
   ```

   The installer: drops the binary at `/usr/local/bin/mxsentinel-plugin`, writes
   `/etc/mxsentinel/plugin.conf` (mode 0600), installs + starts the `mxsentinel-plugin`
   systemd unit, registers the WHM AppConfig, installs the cPanel Jupiter menu item,
   and — if CageFS is present — exposes the socket directory inside user cages.

   Flags: `--whm-only`, `--user-only`, `--no-verify-ssl`, `--keep-config`.

3. **Verify:**

   ```bash
   systemctl status mxsentinel-plugin
   curl --unix-socket /run/mxsentinel-plugin/api.sock http://x/healthz   # 200
   ```

   - WHM → sidebar → search **MX Sentinel**.
   - cPanel → **Email** group → **MX Sentinel** (users may need to re-login).

## CloudLinux / CageFS

If CageFS is enabled, each user's filesystem is jailed and the broker socket under
`/run` is not visible inside the cage by default. `install.sh` adds
`/run/mxsentinel-plugin` to `/etc/cagefs/cagefs.mp` and runs `cagefsctl --remount-all`.
If you manage CageFS config differently, ensure that path is mounted into user cages, or
the user plugin will report "broker is unavailable".

## Updating

Rebuild the binary, copy it over, and either re-run `install.sh --keep-config` or just:

```bash
install -m0755 mxsentinel-plugin /usr/local/bin/mxsentinel-plugin
systemctl restart mxsentinel-plugin
```

The dashboard UI is embedded in the binary, so a binary swap updates everything.

## Uninstall

```bash
./uninstall.sh            # remove plugin, keep config
./uninstall.sh --purge    # also remove /etc/mxsentinel
```

## Limitations / notes

- **Reseller WHM** logins are not root, so a reseller currently maps to a *user* scope
  rather than a full admin view. The admin view is for the root operator. (Per-reseller
  scoping is a possible follow-up.)
- The user view derives a domain's email health from apid's per-domain endpoints only;
  it never shows tenant-wide provider deliverability or egress-IP reputation (those are
  admin-only by design).
- The cPanel user-menu registration uses the Jupiter `dynamicui` mechanism. If your
  cPanel version handles plugin menus differently, the files under
  `…/frontend/jupiter/mxsentinel/` are still served — only the menu entry may need
  adjusting.
