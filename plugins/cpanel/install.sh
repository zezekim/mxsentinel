#!/bin/bash
#
# MX Sentinel cPanel/WHM plugin installer. Run as root ON the WHM/cPanel server.
#
#   ./install.sh --bin ./mxsentinel-plugin --api-base https://api.you.com --token mxs_...
#
# Or let the server mint its OWN key — no SSH to the MX Sentinel host, no copy/paste.
# Prefer the env form: a flag value is visible to every user on the box via `ps`.
#
#   MXS_ENROLL_TOKEN=mxs_... ./install.sh --bin ./mxsentinel-plugin --api-base https://api.you.com
#   ./install.sh --bin ./mxsentinel-plugin --api-base https://api.you.com --enroll-token mxs_...
#
# Flags:
#   --bin PATH        Path to the mxsentinel-plugin binary (built for this server's OS/arch).
#   --api-base URL    MX Sentinel apid base URL.        (or env MXS_API_BASE)
#   --token TOKEN     Tenant API token with READ+RELAY scope (admin also works — it
#                     provisions the relay SMTP user).  (or env MXS_API_TOKEN)
#   --enroll-token T  Enrollment token (PROVISION scope). Instead of pasting a token,
#                     this server POSTs /v1/apikeys once and mints its own read+relay
#                     key. Ignored when --token/MXS_API_TOKEN is set.
#                                                       (or env MXS_ENROLL_TOKEN)
#   --key-name NAME   Name for the self-enrolled key. Default: cpanel-$(hostname -f).
#                     Lowercased and stripped to [a-z0-9.-]; must match
#                     ^cpanel-[a-z0-9][a-z0-9.-]*$ (the server enforces this too).
#   --no-verify-ssl   Disable TLS verification to apid (self-signed/internal CA).
#   --whm-only        Install only the WHM admin plugin.
#   --user-only       Install only the cPanel end-user plugin.
#   --keep-config     Do not overwrite an existing /etc/mxsentinel/plugin.conf.
#
# Mint an enrollment token once, on the MX Sentinel host, and reuse it per server:
#   mxctl apikey create --tenant <slug> --scopes provision --name cpanel-enroll
#
set -euo pipefail

# This script handles API tokens. Anyone debugging it with `bash -x` (or with xtrace
# exported via SHELLOPTS) would otherwise get every token echoed to their terminal, their
# scrollback, and whatever log they piped the install into. So tracing is suppressed
# across the two stretches that touch a token — flag parsing and the config section — and
# restored afterwards, leaving the parts worth debugging (binary, systemd, Exim, WHM)
# traceable. Both helpers are no-ops unless the caller actually asked for xtrace.
XTRACE=0
case "$-" in *x*) XTRACE=1;; esac
mxs_hush()   { if [ "$XTRACE" = "1" ]; then set +x; fi; }
mxs_resume() { if [ "$XTRACE" = "1" ]; then set -x; fi; }

BIN_SRC=""
API_BASE="${MXS_API_BASE:-}"
API_TOKEN="${MXS_API_TOKEN:-}"
ENROLL_TOKEN="${MXS_ENROLL_TOKEN:-}"
KEY_NAME=""
VERIFY_SSL="true"
DO_WHM=1
DO_USER=1
KEEP_CONFIG=0

mxs_hush   # two of these flags carry a token
while [ $# -gt 0 ]; do
  case "$1" in
    --bin) BIN_SRC="$2"; shift 2;;
    --api-base) API_BASE="$2"; shift 2;;
    --token) API_TOKEN="$2"; shift 2;;
    --enroll-token) ENROLL_TOKEN="$2"; shift 2;;
    --key-name) KEY_NAME="$2"; shift 2;;
    --no-verify-ssl) VERIFY_SSL="false"; shift;;
    --whm-only) DO_USER=0; shift;;
    --user-only) DO_WHM=0; shift;;
    --keep-config) KEEP_CONFIG=1; shift;;
    *) echo "unknown flag: $1" >&2; exit 2;;
  esac
done
mxs_resume

[ "$(id -u)" = "0" ] || { echo "must run as root" >&2; exit 1; }
[ -d /usr/local/cpanel ] || { echo "this does not look like a cPanel/WHM server (/usr/local/cpanel missing)" >&2; exit 1; }

