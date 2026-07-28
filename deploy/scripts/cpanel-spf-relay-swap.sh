#!/bin/bash
#
# spf-relay-swap.sh — ensure every cPanel/WHM DNS zone's SPF authorizes your relay.
#
# For each zone's SPF (v=spf1) TXT record it does ONE of:
#   * SKIP  — already contains spf.squidix.net (idempotent; safe to re-run).
#   * SWAP  — contains an old include (relay.mailchannels.net) -> replace with spf.squidix.net.
#   * ADD   — SPF exists but lacks the include -> insert `include:spf.squidix.net` before `all`.
# And optionally (--add-missing) CREATEs `v=spf1 include:spf.squidix.net ~all` for zones that
# have no SPF record at all.
#
# Reads zones with `whmapi1 dumpzone` and writes with `whmapi1 editzonerecord` /
# `addzonerecord` (bumps the SOA serial, reloads the zone, and syncs a DNS cluster if you run
# one). Never hand-edits /var/named — so cPanel won't revert it.
#
# NOTE (vs. the swap-only version): discovery now scans ALL zones that have an SPF record
# (grep /var/named for `v=spf1`), not just the ones containing the old string — because "add if
# missing" has to see zones that never had the relay include. With --add-missing it scans every
# zone via listzones. DRY-RUN by default.
#
# Usage:
#   ./spf-relay-swap.sh                          # dry run: report swap/add across all SPF zones
#   ./spf-relay-swap.sh --zone 12gh5.com --apply # act on ONE zone (test first!)
#   ./spf-relay-swap.sh --apply                  # swap+add across all SPF zones
#   ./spf-relay-swap.sh --apply --add-missing    # also CREATE SPF where none exists
#   ./spf-relay-swap.sh --old X --new Y          # override the strings
#
# Flags:
#   --apply         Perform edits (default is dry-run / report only).
#   --add-missing   Also create v=spf1 include:<new> ~all for zones with no SPF record.
#   --zone NAME     Only process this one zone (skips discovery).
#   --old STRING    Include to replace  (default: relay.mailchannels.net)
#   --new STRING    Include host to ensure present (default: spf.squidix.net)
#   -h | --help     This help.
#
set -euo pipefail

OLD="relay.mailchannels.net"
NEW="spf.squidix.net"
APPLY=0
ADD_MISSING=0
ONLY_ZONE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --apply)       APPLY=1; shift;;
    --add-missing) ADD_MISSING=1; shift;;
    --zone)        ONLY_ZONE="$2"; shift 2;;
    --old)         OLD="$2"; shift 2;;
    --new)         NEW="$2"; shift 2;;
    -h|--help)     sed -n '2,40p' "$0"; exit 0;;
    *) echo "unknown flag: $1" >&2; exit 2;;
  esac
done

# --- preflight ---------------------------------------------------------------
[ "$(id -u)" = "0" ] || { echo "must run as root" >&2; exit 1; }
command -v whmapi1 >/dev/null || { echo "whmapi1 not found — is this a cPanel/WHM server?" >&2; exit 1; }
command -v jq      >/dev/null || { echo "jq not found — install with: yum -y install jq" >&2; exit 1; }

OLD_RE="${OLD//./\\.}"          # dots escaped for jq gsub regex
INC="include:${NEW}"            # the mechanism we ensure is present
NEW_SPF="v=spf1 ${INC} ~all"    # record created for zones with no SPF (--add-missing)

TS="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="/root/spf-relay-swap-backups/$TS"
LOG="/root/spf-relay-swap-$TS.log"
log() { echo "$*" | tee -a "$LOG"; }

if [ "$APPLY" = "1" ]; then
  mkdir -p "$BACKUP_DIR"
  log "==> APPLY mode. Backups: $BACKUP_DIR   Log: $LOG"
else
  log "==> DRY-RUN (no changes). Re-run with --apply to edit. Log: $LOG"
fi
log "==> Ensuring SPF include '$NEW' present (replacing '$OLD' where found)"
[ "$ADD_MISSING" = "1" ] && log "==> --add-missing: will CREATE SPF for zones that have none"
log ""

# --- jq transform: per SPF TXT record, emit  Line \t name \t ttl \t action \t newtxt ---------
read -r -d '' JQ <<'JQEOF' || true
def addinc($inc):
  ( [ splits("[ \t]+") ] | map(select(length > 0)) ) as $toks
  | ( [ $toks[] | test("^[-~?+]?all$"; "i") ] | index(true) ) as $ai
  | ( if $ai == null then $toks + [$inc] else $toks[0:$ai] + [$inc] + $toks[$ai:] end )
  | join(" ");
