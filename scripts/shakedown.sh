#!/usr/bin/env bash
# =============================================================================
# MX Sentinel — end-to-end shakedown script
#
# Exercises the full pipeline:
#   GET /v1/domains  ->  POST .../dns/recheck  ->  poll GET /v1/incidents
#   until ai_summary is populated (Phase 3 AI layer confirmed).
#
# Usage:
#   export API_TOKEN="mxs_<prefix>_<secret>"   # from: make apikey
#   export API="http://localhost:8080"          # default if unset
#   bash scripts/shakedown.sh
#
# Requirements:
#   - curl (always required)
#   - jq   (optional; degrades gracefully to raw JSON output if absent)
#   - All MX Sentinel services running (see docs/shakedown.md)
#   - Ollama running with llama3 pulled (for ai_* fields)
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration — override via environment variables
# ---------------------------------------------------------------------------
API="${API:-http://localhost:8080}"
API_TOKEN="${API_TOKEN:-}"
POLL_INTERVAL=8        # seconds between incident polls
MAX_WAIT=120           # total seconds to wait for ai_summary

# ---------------------------------------------------------------------------
# Colour helpers (skip if not a TTY)
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
  CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; CYAN=''; BOLD=''; RESET=''
fi

info()    { printf "${CYAN}[info]${RESET}  %s\n"    "$*"; }
ok()      { printf "${GREEN}[ok]${RESET}    %s\n"   "$*"; }
warn()    { printf "${YELLOW}[warn]${RESET}  %s\n"  "$*"; }
die()     { printf "${RED}[error]${RESET} %s\n" "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Preflight checks
# ---------------------------------------------------------------------------
if [ -z "$API_TOKEN" ]; then
  die "API_TOKEN is not set. Run 'make apikey', copy the token, then: export API_TOKEN=<token>"
fi

command -v curl >/dev/null 2>&1 || die "curl is required but not found in PATH"

if command -v jq >/dev/null 2>&1; then
  HAS_JQ=1
  info "jq found — output will be pretty-printed"
else
  HAS_JQ=0
  warn "jq not found — raw JSON will be printed; install jq for nicer output"
fi

# Pretty-print JSON if jq is available, otherwise pass through unchanged.
pretty() {
  if [ "$HAS_JQ" -eq 1 ]; then
    jq .
  else
    cat
  fi
}

# Extract a single JSON field value. Falls back to a grep/sed heuristic without jq.
# Usage: extract_field <json_string> <jq_filter> [grep_pattern]
extract_field() {
  local json="$1"
  local filter="$2"
  if [ "$HAS_JQ" -eq 1 ]; then
    printf '%s' "$json" | jq -r "$filter"
  else
    # Crude but functional: grab the first quoted value after the key name.
    local key
    key=$(printf '%s' "$filter" | sed 's/.*\.\([a-z_]*\).*/\1/')
    printf '%s' "$json" | grep -o "\"${key}\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" \
      | head -1 | sed 's/.*: *"\(.*\)"/\1/' || printf 'unknown'
  fi
}

AUTH_HEADER="Authorization: Bearer $API_TOKEN"

printf "\n${BOLD}MX Sentinel shakedown — %s${RESET}\n\n" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
info "API endpoint : $API"
info "Token prefix : $(printf '%s' "$API_TOKEN" | cut -c1-12)..."
printf "\n"

# ---------------------------------------------------------------------------
# Step 1 — Verify the API is reachable
# ---------------------------------------------------------------------------
info "Step 1: checking API health at $API/healthz"

if ! curl -sf "$API/healthz" >/dev/null 2>&1; then
  # /healthz is on the obs port (:9090). Try the main port as a fallback.
  if ! curl -sf "$API/v1/domains" -H "$AUTH_HEADER" -o /dev/null 2>&1; then
    die "Cannot reach $API — is apid running? (make run-apid or make up-app)"
  fi
fi
ok "API is reachable"

# ---------------------------------------------------------------------------
# Step 2 — List domains
# ---------------------------------------------------------------------------
info "Step 2: GET /v1/domains"

domains_json=$(curl -sf -H "$AUTH_HEADER" "$API/v1/domains") || \
  die "GET /v1/domains failed — check API_TOKEN and that apid is running"

printf "\n--- domains response ---\n"
printf '%s' "$domains_json" | pretty
printf "\n"

# Extract the first domain id
if [ "$HAS_JQ" -eq 1 ]; then
  DOMAIN_ID=$(printf '%s' "$domains_json" | jq -r '.domains[0].id // empty')
  DOMAIN_NAME=$(printf '%s' "$domains_json" | jq -r '.domains[0].name // empty')
else
  # Crude extraction: first occurrence of a UUID-shaped value after "id"
  DOMAIN_ID=$(printf '%s' "$domains_json" \
    | grep -o '"id"[[:space:]]*:[[:space:]]*"[0-9a-f-]*"' \
    | head -1 | sed 's/.*"\([0-9a-f-]*\)"/\1/')
  DOMAIN_NAME=$(printf '%s' "$domains_json" \
    | grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | head -1 | sed 's/.*"\([^"]*\)"/\1/')
fi

if [ -z "$DOMAIN_ID" ] || [ "$DOMAIN_ID" = "null" ]; then
  die "No domains found. Run 'make seed' (Path B) or check that migrate completed (Path A)."
fi

ok "Found domain: $DOMAIN_NAME (id=$DOMAIN_ID)"

# ---------------------------------------------------------------------------
# Step 3 — Trigger a DNS recheck
# ---------------------------------------------------------------------------
info "Step 3: POST /v1/domains/$DOMAIN_ID/dns/recheck"

recheck_json=$(curl -sf -X POST -H "$AUTH_HEADER" \
  "$API/v1/domains/$DOMAIN_ID/dns/recheck") || \
  die "POST dns/recheck failed (id=$DOMAIN_ID)"

printf "\n--- recheck response ---\n"
printf '%s' "$recheck_json" | pretty
printf "\n"

if [ "$HAS_JQ" -eq 1 ]; then
  overall=$(printf '%s' "$recheck_json" | jq -r '.overall // "unknown"')
  changed=$(printf '%s' "$recheck_json" | jq -r '.changed // false')
  finding_count=$(printf '%s' "$recheck_json" | jq '.findings | length')
else
  overall="(see JSON above)"
  changed="(see JSON above)"
  finding_count="(see JSON above)"
fi

ok "Recheck complete — overall=$overall  changed=$changed  findings=$finding_count"

if [ "$HAS_JQ" -eq 1 ] && [ "$overall" = "healthy" ]; then
  warn "Domain is fully healthy — no dns.validation_failed event will be emitted."
  warn "incidentd will not create an incident for a passing domain."
  warn "Consider testing with a domain that has a missing DKIM/SPF record."
  warn "Continuing to poll for any pre-existing incidents..."
fi

# ---------------------------------------------------------------------------
# Step 4 — Poll GET /v1/incidents until ai_summary is populated
# ---------------------------------------------------------------------------
info "Step 4: polling GET /v1/incidents (up to ${MAX_WAIT}s, every ${POLL_INTERVAL}s)"
info "Waiting for incidentd to record an incident and aid to add AI analysis..."
printf "\n"

elapsed=0
INCIDENT_ID=""
AI_SUMMARY=""

while [ "$elapsed" -lt "$MAX_WAIT" ]; do
  incidents_json=$(curl -sf -H "$AUTH_HEADER" "$API/v1/incidents") || {
    warn "GET /v1/incidents failed — retrying in ${POLL_INTERVAL}s"
    sleep "$POLL_INTERVAL"
    elapsed=$((elapsed + POLL_INTERVAL))
    continue
  }

  if [ "$HAS_JQ" -eq 1 ]; then
    incident_count=$(printf '%s' "$incidents_json" | jq '.incidents | length')
    AI_SUMMARY=$(printf '%s' "$incidents_json" \
      | jq -r '.incidents[0].ai_summary // empty' 2>/dev/null || true)
    INCIDENT_ID=$(printf '%s' "$incidents_json" \
      | jq -r '.incidents[0].id // empty' 2>/dev/null || true)
  else
    # Without jq: check whether ai_summary appears as a non-null string in the raw JSON.
    AI_SUMMARY=$(printf '%s' "$incidents_json" \
      | grep -o '"ai_summary"[[:space:]]*:[[:space:]]*"[^"]*"' \
      | head -1 | sed 's/.*"\([^"]*\)"/\1/' || true)
    incident_count="(see JSON)"
    INCIDENT_ID="(see JSON)"
  fi

  if [ -n "$AI_SUMMARY" ] && [ "$AI_SUMMARY" != "null" ]; then
    break
  fi

  printf "  [%3ds] incidents=%s  ai_summary=%s — waiting...\n" \
    "$elapsed" "$incident_count" "${AI_SUMMARY:-null}"
  sleep "$POLL_INTERVAL"
  elapsed=$((elapsed + POLL_INTERVAL))
done

printf "\n"

# ---------------------------------------------------------------------------
# Step 5 — Report result
# ---------------------------------------------------------------------------
if [ -n "$AI_SUMMARY" ] && [ "$AI_SUMMARY" != "null" ]; then
  ok "AI analysis is populated — full pipeline confirmed!"
  printf "\n${BOLD}=== Final incident (with AI fields) ===${RESET}\n\n"
  printf '%s' "$incidents_json" | pretty
  printf "\n"

  if [ "$HAS_JQ" -eq 1 ]; then
    printf "${BOLD}ai_summary:${RESET}\n"
    printf '%s' "$incidents_json" | jq -r '.incidents[0].ai_summary'
    printf "\n${BOLD}ai_remediation:${RESET}\n"
    printf '%s' "$incidents_json" | jq '.incidents[0].ai_remediation'
    printf "\n${BOLD}ai_model:${RESET}    %s\n" \
      "$(printf '%s' "$incidents_json" | jq -r '.incidents[0].ai_model')"
    printf "${BOLD}ai_analyzed_at:${RESET} %s\n" \
      "$(printf '%s' "$incidents_json" | jq -r '.incidents[0].ai_analyzed_at')"
  fi

  printf "\n${GREEN}${BOLD}Shakedown passed.${RESET}\n\n"
  exit 0
else
  # Print whatever incidents exist so the user has something to work with.
  printf "\n${YELLOW}--- current incidents (ai_summary still null) ---${RESET}\n"
  printf '%s' "${incidents_json:-<empty response>}" | pretty
  printf "\n"

  warn "ai_summary was not populated within ${MAX_WAIT}s."
  printf "\nPossible causes:\n"
  printf "  1. No incident was created (domain is healthy, or incidentd is not running).\n"
  printf "     -> Check: make run-incidentd / docker compose logs incidentd\n"
  printf "  2. aid cannot reach the LLM endpoint.\n"
  printf "     -> Check: curl http://localhost:11434/v1/models\n"
  printf "     -> Docker Desktop: MXS_AI_ENDPOINT=http://host.docker.internal:11434/v1 (default)\n"
  printf "     -> Linux Docker:   export MXS_AI_ENDPOINT=http://172.17.0.1:11434/v1\n"
  printf "  3. aid is not running.\n"
  printf "     -> Check: make run-aid / docker compose logs aid\n"
  printf "  4. The model is still loading in Ollama. Wait and re-run this script.\n"
  printf "\nSee docs/shakedown.md for the full troubleshooting guide.\n\n"
  exit 1
fi
