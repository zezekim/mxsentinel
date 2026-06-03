#!/usr/bin/env bash
#
# MX Sentinel — all-in-one installer for a fresh Debian/Ubuntu VPS.
#
#     sudo bash deploy/install.sh            # full provision + deploy
#     bash deploy/install.sh --app-only      # skip OS provisioning (Docker etc. already present)
#
# It installs every dependency (Docker, optionally Ollama, optionally Postfix+OpenDKIM),
# configures a firewall, then deploys the MX Sentinel stack behind Caddy (automatic TLS)
# and bootstraps your tenant + owner login. DNS records it cannot publish for you
# (PTR/SPF/DKIM/DMARC) are generated and printed at the end.
#
# It does NOT touch your network config (assigning the extra IPs in netplan is left to you
# — getting it wrong can lock you out). See docs/deploy-relay.md for the advanced relay
# (multi-IP sender pools) setup.
set -euo pipefail

APP_ONLY=0
for arg in "$@"; do
	case "$arg" in
		--app-only) APP_ONLY=1 ;;
		-h|--help) sed -n '2,18p' "$0"; exit 0 ;;
		*) echo "unknown flag: $arg" >&2; exit 2 ;;
	esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

BASE="deploy/docker-compose.yml"
PROD="deploy/docker-compose.prod.yml"
ENV_FILE="deploy/.env"
DKIM_SELECTOR="mxs"

# ---- output + prompt helpers ----------------------------------------------
bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '\033[33m! %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

ask() { # ask <var> <prompt> [default]
	local __var="$1" __prompt="$2" __def="${3:-}" __in
	if [ -n "$__def" ]; then read -rp "  $__prompt [$__def]: " __in || true; __in="${__in:-$__def}"
	else read -rp "  $__prompt: " __in || true; fi
	printf -v "$__var" '%s' "$__in"
}
ask_secret() { local __var="$1" __prompt="$2" __in; read -rsp "  $__prompt: " __in || true; echo; printf -v "$__var" '%s' "$__in"; }
yesno() { # yesno <prompt> <default:y|n>
	local __prompt="$1" __def="${2:-n}" __hint="[y/N]" __in
	[ "$__def" = "y" ] && __hint="[Y/n]"
	read -rp "  $__prompt $__hint: " __in || true; __in="${__in:-$__def}"
	case "$__in" in [Yy]*) return 0 ;; *) return 1 ;; esac
}
gen_secret() { openssl rand -hex 24; }

# ---- preflight -------------------------------------------------------------
bold "MX Sentinel installer"
[ -f "$BASE" ] || die "run from the repo root ($BASE not found)"

OS_ID=""; OS_LIKE=""
if [ -r /etc/os-release ]; then
	# shellcheck source=/dev/null
	. /etc/os-release
	OS_ID="${ID:-}"; OS_LIKE="${ID_LIKE:-}"
fi
is_debian() { [ "$OS_ID" = "debian" ] || [ "$OS_ID" = "ubuntu" ] || case "$OS_LIKE" in *debian*) true ;; *) false ;; esac; }

if [ "$APP_ONLY" -eq 0 ]; then
	[ "$(id -u)" -eq 0 ] || die "full install needs root — run: sudo bash deploy/install.sh (or use --app-only)"
	is_debian || die "automatic provisioning supports Debian/Ubuntu. On another OS, install Docker (+ Postfix) yourself and re-run with --app-only."
fi
have openssl || die "openssl not found"

# ---- collect settings ------------------------------------------------------
REUSE_ENV=0
if [ -f "$ENV_FILE" ]; then
	if yesno "$ENV_FILE exists. Reuse it and just (re)deploy?" y; then
		REUSE_ENV=1
		set -a
		# shellcheck source=/dev/null
		. "$ENV_FILE"
		set +a
		RELAY=0; [ -n "${RELAY_NODE_IP:-}" ] && RELAY=1
		DOMAIN="${MXS_DOMAIN:-}"; AI_MODEL="${MXS_AI_MODEL:-}"; AI_ENDPOINT="${MXS_AI_ENDPOINT:-}"
		MAIL_DOMAIN="${MAIL_DOMAIN:-$DOMAIN}"
	else
		cp "$ENV_FILE" "$ENV_FILE.bak.$(date +%s)"; warn "backed up existing $ENV_FILE"
	fi