HERE="$(cd "$(dirname "$0")" && pwd)"
BINDEST="/usr/local/bin/mxsentinel-plugin"
CONFDIR="/etc/mxsentinel"
CONF="$CONFDIR/plugin.conf"
SOCKET_DIR="/run/mxsentinel-plugin"

# --- locate the binary ---------------------------------------------------------
if [ -z "$BIN_SRC" ]; then
  for c in "$HERE/mxsentinel-plugin" "$HERE/../../bin/mxsentinel-plugin" "./mxsentinel-plugin"; do
    [ -x "$c" ] && BIN_SRC="$c" && break
  done
fi
[ -n "$BIN_SRC" ] && [ -f "$BIN_SRC" ] || {
  echo "binary not found. Build it for this server:" >&2
  echo "  GOOS=linux GOARCH=amd64 go build -o mxsentinel-plugin ./cmd/cpanel-plugin" >&2
  echo "then re-run with --bin ./mxsentinel-plugin" >&2
  exit 1
}

echo "==> installing binary -> $BINDEST"
install -m 0755 "$BIN_SRC" "$BINDEST"

# --- self-enrollment -------------------------------------------------------------
# Trades an enrollment token (provision scope) for THIS server's own narrow read+relay
# token, so nobody has to SSH to the MX Sentinel host and paste one in. Sets API_TOKEN.
#
# Both tokens are handled as if every user on this box were watching — on a shared cPanel
# server they are. /proc/*/cmdline is world-readable, so anything handed to curl as an
# argument can be lifted straight out of `ps`. Nothing secret goes in argv, nothing secret
# goes to stdout or a log, and nothing secret touches the disk outside the 0600 config
# file we are about to write.
mxs_enroll() {
  mxs_hush   # belt and braces: the caller has hushed already, but this function holds
             # both tokens at once and must never be the thing that leaks them.

  # The server insists on ^cpanel-[a-z0-9][a-z0-9.-]*$. Fold case and drop every other
  # character first; after that strip, a case-glob is a complete check of the pattern —
  # only the prefix and the first character are still in question.
  KEY_NAME="$(printf '%s' "$KEY_NAME" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9.-')"
  case "$KEY_NAME" in
    cpanel-[a-z0-9]*) ;;
    *) echo "invalid key name '$KEY_NAME': after lowercasing and stripping to [a-z0-9.-] it must match ^cpanel-[a-z0-9][a-z0-9.-]*$" >&2
       echo "  this server's hostname may be unset or unusable — pass --key-name cpanel-<host>" >&2
       exit 1;;
  esac

  ENROLL_URL="${API_BASE%/}/v1/apikeys"
  echo "==> enrolling this server as '$KEY_NAME' at $ENROLL_URL"

  # curl reads the entire request — URL, method, headers, body — from a config file on
  # stdin (--config -), which is what keeps the enrollment token out of argv. printf is a
  # shell builtin, so the token never becomes another process's arguments either, and a
  # pipe means it never lands in a temp file to leak, chmod, or clean up. Only non-secret
  # options stay on the command line.
  #
  # Quoting matters here: curl truncates an UNQUOTED config value at the first space (it
  # only warns), which would silently drop the Authorization header and send an
  # unauthenticated request. So anything containing a space is double-quoted. The body is
  # left unquoted — it has no whitespace by construction (KEY_NAME is validated down to
  # [a-z0-9.-] above), and unquoted values take no backslash escapes, so its JSON quotes
  # pass through as-is.
  ENROLL_RC=0
  ENROLL_OUT="$(
    {
      printf '%s\n' \
        "url = $ENROLL_URL" \
        "request = POST" \
        "header = \"Authorization: Bearer $ENROLL_TOKEN\"" \
        "header = \"Content-Type: application/json\"" \
        "data = {\"name\":\"$KEY_NAME\",\"scopes\":[\"read\",\"relay\"]}"
      if [ "$VERIFY_SSL" = "false" ]; then printf '%s\n' "insecure"; fi
    } | curl --config - --silent --show-error --max-time 30 --write-out '\n%{http_code}' 2>&1
  )" || ENROLL_RC=$?
  ENROLL_CODE="$(printf '%s\n' "$ENROLL_OUT" | tail -n 1)"
  ENROLL_BODY="$(printf '%s\n' "$ENROLL_OUT" | sed '$d')"
  # Report what the server said, never what we sent.
  ENROLL_MSG="$(printf '%s' "$ENROLL_BODY" | sed -En 's/.*"(error|message)"[[:space:]]*:[[:space:]]*"([^"]*)".*/\2/p' | tail -n 1 || true)"

  case "$ENROLL_CODE" in
    200|201) ;;
    000|"")
      echo "could not reach $ENROLL_URL (curl exit $ENROLL_RC) — check DNS, egress firewall, TLS and --api-base" >&2
      echo "  ${ENROLL_BODY:-no response from server}" >&2
      exit 1;;
    401|403)
      echo "enrollment token rejected (HTTP $ENROLL_CODE${ENROLL_MSG:+: $ENROLL_MSG})" >&2
      echo "  it must be a current, unrevoked token holding the 'provision' scope. Mint one on the" >&2
      echo "  MX Sentinel host: mxctl apikey create --tenant <slug> --scopes provision --name cpanel-enroll" >&2
      exit 1;;
    409)
      echo "a key named '$KEY_NAME' is already enrolled (HTTP 409${ENROLL_MSG:+: $ENROLL_MSG})" >&2
      echo "  revoke the old one on the MX Sentinel host (mxctl apikey list / mxctl apikey revoke)," >&2
      echo "  re-run with --token if you still have it, or pick another --key-name cpanel-<name>" >&2
      exit 1;;
    *)
      echo "enrollment failed (HTTP $ENROLL_CODE${ENROLL_MSG:+: $ENROLL_MSG})" >&2
      echo "  name '$KEY_NAME', scopes read+relay — check the apid logs for the matching request" >&2
      exit 1;;
  esac

  # No jq: cPanel servers don't reliably ship it. The token format is fixed by the API
  # (mxs_ + 8 hex + '_' + 40 hex), so this grep is unambiguous even if the JSON grows
  # fields. head -n 1 would SIGPIPE grep under `set -o pipefail`, so take the last match.
  API_TOKEN="$(printf '%s' "$ENROLL_BODY" | grep -oE 'mxs_[0-9a-f]{8}_[0-9a-f]{40}' | tail -n 1 || true)"
  [ "${#API_TOKEN}" = "53" ] || {
    echo "enrollment returned HTTP $ENROLL_CODE but no usable token" >&2
    echo "  expected mxs_ + 8 hex + _ + 40 hex (53 chars), matched ${#API_TOKEN} — refusing to write" >&2
    echo "  a truncated or empty token to $CONF. Check that apid is current." >&2
    exit 1
  }

  # The prefix is the key's public identifier (it is what `mxctl apikey list` shows), so
  # it is safe to print and makes the key findable later. The secret half never surfaces.
  ENROLL_EXP="$(printf '%s' "$ENROLL_BODY" | sed -En 's/.*"expires_at"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p' | tail -n 1 || true)"
  echo "    minted key $(printf '%s' "$API_TOKEN" | cut -c1-12) (scopes: read,relay${ENROLL_EXP:+; expires $ENROLL_EXP})"

  # Done with it — drop it so nothing downstream (or any child process) can leak it.
  ENROLL_TOKEN=""
  unset MXS_ENROLL_TOKEN
}

