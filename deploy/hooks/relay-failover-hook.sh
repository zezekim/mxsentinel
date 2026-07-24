#!/usr/bin/env bash
# MX Sentinel — host-side outbound-failover hook.
#
# relayfailoverd (container) runs a circuit breaker over the relay-wide TRANSIENT 4xx defer
# rate to a receiver provider (default Microsoft/Outlook) and writes the set of recipient
# domains currently in failover to a bind-mounted state file (MXS_FAILOVER_STATE_FILE -> host
# FAILOVER_DOMAINS_FILE). It cannot reload host Postfix from inside the container, so this
# script runs on the HOST (via cron/systemd-timer) to:
#
#   1. rebuild the Postfix transport OVERLAY map (MAP_FILE) so each failover domain routes to
#      the fallback smarthost transport (FALLBACK_TRANSPORT, e.g. relay-mailbaby:); an empty
#      state file clears the overlay (normal, breaker-closed state);
#   2. ensure the overlay map is the FIRST entry in transport_maps (so a specific failover
#      domain wins over the IP-rotation randmap catch-all), self-healing if another hook
#      rewrote transport_maps;
#   3. on any change, requeue deferred mail so it immediately picks up the new route.
#
# Only TRANSIENT 4xx defers ever put a domain here — relayfailoverd never fails over spam or
# reputation 5xx blocks (rerouting those just launders reputation onto the fallback's IPs).
# See docs/relay-failover.md and deploy/install.sh (--wire-relay-failover).
#
# Install (run as root, every few minutes):
#   */2 * * * * /opt/mxsentinel/deploy/hooks/relay-failover-hook.sh >> /var/log/mxs-failover-hook.log 2>&1
#
# Env overrides:
#   FAILOVER_DOMAINS_FILE  state file written by relayfailoverd
#                          (default: <repo>/deploy/failover-state/failover-domains)
#   MAP_FILE               Postfix transport overlay map (default: /etc/postfix/mxs_failover)
#   FALLBACK_TRANSPORT     transport to route failover domains to (default: relay-mailbaby:)
#   STATE_FILE             last-applied set, to avoid needless reloads (default: next to this script)
#   REQUEUE                on change, "targeted" (only matching domains, needs jq+postqueue -j)
#                          or "all" (postsuper -r ALL) or "none" (default: targeted, falls back to all)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The installer (--wire-relay-failover) writes the smarthost nexthop (FALLBACK_TRANSPORT,
# e.g. relay-mailbaby:[smtp.mailbaby.net]:587) here. Source it so the overlay routes to the
# actual fallback smarthost rather than the recipient's real MX. An explicit env var wins.
FAILOVER_ENV="${FAILOVER_ENV:-/etc/postfix/mxs_failover.env}"
[ -f "$FAILOVER_ENV" ] && . "$FAILOVER_ENV"

FAILOVER_DOMAINS_FILE="${FAILOVER_DOMAINS_FILE:-$SCRIPT_DIR/../failover-state/failover-domains}"
MAP_FILE="${MAP_FILE:-/etc/postfix/mxs_failover}"
FALLBACK_TRANSPORT="${FALLBACK_TRANSPORT:-relay-mailbaby:}"
STATE_FILE="${STATE_FILE:-$SCRIPT_DIR/.failover-applied}"
REQUEUE="${REQUEUE:-targeted}"
ts() { date -Is 2>/dev/null || date; }

command -v postconf >/dev/null 2>&1 || { echo "$(ts) postconf not found — not a Postfix host?"; exit 0; }

# --- Dashboard-managed smarthost credentials + transport (rendered by relayfailoverd) --------
# When the fallback smarthost is configured in the dashboard, relayfailoverd renders
# smarthost.sasl ("[host]:port user:pass") and smarthost.transport ("FALLBACK_TRANSPORT=...")
# next to the domains file. Apply them so credentials + host are managed from the UI, not by
# hand-editing /etc/postfix. (The one-time `--wire-relay-failover` still defines the
# relay-mailbaby transport + transport_maps overlay; only creds/host/domains are dynamic.)
STATE_DIR="$(dirname "${FAILOVER_DOMAINS_FILE:-$SCRIPT_DIR/../failover-state/failover-domains}")"
RENDERED_SASL="$STATE_DIR/smarthost.sasl"
RENDERED_TRANSPORT="$STATE_DIR/smarthost.transport"
LIVE_SASL="/etc/postfix/mxs_failover_sasl"
SMARTHOST_CHANGED=0

# transport nexthop: the dashboard-rendered value wins over the static env file sourced above.
[ -f "$RENDERED_TRANSPORT" ] && . "$RENDERED_TRANSPORT"

if [ -f "$RENDERED_SASL" ]; then
	# Install creds only when they actually changed, then ensure SASL client auth is enabled.
	if ! cmp -s "$RENDERED_SASL" "$LIVE_SASL" 2>/dev/null; then
		install -m 0600 "$RENDERED_SASL" "$LIVE_SASL"
		postmap "$LIVE_SASL"
		postconf -e \
			"smtp_sasl_auth_enable = yes" \
			"smtp_sasl_password_maps = hash:$LIVE_SASL" \
			"smtp_sasl_security_options = noanonymous" \
			"smtp_sasl_mechanism_filter = plain, login"
		SMARTHOST_CHANGED=1
		echo "$(ts) applied dashboard-managed smarthost credentials -> $LIVE_SASL"
	fi
