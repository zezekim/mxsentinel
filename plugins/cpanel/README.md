# MX Sentinel — cPanel/WHM plugin

Install artifacts for the on-server plugin. Full docs: [`../../docs/cpanel-plugin.md`](../../docs/cpanel-plugin.md).

```bash
# 1. build for the target server
GOOS=linux GOARCH=amd64 go build -o mxsentinel-plugin ./cmd/cpanel-plugin   # from repo root

# 2. copy this dir + the binary to the server, then as root:
./install.sh --bin ./mxsentinel-plugin --api-base https://api.you.com --token mxs_...
```

- **WHM admin view**: WHM sidebar → "MX Sentinel" (whole tenant).
- **cPanel user view**: cPanel → Email → "MX Sentinel" (that account's domains only).

Security: the API token lives only in `/etc/mxsentinel/plugin.conf` (root, 0600) and is
read only by the root broker daemon. The end-user CGI never sees it; the broker scopes
every response by the connection's kernel-reported uid (`SO_PEERCRED`). See the docs for
the full model and the CloudLinux/CageFS note.

Add your own 48×48 `user/mxsentinel.png` before installing to replace the placeholder
menu icon.