fi

if [ "$REUSE_ENV" -eq 0 ]; then
	bold "Settings"
	ask DOMAIN "Public domain for the dashboard/API (A record -> this server)"
	[ -n "$DOMAIN" ] || die "a domain is required"
	ask ACME_EMAIL "Email for Let's Encrypt TLS"
	[ -n "$ACME_EMAIL" ] || die "an email is required"

	default_slug="$(printf '%s' "$DOMAIN" | cut -d. -f1)"
	ask ORG_NAME "Organization / tenant name" "$default_slug"
	ask TENANT_SLUG "Tenant slug (unique, lowercase)" "$default_slug"
	ask OWNER_EMAIL "Owner login email" "admin@$DOMAIN"
	ask_secret OWNER_PASSWORD "Owner password (blank = auto-generate)"
	OWNER_PW_GENERATED=0
	if [ -z "$OWNER_PASSWORD" ]; then OWNER_PASSWORD="$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | cut -c1-20)"; OWNER_PW_GENERATED=1; fi

	bold "AI diagnostics model (local LLM via Ollama)"
	info "1) llama3.2:3b   recommended for 8 GB"
	info "2) llama3        8B, tight on 8 GB"
	info "3) remote        external OpenAI-compatible endpoint"
	info "4) none"
	ask AI_CHOICE "Choose" "1"
	AI_ENDPOINT="http://host.docker.internal:11434/v1"; AI_LOCAL=1
	case "$AI_CHOICE" in
		2) AI_MODEL="llama3" ;;
		3) AI_LOCAL=0; ask AI_ENDPOINT "Remote LLM base URL" "$AI_ENDPOINT"; ask AI_MODEL "Model name" "llama3.2:3b" ;;
		4) AI_MODEL=""; AI_ENDPOINT=""; AI_LOCAL=0 ;;
		*) AI_MODEL="llama3.2:3b" ;;
	esac

	RELAY=0; RELAY_NODE_IP=""; MAILLOG_PATH="/var/log/mail.log"; MAIL_DOMAIN=""
	if yesno "Run the Postfix mail relay on THIS host (install + base config)?" n; then
		RELAY=1
		default_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
		ask RELAY_NODE_IP "Relay primary IP" "$default_ip"
		ask MAIL_DOMAIN "Primary sending domain (for DKIM)" "$DOMAIN"
	fi

	PG_PASSWORD="$(gen_secret)"; MINIO_ROOT_PASSWORD="$(gen_secret)"; MXS_TELEMETRY_HASHKEY="$(gen_secret)"

	bold "Summary"
	info "Domain:        $DOMAIN  (TLS: $ACME_EMAIL)"
	info "Tenant/owner:  $ORG_NAME ($TENANT_SLUG) / $OWNER_EMAIL"
	info "AI:            ${AI_MODEL:-disabled}${AI_ENDPOINT:+ @ $AI_ENDPOINT}"
	if [ "$RELAY" -eq 1 ]; then info "Relay on box:  yes — install Postfix+OpenDKIM, sign $MAIL_DOMAIN, ip ${RELAY_NODE_IP:-?}"; else info "Relay on box:  no"; fi
	[ "$APP_ONLY" -eq 1 ] && info "Mode:          --app-only (no OS provisioning)"
	yesno "Proceed?" y || die "aborted by user"

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
MAIL_DOMAIN=$MAIL_DOMAIN
MAILLOG_PATH=$MAILLOG_PATH
MXS_TELEMETRY_HASHKEY=$MXS_TELEMETRY_HASHKEY
MXS_LOGLEVEL=info
EOF
	chmod 600 "$ENV_FILE"
	info "wrote $ENV_FILE"
