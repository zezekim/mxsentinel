#!/bin/sh
# WHM admin entrypoint for the MX Sentinel plugin. cpsrvd runs this as root, so the
# broker attributes peer uid 0 → full-tenant (admin) view.
MXS_PLUGIN_SOCKET="${MXS_PLUGIN_SOCKET:-/run/mxsentinel-plugin/api.sock}"
export MXS_PLUGIN_SOCKET
exec /usr/local/bin/mxsentinel-plugin cgi
