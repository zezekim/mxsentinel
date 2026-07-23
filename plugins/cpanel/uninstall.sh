#!/bin/bash
# Remove the MX Sentinel cPanel/WHM plugin. Run as root on the server.
# Leaves /etc/mxsentinel/plugin.conf in place unless --purge is given.
set -uo pipefail

PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

[ "$(id -u)" = "0" ] || { echo "must run as root" >&2; exit 1; }
HERE="$(cd "$(dirname "$0")" && pwd)"

echo "==> stopping broker"
systemctl disable --now mxsentinel-plugin.service 2>/dev/null || true
rm -f /etc/systemd/system/mxsentinel-plugin.service
systemctl daemon-reload 2>/dev/null || true

echo "==> removing WHM plugin"
/usr/local/cpanel/bin/unregister_appconfig mxsentinel 2>/dev/null || true
rm -rf /usr/local/cpanel/whostmgr/docroot/cgi/mxsentinel

echo "==> removing cPanel user plugin"
for THEME in jupiter; do
  BASE="/usr/local/cpanel/base/frontend/$THEME"
  rm -rf "$BASE/mxsentinel"
  rm -f "$BASE/dynamicui/dynamicui_mxsentinel.conf"
done
/usr/local/cpanel/bin/rebuild_sprites jupiter >/dev/null 2>&1 || true

echo "==> removing binary"
rm -f /usr/local/bin/mxsentinel-plugin

# CageFS mount point
if command -v cagefsctl >/dev/null 2>&1 && grep -qxF "/run/mxsentinel-plugin" /etc/cagefs/cagefs.mp 2>/dev/null; then
  grep -vxF "/run/mxsentinel-plugin" /etc/cagefs/cagefs.mp > /etc/cagefs/cagefs.mp.tmp && mv /etc/cagefs/cagefs.mp.tmp /etc/cagefs/cagefs.mp
  cagefsctl --remount-all >/dev/null 2>&1 || true
fi

if [ "$PURGE" = "1" ]; then
  echo "==> purging config"
  rm -rf /etc/mxsentinel
fi

echo "Done."