fi

# ---- OS provisioning -------------------------------------------------------
apt_install() { DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" >/dev/null; }

provision_base() {
	bold "Installing base packages…"
	apt-get update -qq
	apt_install ca-certificates curl gnupg openssl jq ufw
}

provision_docker() {
	if have docker && docker compose version >/dev/null 2>&1; then info "Docker already present"; return; fi
	bold "Installing Docker…"
	curl -fsSL https://get.docker.com | sh >/dev/null
	systemctl enable --now docker >/dev/null 2>&1 || true
}

provision_ollama() {
	if ! have ollama; then
		bold "Installing Ollama…"
		curl -fsSL https://ollama.com/install.sh | sh >/dev/null
	fi
	# Make Ollama reachable from containers (host.docker.internal -> host-gateway).
	mkdir -p /etc/systemd/system/ollama.service.d
	printf '[Service]\nEnvironment="OLLAMA_HOST=0.0.0.0"\n' > /etc/systemd/system/ollama.service.d/override.conf
	systemctl daemon-reload >/dev/null 2>&1 || true
	systemctl enable --now ollama >/dev/null 2>&1 || true
	systemctl restart ollama >/dev/null 2>&1 || true
	bold "Pulling model $AI_MODEL (this can take a few minutes)…"
	ollama pull "$AI_MODEL" || warn "ollama pull failed — run 'ollama pull $AI_MODEL' later"
}

provision_postfix() {
	bold "Installing Postfix + OpenDKIM…"
	echo "postfix postfix/main_mailer_type select Internet Site" | debconf-set-selections
	echo "postfix postfix/mailname string $DOMAIN" | debconf-set-selections
	apt_install postfix opendkim opendkim-tools

	# Base outbound-relay config (idempotent via postconf; back up first).
	cp -n /etc/postfix/main.cf "/etc/postfix/main.cf.bak.$(date +%s)" 2>/dev/null || true
	postconf -e \
		"myhostname = $DOMAIN" \
		"inet_interfaces = all" \
		"inet_protocols = ipv4" \
		"mydestination = localhost" \
		"relayhost =" \
		"smtp_tls_security_level = may" \
		"smtpd_tls_security_level = may" \
		"smtp_tls_loglevel = 1" \
		"message_size_limit = 52428800" \
		"maillog_file = $MAILLOG_PATH" \
		"milter_default_action = accept" \
		"milter_protocol = 6" \
		"smtpd_milters = inet:localhost:8891" \
		"non_smtpd_milters = inet:localhost:8891"

	# OpenDKIM: generate a 2048-bit key for the sending domain and wire the milter.
	local keydir="/etc/opendkim/keys/$MAIL_DOMAIN"
	mkdir -p "$keydir" /run/opendkim
	if [ ! -f "$keydir/$DKIM_SELECTOR.private" ]; then
		opendkim-genkey -b 2048 -s "$DKIM_SELECTOR" -d "$MAIL_DOMAIN" -D "$keydir"
	fi
	cp -n /etc/opendkim.conf "/etc/opendkim.conf.bak.$(date +%s)" 2>/dev/null || true
	cat > /etc/opendkim.conf <<EOF
Syslog yes
UMask 002
Mode sv
Socket inet:8891@localhost
PidFile /run/opendkim/opendkim.pid
UserID opendkim
Canonicalization relaxed/simple
OversignHeaders From
KeyTable refile:/etc/opendkim/key.table
SigningTable refile:/etc/opendkim/signing.table
ExternalIgnoreList /etc/opendkim/trusted.hosts
InternalHosts /etc/opendkim/trusted.hosts
EOF
	echo "$DKIM_SELECTOR._domainkey.$MAIL_DOMAIN $MAIL_DOMAIN:$DKIM_SELECTOR:$keydir/$DKIM_SELECTOR.private" > /etc/opendkim/key.table
	echo "*@$MAIL_DOMAIN $DKIM_SELECTOR._domainkey.$MAIL_DOMAIN" > /etc/opendkim/signing.table
	printf '127.0.0.1\n::1\nlocalhost\n%s\n' "$MAIL_DOMAIN" > /etc/opendkim/trusted.hosts
	chown -R opendkim:opendkim /etc/opendkim /run/opendkim
	chmod 600 "$keydir/$DKIM_SELECTOR.private"
	systemctl enable opendkim >/dev/null 2>&1 || true
	systemctl restart opendkim
	systemctl restart postfix
	info "Postfix + OpenDKIM configured (signing $MAIL_DOMAIN, selector $DKIM_SELECTOR)"
}

provision_firewall() {
	have ufw || return 0
	bold "Configuring firewall (ufw)…"
	ufw allow 22/tcp  >/dev/null 2>&1 || true
	ufw allow 80/tcp  >/dev/null 2>&1 || true
	ufw allow 443/tcp >/dev/null 2>&1 || true
	if [ "${RELAY:-0}" -eq 1 ]; then
		ufw allow 25/tcp  >/dev/null 2>&1 || true
		ufw allow 587/tcp >/dev/null 2>&1 || true
	fi
	ufw --force enable >/dev/null 2>&1 || true
	info "firewall: 22/80/443$([ "${RELAY:-0}" -eq 1 ] && echo "/25/587") allowed"
}

# Provision the OS only on a fresh config. Reusing an existing .env means "just
# redeploy" — the box is already provisioned (and we don't have the original answers).
if [ "$APP_ONLY" -eq 0 ] && [ "$REUSE_ENV" -eq 0 ]; then
	provision_base
	provision_docker
	{ [ "${AI_LOCAL:-0}" -eq 1 ] && [ -n "${AI_MODEL:-}" ]; } && provision_ollama
	[ "${RELAY:-0}" -eq 1 ] && provision_postfix
	provision_firewall
else
	have docker || die "Docker is required (run without --app-only on a fresh box to auto-install it)"
	docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
fi

# ---- deploy ----------------------------------------------------------------
COMPOSE=(docker compose -f "$BASE" -f "$PROD" --env-file "$ENV_FILE")
PROFILES=(--profile app)
[ "${RELAY:-0}" -eq 1 ] && PROFILES+=(--profile relay)

# A fresh (non-reuse) run generates NEW database credentials. Docker only applies a DB
# password when it first creates the data volume, so volumes left over from an earlier
# run still carry the OLD password — migrate would then fail with a password-auth error.
# Detect leftover volumes and offer to reset them for a clean install.
if [ "$REUSE_ENV" -eq 0 ] && docker volume ls -q 2>/dev/null | grep -q "^mxsentinel_"; then
	warn "Found existing 'mxsentinel_*' data volumes from a previous run."
	warn "This run generated fresh credentials, which will NOT match those volumes (migrate would fail to authenticate)."
	if yesno "Reset (delete) the existing data volumes for a clean install?" y; then
		"${COMPOSE[@]}" "${PROFILES[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
		info "stale volumes removed — Postgres will re-initialize with the new credentials"
	else
		warn "Keeping volumes — if deploy fails on a password-auth error, re-run and choose to reset."
	fi
fi

bold "Deploying MX Sentinel (building images — first run takes a few minutes)…"
"${COMPOSE[@]}" "${PROFILES[@]}" up -d --build

mxctl() { "${COMPOSE[@]}" --profile app run --rm -T apid /usr/local/bin/mxctl "$@"; }

if [ "$REUSE_ENV" -eq 0 ]; then
	bold "Bootstrapping tenant + owner (waiting for the database)…"
	created=0
	for _ in $(seq 1 40); do
		if mxctl tenant create --name "$ORG_NAME" --slug "$TENANT_SLUG" >/dev/null 2>&1; then created=1; break; fi
		sleep 3
	done
	if [ "$created" -eq 1 ]; then info "tenant '$TENANT_SLUG' created"; else warn "tenant create did not succeed (may already exist)"; fi
	if mxctl user create --tenant "$TENANT_SLUG" --email "$OWNER_EMAIL" --password "$OWNER_PASSWORD" --role owner >/dev/null 2>&1; then
		info "owner '$OWNER_EMAIL' created"
	else
		warn "owner create failed (may already exist)"
	fi
	if [ "${RELAY:-0}" -eq 1 ]; then
		mxctl relay-node add --tenant "$TENANT_SLUG" --hostname "$DOMAIN" --ip "${RELAY_NODE_IP:-}" --software postfix >/dev/null 2>&1 || true
		if yesno "Register a sending IP pool now?" n; then
			ask POOL_NAME "Pool name" "transactional"
			ask POOL_PURPOSE "Purpose (transactional|marketing|warmup|mixed)" "transactional"
			ask POOL_ADDRS "Pool IPs (comma-separated)"
			[ -n "$POOL_ADDRS" ] && { mxctl ip-pool create --tenant "$TENANT_SLUG" --name "$POOL_NAME" --purpose "$POOL_PURPOSE" --addresses "$POOL_ADDRS" || warn "ip-pool create failed"; }
		fi
	fi
else
	# Reused .env: credentials aren't stored, so we can't bootstrap. If you don't yet
	# have a login (e.g. a previous run failed before this step), create one now:
	info "Reused existing config — skipped tenant/owner bootstrap."
	info "  No login yet? Create one:"
	info "    docker compose -f $BASE -f $PROD --env-file $ENV_FILE --profile app run --rm apid \\"
	info "      /usr/local/bin/mxctl tenant create --name <Org> --slug <slug>"
	info "    docker compose -f $BASE -f $PROD --env-file $ENV_FILE --profile app run --rm apid \\"
	info "      /usr/local/bin/mxctl user create --tenant <slug> --email <you> --password <pw> --role owner"
fi

# ---- summary + DNS records to publish --------------------------------------
bold "Done ✓"
info "Dashboard:  https://${MXS_DOMAIN:-$DOMAIN}/login"
[ "${OWNER_PW_GENERATED:-0}" -eq 1 ] && info "Owner password (SAVE THIS NOW): $OWNER_PASSWORD"
info "Config/secrets: $ENV_FILE (private; never commit)"
info "Logs: docker compose -f $BASE -f $PROD --env-file $ENV_FILE ${PROFILES[*]} logs -f apid aid"
info "Caddy issues TLS automatically once $DOMAIN resolves here and 80/443 are open."

if [ "${RELAY:-0}" -eq 1 ] && [ "$APP_ONLY" -eq 0 ] && [ "$REUSE_ENV" -eq 0 ]; then
	bold "DNS records to publish for $MAIL_DOMAIN (the installer cannot set these for you)"
	info "1. PTR / reverse DNS for each sending IP -> a hostname under $MAIL_DOMAIN (set at your VPS provider)."
	info "2. SPF  (TXT @):   v=spf1 ip4:${RELAY_NODE_IP:-YOUR_IP} -all   (add every sending IP as ip4:)"
	info "3. DMARC (TXT _dmarc):  v=DMARC1; p=none; rua=mailto:dmarc@$MAIL_DOMAIN"
	info "4. DKIM (TXT $DKIM_SELECTOR._domainkey) — published key below:"
	echo
	cat "/etc/opendkim/keys/$MAIL_DOMAIN/$DKIM_SELECTOR.txt" 2>/dev/null || warn "DKIM record file not found"
	echo
	info "Multi-IP sender pools, SASL submission auth, and warmup: see docs/deploy-relay.md."
fi
