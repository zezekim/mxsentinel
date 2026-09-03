#!/usr/bin/env bash
#
# MX Sentinel self-updater (pull-based, CI-gated).
#
# Primary trigger: the `deploy` job in .github/workflows/ci.yml, which runs on a
# self-hosted runner living on this VPS and invokes this script (MXS_UPDATE_BRANCH=main)
# only after every other CI job — build/vet/test, lint, dashboard build, docker images,
# schema-lint — has passed on a real push to main. That gives push-to-deploy: a commit
# pushed from any machine lands here within a couple of minutes of CI going green.
#
# It can also run standalone (via the mxsentinel-update.timer systemd unit, or cron) against
# any tracked branch, e.g. as a periodic safety net that self-heals a missed/offline runner.
# Either way: it checks whether the tracked branch has moved and, if so, pulls, rebuilds,
# and restarts the stack, then health-checks apid and AUTOMATICALLY ROLLS BACK to the
# previous commit if the new version is unhealthy.
#
# Design goals:
#   - Pull-based: no inbound access to the VPS, no secrets stored in GitHub.
#   - Safe: only deploys commits CI has already gated onto the release branch.
#   - Non-destructive to mail: the Postfix relay is independent of this stack; a failed
#     app update rolls back and never touches mail flow.
#   - Idempotent + single-flighted: a flock prevents overlapping runs.
#
# Configuration (environment variables, all optional):
#   MXS_UPDATE_BRANCH        git branch to track            (default: release)
#   MXS_UPDATE_PROFILES      space-sep compose profiles      (default: app)
#   MXS_UPDATE_HEALTH_URL    readiness URL to probe          (default: http://127.0.0.1:9090/healthz)
#   MXS_UPDATE_HEALTH_TRIES  health poll attempts            (default: 30)
#   MXS_UPDATE_HEALTH_WAIT   seconds between attempts        (default: 5)
#   MXS_UPDATE_ALERT_WEBHOOK optional Slack/webhook URL for deploy success/failure notices
#   MXS_UPDATE_ENV_FILE      path to deploy/.env             (default: <repo>/deploy/.env)
#
# Exit codes: 0 = up to date or deployed-and-healthy; 1 = deploy failed (rolled back or
# rollback also failed — see logs). Journald captures stdout: `journalctl -u mxsentinel-update`.

set -euo pipefail

# ---- resolve paths ---------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

BRANCH="${MXS_UPDATE_BRANCH:-release}"
PROFILES_RAW="${MXS_UPDATE_PROFILES:-app}"
HEALTH_URL="${MXS_UPDATE_HEALTH_URL:-http://127.0.0.1:9090/healthz}"
HEALTH_TRIES="${MXS_UPDATE_HEALTH_TRIES:-30}"
HEALTH_WAIT="${MXS_UPDATE_HEALTH_WAIT:-5}"
ENV_FILE="${MXS_UPDATE_ENV_FILE:-$REPO_ROOT/deploy/.env}"
ALERT_WEBHOOK="${MXS_UPDATE_ALERT_WEBHOOK:-}"

BASE="$REPO_ROOT/deploy/docker-compose.yml"
PROD="$REPO_ROOT/deploy/docker-compose.prod.yml"

# Build the compose base command + profile flags.
COMPOSE=(docker compose -f "$BASE" -f "$PROD" --env-file "$ENV_FILE")
PROFILE_FLAGS=()
for p in $PROFILES_RAW; do PROFILE_FLAGS+=(--profile "$p"); done

