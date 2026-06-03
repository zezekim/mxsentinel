#!/usr/bin/env bash
#
# MX Sentinel — interactive installer.
#
# Run on your VPS from the cloned repo:
#     bash deploy/install.sh
#
# It asks for your domain and settings, generates strong secrets into deploy/.env,
# launches the full stack behind Caddy (automatic TLS), and bootstraps your tenant +
# owner login. It does NOT install Docker or Postfix — Docker must already be present,
# and the mail relay (Postfix) is a separate concern (see docs/deploy-relay.md).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

BASE="deploy/docker-compose.yml"
PROD="deploy/docker-compose.prod.yml"
ENV_FILE="deploy/.env"

# ---- output helpers --------------------------------------------------------
bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '\033[33m! %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

ask() { # ask <var> <prompt> [default]
	local __var="$1" __prompt="$2" __def="${3:-}" __in
	if [ -n "$__def" ]; then
		read -rp "  $__prompt [$__def]: " __in || true
		__in="${__in:-$__def}"
	else
		read -rp "  $__prompt: " __in || true
	fi
	printf -v "$__var" '%s' "$__in"
}

ask_secret() { # ask_secret <var> <prompt>   (hidden, may be empty)
	local __var="$1" __prompt="$2" __in
	read -rsp "  $__prompt: " __in || true
	echo
	printf -v "$__var" '%s' "$__in"
}

yesno() { # yesno <prompt> <default:y|n>  -> 0 if yes
	local __prompt="$1" __def="${2:-n}" __hint="[y/N]" __in
	if [ "$__def" = "y" ]; then __hint="[Y/n]"; fi
	read -rp "  $__prompt $__hint: " __in || true
	__in="${__in:-$__def}"
	case "$__in" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

gen_secret() { openssl rand -hex 24; }

# ---- preflight -------------------------------------------------------------
bold "MX Sentinel installer"
[ -f "$BASE" ] || die "must run from the repo root ($BASE not found)"
command -v docker >/dev/null 2>&1 || die "Docker not found. Install it first: curl -fsSL https://get.docker.com | sh"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 not found (need the 'docker compose' plugin)"
command -v openssl >/dev/null 2>&1 || die "openssl not found"

COMPOSE=(docker compose -f "$BASE" -f "$PROD" --env-file "$ENV_FILE")

REUSE_ENV=0
if [ -f "$ENV_FILE" ]; then
	if yesno "$ENV_FILE exists. Reuse it and just (re)deploy?" y; then
		REUSE_ENV=1
		# shellcheck disable=SC1090
		set -a; . "$ENV_FILE"; set +a
		RELAY=0; [ -n "${RELAY_NODE_IP:-}" ] && RELAY=1
	else
		cp "$ENV_FILE" "$ENV_FILE.bak.$(date +%s)"
		warn "backed up existing $ENV_FILE"
	fi
fi

# ---- collect settings ------------------------------------------------------
if [ "$REUSE_ENV" -eq 0 ]; then
	bold "Settings"
	ask DOMAIN "Public domain (A record must point at this server)"
	[ -n "$DOMAIN" ] || die "a domain is required"
	ask ACME_EMAIL "Email for Let's Encrypt TLS notices"
	[ -n "$ACME_EMAIL" ] || die "an email is required"

	default_slug="$(printf '%s' "$DOMAIN" | cut -d. -f1)"
	ask ORG_NAME "Organization / tenant name" "$default_slug"
	ask TENANT_SLUG "Tenant slug (unique, lowercase)" "$default_slug"
	ask OWNER_EMAIL "Owner login email" "admin@$DOMAIN"
	ask_secret OWNER_PASSWORD "Owner password (leave blank to auto-generate)"
	OWNER_PW_GENERATED=0
	if [ -z "$OWNER_PASSWORD" ]; then
		OWNER_PASSWORD="$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | cut -c1-20)"
		OWNER_PW_GENERATED=1
	fi

	bold "AI diagnostics model (local LLM via Ollama)"
	info "1) llama3.2:3b   recommended for 8 GB (~2-3 GB)"
	info "2) llama3        8B, better quality, tight on 8 GB (~5-6 GB)"
	info "3) remote        point at an external OpenAI-compatible endpoint"
	info "4) none          skip AI (incidents still record, no narratives)"
	ask AI_CHOICE "Choose" "1"
	AI_ENDPOINT="http://host.docker.internal:11434/v1"
	case "$AI_CHOICE" in
		2) AI_MODEL="llama3" ;;
		3) ask AI_ENDPOINT "Remote LLM base URL" "$AI_ENDPOINT"; ask AI_MODEL "Model name" "llama3.2:3b" ;;
		4) AI_MODEL=""; AI_ENDPOINT="" ;;
		*) AI_MODEL="llama3.2:3b" ;;
	esac

	RELAY=0; RELAY_NODE_IP=""; MAILLOG_PATH="/var/log/mail.log"
	if yesno "Is the Postfix mail relay running on THIS host?" n; then
		RELAY=1
		default_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
		ask RELAY_NODE_IP "Relay primary IP" "$default_ip"
		ask MAILLOG_PATH "Postfix maillog path" "/var/log/mail.log"
	fi

	PG_PASSWORD="$(gen_secret)"
	MINIO_ROOT_PASSWORD="$(gen_secret)"
	MXS_TELEMETRY_HASHKEY="$(gen_secret)"

	bold "Summary"
	info "Domain:       $DOMAIN   (TLS email: $ACME_EMAIL)"
	info "Tenant:       $ORG_NAME (slug: $TENANT_SLUG)"
	info "Owner:        $OWNER_EMAIL"
	info "AI:           ${AI_MODEL:-disabled}${AI_ENDPOINT:+ @ $AI_ENDPOINT}"
	if [ "$RELAY" -eq 1 ]; then info "Relay on box: yes (ip ${RELAY_NODE_IP:-?}, log $MAILLOG_PATH)"; else info "Relay on box: no"; fi
	info "Secrets:      auto-generated -> $ENV_FILE"
	yesno "Write $ENV_FILE and deploy now?" y || die "aborted by user"

	umask 077
	cat > "$ENV_FILE" <<EOF