# --- config --------------------------------------------------------------------
mxs_hush   # everything down to the matching mxs_resume handles the API token
mkdir -p "$CONFDIR"; chmod 0755 "$CONFDIR"
if [ -f "$CONF" ] && [ "$KEEP_CONFIG" = "1" ]; then
  echo "==> keeping existing $CONF"
else
  if [ -z "$API_BASE" ]; then read -r -p "MX Sentinel API base URL: " API_BASE; fi
  # An explicit --token/MXS_API_TOKEN always wins — self-enrollment slots in ahead of the
  # prompt only, so an invocation that already passes a token behaves exactly as before.
  if [ -n "$API_TOKEN" ] && [ -n "$ENROLL_TOKEN" ]; then
    echo "==> --token supplied — skipping self-enrollment"
  fi
  if [ -z "$API_TOKEN" ] && [ -n "$ENROLL_TOKEN" ]; then
    [ -n "$API_BASE" ] || { echo "api-base is required to self-enroll" >&2; exit 1; }
    [ -n "$KEY_NAME" ] || KEY_NAME="cpanel-$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
    mxs_enroll
  fi
  if [ -z "$API_TOKEN" ]; then read -r -p "Tenant API token (mxs_..., read+relay scopes): " API_TOKEN; fi
  [ -n "$API_BASE" ] && [ -n "$API_TOKEN" ] || { echo "api-base and token are required" >&2; exit 1; }
  echo "==> writing $CONF (root-only)"
  umask 077
  cat > "$CONF" <<EOF