fi

# FAIL-SAFE: a MISSING state file means relayfailoverd isn't managing this overlay (not
# deployed, not configured, wrong bind-mount, or a permission error writing the file). In that
# case leave the overlay exactly as-is rather than clearing it — clearing a working hand- or
# previously-populated overlay is how consumer-Outlook routing silently broke. To DISABLE
# failover, write an EMPTY state file (a deliberate, present-but-empty signal), which the
# block below treats as "clear".
if [ ! -f "$FAILOVER_DOMAINS_FILE" ]; then
	echo "$(ts) state file $FAILOVER_DOMAINS_FILE absent — relayfailoverd not writing it; leaving overlay unchanged"
	exit 0
fi

# Read the desired domain set (a present-but-empty file = failover disabled = clear the overlay).
DOMAINS=()
if [ -f "$FAILOVER_DOMAINS_FILE" ]; then
	# Accept only plausible domain lines; lowercase; de-dupe; sort for a stable comparison.
	# (read loop rather than mapfile so the hook runs on bash 3.2, e.g. macOS, for local testing.)
	while IFS= read -r _d; do [ -n "$_d" ] && DOMAINS+=("$_d"); done < <(
		grep -E '^[a-zA-Z0-9.-]+$' "$FAILOVER_DOMAINS_FILE" | tr '[:upper:]' '[:lower:]' | sort -u || true)
fi

# No-op if the desired set is identical to what we last applied (cron runs often; reloads
# and requeues should not).
NEW_SET="$(printf '%s\n' "${DOMAINS[@]:-}")"
LAST_SET=""
[ -f "$STATE_FILE" ] && LAST_SET="$(cat "$STATE_FILE")"

# --- (1) rebuild the overlay map (idempotent even on no-op, to self-heal a deleted map) ---
{
	for d in "${DOMAINS[@]:-}"; do
		[ -n "$d" ] && printf '%s\t%s\n' "$d" "$FALLBACK_TRANSPORT"
	done
} > "$MAP_FILE"
postmap "$MAP_FILE"

# --- (2) ensure the overlay is the first entry in transport_maps (self-healing) ---
CUR="$(postconf -h transport_maps 2>/dev/null || true)"
if ! printf '%s' "$CUR" | grep -q "hash:$MAP_FILE"; then
	if [ -n "$CUR" ]; then
		postconf -e "transport_maps = hash:$MAP_FILE, $CUR"
	else
		postconf -e "transport_maps = hash:$MAP_FILE"
	fi
	echo "$(ts) inserted hash:$MAP_FILE at front of transport_maps"
fi

if [ "$NEW_SET" = "$LAST_SET" ]; then
	# Domain set unchanged. Reload only if the smarthost credentials changed this run (so a
	# dashboard cred update takes effect); otherwise do nothing (cron runs often).
	if [ "$SMARTHOST_CHANGED" = "1" ]; then
		systemctl reload postfix 2>/dev/null || postfix reload 2>/dev/null || true
	fi
	exit 0
fi

systemctl reload postfix 2>/dev/null || postfix reload
echo "$(ts) failover overlay -> ${#DOMAINS[@]} domain(s): ${DOMAINS[*]:-<none>} via $FALLBACK_TRANSPORT"

# --- (3) requeue deferred mail so it picks up the new route ---
requeue_targeted() {
	command -v jq >/dev/null 2>&1 || return 1
	postqueue -j >/dev/null 2>&1 || return 1
	# Build a jq regex of the failover domains; requeue queue IDs whose recipient matches.
	local pat="" d ids
	for d in "${DOMAINS[@]:-}"; do
		[ -n "$d" ] && pat="$pat${pat:+|}$(printf '%s' "$d" | sed 's/\./\\./g')"
	done
	# On CLEAR (empty set) there is nothing domain-specific to requeue — the reverted overlay
	# takes effect on Postfix's own next retry, so skip targeted requeue.
	[ -n "$pat" ] || return 0
	ids="$(postqueue -j 2>/dev/null | jq -r --arg pat "$pat" \
		'select(any(.recipients[].address; test("@(" + $pat + ")$"; "i"))) | .queue_id' 2>/dev/null | sort -u || true)"
	[ -n "$ids" ] || return 0
	printf '%s\n' "$ids" | while read -r id; do [ -n "$id" ] && postsuper -r "$id" >/dev/null 2>&1 || true; done
	echo "$(ts) requeued $(printf '%s\n' "$ids" | grep -c .) matching deferred message(s)"
}

case "$REQUEUE" in
	none) : ;;
	all)  postsuper -r ALL >/dev/null 2>&1 || true; echo "$(ts) requeued ALL deferred mail" ;;
	*)    requeue_targeted || { postsuper -r ALL >/dev/null 2>&1 || true; echo "$(ts) targeted requeue unavailable — requeued ALL"; } ;;
esac

printf '%s' "$NEW_SET" > "$STATE_FILE"
