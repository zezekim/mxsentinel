#!/bin/sh
# WHM admin entrypoint — the MX Sentinel relay installer. cpsrvd runs this as root under
# the whostmgr docroot, so it is inherently admin-only. MXS_PLUGIN_MODE=whm selects the
# privileged relay-config tool (provision credential + rewrite Exim), which reads the API
# token from the root-only config directly (no broker socket needed).
MXS_PLUGIN_MODE=whm
MXS_PLUGIN_CONFIG="${MXS_PLUGIN_CONFIG:-/etc/mxsentinel/plugin.conf}"
export MXS_PLUGIN_MODE MXS_PLUGIN_CONFIG
exec /usr/local/bin/mxsentinel-plugin cgi