.data.zone[0].record[]?
| select(.type == "TXT")
| select((.txtdata // "") | test("v=spf1"; "i"))
| . as $r
| ($r.txtdata // "") as $txt
| ( if   ($txt | ascii_downcase | contains($NEW | ascii_downcase)) then "skip"
    elif ($txt | contains($OLD))                                    then "swap"
    else "add" end ) as $action
| ( if   $action == "swap" then ($txt | gsub($OLDRE; $NEW))
    elif $action == "add"  then ($txt | addinc($INC))
    else $txt end ) as $newtxt
| [ ($r.Line | tostring), ($r.name // ""), (($r.ttl // 14400) | tostring), $action, $newtxt ]
| @tsv
JQEOF

# --- gather candidate zones --------------------------------------------------
if [ -n "$ONLY_ZONE" ]; then
  ZONES=("$ONLY_ZONE")
  log "Target: single zone '$ONLY_ZONE'."
elif [ "$ADD_MISSING" = "1" ]; then
  mapfile -t ZONES < <(whmapi1 --output=json listzones | jq -r '.data.zone[].domain' | sort -u)
  log "Discovery: listzones (all ${#ZONES[@]} zones — needed to find zones lacking SPF)."
elif compgen -G "/var/named/*.db" >/dev/null 2>&1; then
  mapfile -t ZONES < <(grep -rilZ --include='*.db' 'v=spf1' /var/named 2>/dev/null \
    | xargs -0 -r -n1 basename | sed 's/\.db$//' | sort -u)
  log "Discovery: grep /var/named for SPF (fast). ${#ZONES[@]} zone(s) have an SPF record."
else
  mapfile -t ZONES < <(whmapi1 --output=json listzones | jq -r '.data.zone[].domain' | sort -u)
  log "Discovery: listzones full scan (no /var/named master files). ${#ZONES[@]} zone(s)."
fi
log ""

TOTAL_SKIP=0; TOTAL_SWAP=0; TOTAL_ADD=0; TOTAL_CREATE=0; TOTAL_EDITED=0; TOTAL_ZONES_CHANGED=0
FAILED=()

for zone in "${ZONES[@]}"; do
  [ -n "$zone" ] || continue

  dump="$(whmapi1 --output=json dumpzone zone="$zone" 2>/dev/null || true)"
  [ -n "$dump" ] && [ "$(echo "$dump" | jq -r '.metadata.result // 0')" = "1" ] || {
    log "[$zone] dumpzone failed: $(echo "$dump" | jq -r '.metadata.reason // "no response"')"
    FAILED+=("$zone"); continue; }

  matches="$(echo "$dump" | jq -r --arg OLD "$OLD" --arg OLDRE "$OLD_RE" \
                                   --arg NEW "$NEW" --arg INC "$INC" "$JQ")"

  # No SPF record in this zone.
  if [ -z "$matches" ]; then
    if [ "$ADD_MISSING" = "1" ]; then
      TOTAL_CREATE=$((TOTAL_CREATE+1))
      log "[$zone] no SPF record"
      log "    + CREATE  $NEW_SPF"
      if [ "$APPLY" = "1" ]; then
        echo "$dump" | jq '.' > "$BACKUP_DIR/${zone}.json"
        resp="$(whmapi1 --output=json addzonerecord \
          zone="$zone" name="${zone}." class=IN ttl=14400 type=TXT txtdata="$NEW_SPF" 2>/dev/null || true)"
        if [ "$(echo "$resp" | jq -r '.metadata.result // 0')" = "1" ]; then
          TOTAL_ZONES_CHANGED=$((TOTAL_ZONES_CHANGED+1)); log "    OK"
        else
          log "    !! addzonerecord FAILED: $(echo "$resp" | jq -r '.metadata.reason // "unknown"')"
          FAILED+=("$zone:new")
        fi
      fi
    else
      log "[$zone] no SPF record (use --add-missing to create one)."
    fi
    continue
  fi

  zone_changed=0
  while IFS=$'\t' read -r lineno name ttl action newtxt; do
    [ -n "$lineno" ] || continue
    case "$action" in
      skip) TOTAL_SKIP=$((TOTAL_SKIP+1)); log "[$zone] line $lineno ($name): already has $NEW — skip"; continue;;
      swap) TOTAL_SWAP=$((TOTAL_SWAP+1)); log "[$zone] line $lineno ($name): SWAP";;
      add)  TOTAL_ADD=$((TOTAL_ADD+1));   log "[$zone] line $lineno ($name): ADD";;
    esac
    log "    + $newtxt"

    [ "$APPLY" = "1" ] || continue

    if [ "$zone_changed" = "0" ]; then
      echo "$dump" | jq '.' > "$BACKUP_DIR/${zone}.json"   # back up once per zone
    fi
    resp="$(whmapi1 --output=json editzonerecord \
      zone="$zone" line="$lineno" name="$name" class=IN ttl="$ttl" type=TXT \
      txtdata="$newtxt" 2>/dev/null || true)"
    if [ "$(echo "$resp" | jq -r '.metadata.result // 0')" = "1" ]; then
      TOTAL_EDITED=$((TOTAL_EDITED+1)); zone_changed=1; log "    OK"
    else
      log "    !! editzonerecord FAILED: $(echo "$resp" | jq -r '.metadata.reason // "unknown"')"
      FAILED+=("$zone:$lineno")
    fi
  done <<< "$matches"

  [ "$zone_changed" = "1" ] && TOTAL_ZONES_CHANGED=$((TOTAL_ZONES_CHANGED+1))
done

log ""
log "==> Summary: swap=$TOTAL_SWAP add=$TOTAL_ADD create=$TOTAL_CREATE skip=$TOTAL_SKIP  across ${#ZONES[@]} zone(s)"
if [ "$APPLY" = "1" ]; then
  log "    Edited/created records: $TOTAL_EDITED write(s) + create(s); $TOTAL_ZONES_CHANGED zone(s) changed."
  log "    Backups in $BACKUP_DIR (one <zone>.json per changed zone)."
  if [ "${#FAILED[@]}" -gt 0 ]; then
    log "    !! ${#FAILED[@]} failure(s): ${FAILED[*]}"
    exit 1
  fi
else
  log "    DRY-RUN — nothing changed."
  log "    Test one live edit:  $0 --zone <a-zone> --apply"
  log "    Then apply to all:   $0 --apply   (add --add-missing to also create where absent)"
fi