# Generated by plugins/cpanel/install.sh — keep this file root-only (0600).
api_base = ${API_BASE%/}
token = $API_TOKEN
verify_ssl = $VERIFY_SSL
socket_path = $SOCKET_DIR/api.sock
EOF
  chmod 0600 "$CONF"
fi
mxs_resume

# --- broker daemon (systemd) ----------------------------------------------------
echo "==> installing systemd unit"
install -m 0644 "$HERE/systemd/mxsentinel-plugin.service" /etc/systemd/system/mxsentinel-plugin.service
systemctl daemon-reload
systemctl enable --now mxsentinel-plugin.service
sleep 1
systemctl is-active --quiet mxsentinel-plugin.service \
  && echo "    broker active" \
  || { echo "    broker failed to start — check: journalctl -u mxsentinel-plugin" >&2; }

# --- CageFS (CloudLinux): expose the socket dir inside user cages ---------------
if command -v cagefsctl >/dev/null 2>&1; then
  echo "==> CageFS detected — exposing $SOCKET_DIR inside user cages"
  if ! grep -qxF "$SOCKET_DIR" /etc/cagefs/cagefs.mp 2>/dev/null; then
    echo "$SOCKET_DIR" >> /etc/cagefs/cagefs.mp
  fi
  cagefsctl --remount-all >/dev/null 2>&1 || \
    echo "    NOTE: 'cagefsctl --remount-all' failed; run it manually so users can reach the socket." >&2
fi

# --- WHM admin plugin -----------------------------------------------------------
if [ "$DO_WHM" = "1" ]; then
  echo "==> installing WHM admin plugin"
  WHMCGI=/usr/local/cpanel/whostmgr/docroot/cgi/mxsentinel
  mkdir -p "$WHMCGI"
  install -m 0755 "$HERE/whm/index.cgi" "$WHMCGI/index.cgi"
  /usr/local/cpanel/bin/register_appconfig "$HERE/whm/mxsentinel.conf" \
    && echo "    registered in WHM (search 'MX Sentinel' in the WHM sidebar)" \
    || echo "    register_appconfig failed — register manually." >&2
fi

# --- cPanel end-user plugin -----------------------------------------------------
if [ "$DO_USER" = "1" ]; then
  echo "==> installing cPanel user plugin"
  for THEME in jupiter; do
    BASE="/usr/local/cpanel/base/frontend/$THEME"
    [ -d "$BASE" ] || continue
    PDIR="$BASE/mxsentinel"
    mkdir -p "$PDIR"
    install -m 0755 "$HERE/user/index.live.cgi" "$PDIR/index.live.cgi"
    # Icon: ship one as plugins/cpanel/user/mxsentinel.png, else drop a placeholder.
    if [ -f "$HERE/user/mxsentinel.png" ]; then
      install -m 0644 "$HERE/user/mxsentinel.png" "$PDIR/mxsentinel.png"
    else
      printf 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMCAQDc7v9aAAAAAElFTkSuQmCC' \
        | base64 -d > "$PDIR/mxsentinel.png" 2>/dev/null || true
    fi
    install -m 0644 "$HERE/user/dynamicui_mxsentinel.conf" "$BASE/dynamicui/dynamicui_mxsentinel.conf"
    echo "    installed into theme: $THEME"
  done
  /usr/local/cpanel/bin/rebuild_sprites jupiter >/dev/null 2>&1 || true
  echo "    users will see 'MX Sentinel' under the Email group (re-login may be needed)"
fi

echo
echo "Done. Verify the broker can reach apid:"
echo "    curl --unix-socket $SOCKET_DIR/api.sock http://x/healthz   # expect HTTP 200"
echo "    journalctl -u mxsentinel-plugin -n 30"
