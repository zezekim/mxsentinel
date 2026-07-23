#!/bin/sh
# cPanel user entrypoint. cpsrvd runs .live.cgi as the logged-in cPanel account, so
# the broker reads that account's uid from the socket peer credentials and scopes the
# response to only the domains that account owns. The account never sees the API token
# nor any other tenant's data.
MXS_PLUGIN_SOCKET="${MXS_PLUGIN_SOCKET:-/run/mxsentinel-plugin/api.sock}"
export MXS_PLUGIN_SOCKET
exec /usr/local/bin/mxsentinel-plugin cgi
