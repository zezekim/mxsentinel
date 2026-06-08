#!/usr/bin/env bash
# MX Sentinel — host-side RBL auto-pull hook.
#
# The rbld container monitors the relay's egress IPs against DNSBLs and writes the set of
# currently-CLEAN IPs to a bind-mounted file (RBL_HEALTHY_IPS_FILE -> host RBL_HEALTHY_IPS_DIR,
# default deploy/rbl-state/healthy-ips). rbld cannot reload host Postfix from inside the
# container, so this script runs on the HOST (via cron / systemd-timer) to rebuild the Postfix
# outbound randmap over only the healthy IPs — pulling a blocklisted IP out of rotation and
# adding it back automatically once it delists.
#
# It mirrors deploy/install.sh provision_ip_rotation exactly (one smtp-ipN transport per IP,
# bound via smtp_bind_address, selected by transport_maps = randmap:{...}), just restricted to
# the healthy subset, so the syslog tags (postfix/smtp-ipN) the telemetry parser keys on are
# unchanged. See docs/deploy-relay.md §4.6.
#
# Install (run as root, every few minutes):
#   */5 * * * * /opt/mxsentinel/deploy/hooks/rbl-rotation-hook.sh >> /var/log/mxs-rbl-hook.log 2>&1
#
# Env overrides:
#   HEALTHY_IPS_FILE  healthy-IPs file (default: <repo>/deploy/rbl-state/healthy-ips)
#   STATE_FILE        last-applied set, to avoid needless reloads (default: next to this script)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HEALTHY_IPS_FILE="${HEALTHY_IPS_FILE:-$SCRIPT_DIR/../rbl-state/healthy-ips}"
STATE_FILE="${STATE_FILE:-$SCRIPT_DIR/.rbl-rotation-applied}"
ts() { date -Is 2>/dev/null || date; }

command -v postconf >/dev/null 2>&1 || { echo "$(ts) postconf not found — not a Postfix host?"; exit 0; }

if [ ! -f "$HEALTHY_IPS_FILE" ]; then
	echo "$(ts) healthy-IPs file not found: $HEALTHY_IPS_FILE — leaving rotation unchanged"
	exit 0
fi

# Accept only well-formed IPv4/IPv6 lines; de-dupe and sort for a stable comparison.
mapfile -t HEALTHY < <(grep -E '^[0-9a-fA-F:.]+$' "$HEALTHY_IPS_FILE" | sort -u || true)

# SAFETY (fail-open): never narrow the pool to zero. If every IP is listed, or the file is
# empty/garbled, leave the current rotation as-is rather than halting ALL outbound mail.
if [ "${#HEALTHY[@]}" -lt 1 ]; then
	echo "$(ts) no healthy IPs in $HEALTHY_IPS_FILE — leaving rotation unchanged (fail-open)"
	exit 0
fi

# No-op if the healthy set is identical to what we last applied (cron runs often; reloads
# should not).
NEW_SET="$(printf '%s\n' "${HEALTHY[@]}")"
if [ -f "$STATE_FILE" ] && [ "$NEW_SET" = "$(cat "$STATE_FILE")" ]; then
	exit 0
fi

# Rebuild the rotation over the healthy IPs (same scheme as provision_ip_rotation).
i=0 rand_entries=""
for ip in "${HEALTHY[@]}"; do
	i=$((i + 1))
	name="smtp-ip$i"
	postconf -M "$name/unix=$name unix - - n - - smtp"
	postconf -P "$name/unix/smtp_bind_address=$ip" "$name/unix/syslog_name=postfix/$name"
	rand_entries="$rand_entries${rand_entries:+, }$name:"
done
postconf -e "transport_maps = randmap:{$rand_entries}"
systemctl reload postfix 2>/dev/null || postfix reload

printf '%s' "$NEW_SET" > "$STATE_FILE"
echo "$(ts) applied rotation over ${#HEALTHY[@]} healthy IP(s): ${HEALTHY[*]}"
