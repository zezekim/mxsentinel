# MX Sentinel — cPanel/WHM plugin (outbound relay installer)

Points the server's Exim outbound at the MX Sentinel relay — like the MailChannels plugin.
Full docs: [`../../docs/cpanel-plugin.md`](../../docs/cpanel-plugin.md).

```bash
# 1. build for the target server (from repo root)
make build-cpanel-plugin            # → bin/mxsentinel-plugin (linux/amd64)

# 2. copy this dir + the binary to the server, then as root:
./install.sh --bin ./mxsentinel-plugin --api-base https://sentinel.you.com --enroll-token mxs_...
```

- **WHM → MX Sentinel** (admin): provision the relay credential, **Enable/Disable** server-wide
  relay routing (rewrites Exim safely), **Test** the path, view **DNS** to publish.
- **cPanel → Email → MX Sentinel** (end user): read-only status for that account's domains.

Needs an API token with **read + relay** scope (`relay` covers provisioning the relay SMTP
user). Easiest is to let the server enroll itself: mint one `provision`-scoped enrollment
token for the fleet — `mxctl apikey create --tenant <slug> --scopes provision --name fleet-enroll`
— and pass it as `--enroll-token` (env `MXS_ENROLL_TOKEN`); the installer mints this server
its own key, named `cpanel-$(hostname -f)` (`--key-name` overrides). To supply the key
yourself instead, use `--token` with a key from
`mxctl apikey create --tenant <slug> --scopes read,relay --name cpanel-<fqdn>` — an existing
**admin** token also still works. Revoke a retired server's key with
`mxctl apikey revoke --tenant <slug> --name cpanel-<fqdn>`.

Safety: enabling backs up `/etc/exim.conf.local`, rebuilds, validates with `exim -bV`, and
rolls back if the config doesn't parse. Manual rollback: delete the `# BEGIN/END mxsentinel`
blocks and run `buildeximconf && restartsrv_exim`.

Security: the API token lives only in `/etc/mxsentinel/plugin.conf` (root, 0600). The WHM
tool (root, admin-only docroot) reads it directly; the end-user status page never sees it —
it goes through the root broker, scoped by the connection's kernel-reported uid (`SO_PEERCRED`).

Add a 48×48 `user/mxsentinel.png` before installing to replace the placeholder menu icon.