# Generated by deploy/install.sh on $(date -u '+%Y-%m-%dT%H:%M:%SZ'). Keep private; never commit.
MXS_DOMAIN=$DOMAIN
MXS_ACME_EMAIL=$ACME_EMAIL
PG_USER=mxsentinel
PG_PASSWORD=$PG_PASSWORD
PG_DB=mxsentinel
CH_DB=mxsentinel
MINIO_ROOT_USER=mxsentinel
MINIO_ROOT_PASSWORD=$MINIO_ROOT_PASSWORD
MINIO_BUCKET=mxsentinel
MXS_AI_ENDPOINT=$AI_ENDPOINT
MXS_AI_MODEL=$AI_MODEL
RELAY_NODE_IP=$RELAY_NODE_IP
MAILLOG_PATH=$MAILLOG_PATH
MXS_TELEMETRY_HASHKEY=$MXS_TELEMETRY_HASHKEY
MXS_LOGLEVEL=info
EOF
	chmod 600 "$ENV_FILE"
	info "wrote $ENV_FILE"
fi

# ---- deploy ----------------------------------------------------------------
PROFILES=(--profile app)
if [ "${RELAY:-0}" -eq 1 ]; then PROFILES+=(--profile relay); fi

bold "Deploying (building images — the first run takes a few minutes)…"
"${COMPOSE[@]}" "${PROFILES[@]}" up -d --build

# one-shot mxctl runner inside the app image
mxctl() { "${COMPOSE[@]}" --profile app run --rm -T apid /usr/local/bin/mxctl "$@"; }

# ---- bootstrap (only for a fresh config) -----------------------------------
if [ "$REUSE_ENV" -eq 0 ]; then
	bold "Bootstrapping tenant + owner (waiting for the database to be ready)…"
	created=0
	for i in $(seq 1 40); do
		if mxctl tenant create --name "$ORG_NAME" --slug "$TENANT_SLUG" >/dev/null 2>&1; then
			info "tenant '$TENANT_SLUG' created"; created=1; break
		fi
		sleep 3
	done
	[ "$created" -eq 1 ] || warn "could not create tenant (it may already exist) — continuing"

	if mxctl user create --tenant "$TENANT_SLUG" --email "$OWNER_EMAIL" --password "$OWNER_PASSWORD" --role owner >/dev/null 2>&1; then
		info "owner '$OWNER_EMAIL' created"
	else
		warn "could not create owner user (it may already exist)"
	fi

	if [ "${RELAY:-0}" -eq 1 ]; then
		if yesno "Register a sending IP pool now?" n; then
			ask POOL_NAME "Pool name" "transactional"
			ask POOL_PURPOSE "Purpose (transactional|marketing|warmup|mixed)" "transactional"
			ask POOL_ADDRS "Pool IPs (comma-separated)"
			if [ -n "$POOL_ADDRS" ]; then
				mxctl ip-pool create --tenant "$TENANT_SLUG" --name "$POOL_NAME" --purpose "$POOL_PURPOSE" --addresses "$POOL_ADDRS" || warn "ip-pool create failed"
			fi
		fi
		mxctl relay-node add --tenant "$TENANT_SLUG" --hostname "$DOMAIN" --ip "${RELAY_NODE_IP:-}" --software postfix >/dev/null 2>&1 || true
	fi
fi

# ---- optional: pull the local LLM model ------------------------------------
if [ -n "${AI_MODEL:-}" ] && [ "${AI_ENDPOINT:-}" = "http://host.docker.internal:11434/v1" ]; then
	if command -v ollama >/dev/null 2>&1; then
		if yesno "Pull Ollama model '$AI_MODEL' now?" y; then
			ollama pull "$AI_MODEL" || warn "ollama pull failed — run it manually later"
		fi
	else
		warn "Ollama not installed. For AI narratives: install Ollama, then 'ollama pull ${AI_MODEL}'."
	fi
fi

# ---- done ------------------------------------------------------------------
bold "Done ✓"
info "Dashboard:  https://${MXS_DOMAIN:-$DOMAIN}/login"
if [ "${OWNER_PW_GENERATED:-0}" -eq 1 ]; then
	info "Owner password (SAVE THIS NOW): $OWNER_PASSWORD"
fi
info "Config/secrets live in $ENV_FILE (private; never commit)."
info "Tail logs:  docker compose -f $BASE -f $PROD --env-file $ENV_FILE ${PROFILES[*]} logs -f apid aid"
info "Caddy issues TLS automatically once the domain resolves here and ports 80/443 are open."
if [ "${RELAY:-0}" -eq 1 ]; then
	info "Relay: finish Postfix + DNS (PTR/SPF/DKIM) per docs/deploy-relay.md."
fi