log() { printf '%s  %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }

notify() { # notify <ok|fail> <message>
	[ -n "$ALERT_WEBHOOK" ] || return 0
	local status="$1" msg="$2"
	curl -fsS -m 10 -X POST -H 'Content-Type: application/json' \
		--data "{\"text\":\"MX Sentinel auto-update [$status]: $msg\"}" \
		"$ALERT_WEBHOOK" >/dev/null 2>&1 || log "warn: alert webhook post failed"
}

http_ok() { # http_ok <url> -> 0 if it returns HTTP 200
	if command -v curl >/dev/null 2>&1; then
		[ "$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$1" 2>/dev/null || echo 000)" = "200" ]
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O /dev/null -T 5 "$1" 2>/dev/null
	else
		die "neither curl nor wget is available for the health check"
	fi
}

deploy() { # rebuild + (re)start the stack; migrate runs automatically before daemons
	log "building + starting stack (profiles: $PROFILES_RAW)…"
	"${COMPOSE[@]}" "${PROFILE_FLAGS[@]}" up -d --build
}

containers_ok() {
	# A container stuck "restarting" is crash-looping (a real regression) — fail. So is a
	# long-running service that has "exited". We EXCLUDE `migrate`, the one-shot job that
	# legitimately exits 0. A daemon that is merely idle/degraded (e.g. sndsd with no SNDS
	# key) stays "running", so graceful degradation never triggers a rollback.
	local restarting exited
	restarting="$("${COMPOSE[@]}" "${PROFILE_FLAGS[@]}" ps --status restarting --services 2>/dev/null || true)"
	if [ -n "$restarting" ]; then
		log "unhealthy: crash-looping: $(echo "$restarting" | tr '\n' ' ')"
		return 1
	fi
	exited="$("${COMPOSE[@]}" "${PROFILE_FLAGS[@]}" ps -a --status exited --services 2>/dev/null | grep -vx migrate || true)"
	if [ -n "$exited" ]; then
		log "unhealthy: exited: $(echo "$exited" | tr '\n' ' ')"
		return 1
	fi
	return 0
}

health_ok() {
	# Gate 1: apid (the critical surface) must become ready.
	log "waiting for apid to become healthy at $HEALTH_URL (up to $((HEALTH_TRIES * HEALTH_WAIT))s)…"
	local apid_ready=0
	for _ in $(seq 1 "$HEALTH_TRIES"); do
		if http_ok "$HEALTH_URL"; then apid_ready=1; log "apid is healthy."; break; fi
		sleep "$HEALTH_WAIT"
	done
	[ "$apid_ready" = 1 ] || { log "apid did not become healthy in time."; return 1; }

	# Gate 2: no daemon is crash-looping (give the stack a few cycles to settle first).
	log "checking that no daemon is crash-looping…"
	for _ in 1 2 3 4 5 6; do
		if containers_ok; then log "all containers are running (none crash-looping)."; return 0; fi
		sleep "$HEALTH_WAIT"
	done
	return 1
}

# ---- single-flight lock ----------------------------------------------------------------
LOCK_FILE="${TMPDIR:-/tmp}/mxsentinel-self-update.lock"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then log "another self-update run is in progress — exiting."; exit 0; fi

# ---- preflight -------------------------------------------------------------------------
command -v git >/dev/null 2>&1 || die "git not found"
command -v docker >/dev/null 2>&1 || die "docker not found"
[ -f "$ENV_FILE" ] || die "env file not found: $ENV_FILE (run the installer first)"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "$REPO_ROOT is not a git repository"

# ---- check for updates -----------------------------------------------------------------
log "fetching origin/$BRANCH…"
git fetch --quiet origin "$BRANCH" || die "git fetch failed"

# Until CI has run on main at least once, the release branch may not exist yet. That's not
# an error — just nothing to track. Exit cleanly; the timer will pick it up once published.
if ! git rev-parse --verify --quiet "origin/$BRANCH" >/dev/null; then
	log "branch origin/$BRANCH not published yet (CI advances it on green main) — nothing to do."
	exit 0
fi

LOCAL="$(git rev-parse HEAD)"
REMOTE="$(git rev-parse "origin/$BRANCH")"

if [ "$LOCAL" = "$REMOTE" ]; then
	log "already up to date ($BRANCH @ ${LOCAL:0:12}) — nothing to do."
	exit 0
fi

log "update available: ${LOCAL:0:12} -> ${REMOTE:0:12} on $BRANCH"
PREV="$LOCAL"   # rollback target

# ---- apply -----------------------------------------------------------------------------
# Match the deploy tree exactly to origin/<branch>. Untracked files (deploy/.env, drop
# dirs) are preserved; only tracked files are reset.
git checkout -q "$BRANCH" 2>/dev/null || git checkout -q -B "$BRANCH" "origin/$BRANCH"
git reset --hard "origin/$BRANCH"

if deploy && health_ok; then
	NEW="$(git rev-parse HEAD)"
	log "update deployed and healthy: now at ${NEW:0:12} on $BRANCH"
	notify ok "deployed ${PREV:0:12} -> ${NEW:0:12} on $BRANCH"
	# Prune dangling build layers to keep disk in check (best-effort).
	docker image prune -f >/dev/null 2>&1 || true
	exit 0
fi

# ---- rollback --------------------------------------------------------------------------
log "new version FAILED health check — rolling back to ${PREV:0:12}…"
git reset --hard "$PREV"
if deploy && health_ok; then
	log "rollback complete — restored ${PREV:0:12} and it is healthy."
	notify fail "update to ${REMOTE:0:12} failed health check; rolled back to ${PREV:0:12} (healthy)"
	exit 1
fi

log "CRITICAL: rollback to ${PREV:0:12} is ALSO unhealthy — manual intervention required."
notify fail "CRITICAL: update AND rollback both unhealthy — manual intervention required on the VPS"
exit 1
