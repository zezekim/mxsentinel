#!/usr/bin/env bash
#
# MX Sentinel — all-in-one installer for a fresh Debian/Ubuntu VPS.
#
#     sudo bash deploy/install.sh                    # full provision + deploy
#     bash deploy/install.sh --app-only              # skip OS provisioning (Docker etc. already present)
#     sudo bash deploy/install.sh --wire-relay-sasl  # (re-)wire relay SASL on an existing box, then exit
#     sudo bash deploy/install.sh --wire-relay-spam  # (re-)install rspamd + ClamAV outbound filtering, then exit
#     sudo bash deploy/install.sh --wire-ip-rotation # rotate outbound across a list of sending IPs, then exit
#     bash deploy/install.sh --restart               # restart the running stack, then exit
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
WIRE_RELAY_SASL=0
WIRE_RELAY_SPAM=0
WIRE_IP_ROTATION=0
RESTART_ONLY=0
for arg in "$@"; do
	case "$arg" in
		--app-only) APP_ONLY=1 ;;
		--wire-relay-sasl) WIRE_RELAY_SASL=1 ;;
		--wire-relay-spam) WIRE_RELAY_SPAM=1 ;;
		--wire-ip-rotation) WIRE_IP_ROTATION=1 ;;
		--restart) RESTART_ONLY=1 ;;
		-h|--help) sed -n '2,19p' "$0"; exit 0 ;;
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

# --restart just bounces containers, so it needs neither root nor Debian (only Docker).
if [ "$APP_ONLY" -eq 0 ] && [ "$RESTART_ONLY" -eq 0 ]; then
	[ "$(id -u)" -eq 0 ] || die "full install needs root — run: sudo bash deploy/install.sh (or use --app-only)"
	is_debian || die "automatic provisioning supports Debian/Ubuntu. On another OS, install Docker (+ Postfix) yourself and re-run with --app-only."
fi
have openssl || die "openssl not found"

# ---- collect settings ------------------------------------------------------
# Skipped for the focused maintenance runs (--wire-relay-*/--restart): they don't deploy,
# so they neither prompt for settings nor write deploy/.env.
REUSE_ENV=0
if [ "$WIRE_RELAY_SASL" -eq 0 ] && [ "$WIRE_RELAY_SPAM" -eq 0 ] && [ "$WIRE_IP_ROTATION" -eq 0 ] && [ "$RESTART_ONLY" -eq 0 ]; then
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
MAILLOG_DIR=$(dirname "$MAILLOG_PATH")
MXS_TELEMETRY_HASHKEY=$MXS_TELEMETRY_HASHKEY
# Attribute relay mail from un-registered sending domains to this tenant (so a shared
# hosting relay captures all telemetry without registering every client domain).
MXS_FALLBACK_TENANT_SLUG=$TENANT_SLUG
MXS_LOGLEVEL=info
EOF
	chmod 600 "$ENV_FILE"
	info "wrote $ENV_FILE"
fi
fi  # end --wire-relay-sasl guard around interactive settings

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

provision_dovecot() {
	# Dovecot acts purely as Postfix's SASL provider (no IMAP/POP). It authenticates SMTP
	# submission clients against the smtp_users table in Postgres, so credentials created
	# in the dashboard (SMTP Users) or via `mxctl smtp-user create` work immediately.
	bold "Installing Dovecot (SASL passdb for SMTP submission users)…"
	apt_install dovecot-core dovecot-pgsql

	cp -n /etc/dovecot/dovecot.conf "/etc/dovecot/dovecot.conf.bak.$(date +%s)" 2>/dev/null || true
	cat > /etc/dovecot/dovecot.conf <<'EOF'
# Managed by mxsentinel deploy/install.sh — SMTP submission SASL only (no IMAP/POP).
# Postfix uses this as its SASL backend; passwords live in Postgres (smtp_users).
protocols =
auth_mechanisms = plain login
disable_plaintext_auth = no
log_path = /var/log/dovecot.log

passdb {
  driver = sql
  args = /etc/dovecot/dovecot-sql.conf.ext
}
userdb {
  driver = static
  args = uid=nobody gid=nogroup home=/nonexistent allow_all_users=yes
}

# Auth socket Postfix reads (smtpd_sasl_type=dovecot, smtpd_sasl_path=private/auth).
service auth {
  unix_listener /var/spool/postfix/private/auth {
    mode = 0660
    user = postfix
    group = postfix
  }
}
EOF

	# Postgres-backed password lookup. The relay connects to the Postgres container
	# published on 127.0.0.1:5432 (see deploy/docker-compose.yml). default_pass_scheme
	# BLF-CRYPT verifies the bcrypt hashes the API/mxctl write.
	cat > /etc/dovecot/dovecot-sql.conf.ext <<EOF
driver = pgsql
connect = host=127.0.0.1 port=5432 dbname=${PG_DB:-mxsentinel} user=${PG_USER:-mxsentinel} password=${PG_PASSWORD:-}
default_pass_scheme = BLF-CRYPT
password_query = SELECT username AS "user", password_hash AS password FROM smtp_users WHERE username = '%u' AND enabled = TRUE
EOF
	chmod 600 /etc/dovecot/dovecot-sql.conf.ext
	systemctl enable dovecot >/dev/null 2>&1 || true
	systemctl restart dovecot || warn "dovecot restart failed — check 'journalctl -u dovecot'"

	# Postfix submission service (587): offer AUTH (via Dovecot) only over TLS, and relay
	# only for authenticated senders. Port 25 stays trusted-network only (no SASL).
	postconf -e \
		"smtpd_sasl_type = dovecot" \
		"smtpd_sasl_path = private/auth" \
		"smtpd_tls_auth_only = yes"
	postconf -M "submission/inet=submission inet n - y - - smtpd"
	postconf -P \
		"submission/inet/syslog_name=postfix/submission" \
		"submission/inet/smtpd_tls_security_level=may" \
		"submission/inet/smtpd_sasl_auth_enable=yes" \
		"submission/inet/smtpd_client_restrictions=permit_sasl_authenticated,reject" \
		"submission/inet/smtpd_relay_restrictions=permit_sasl_authenticated,reject" \
		"submission/inet/smtpd_recipient_restrictions=permit_sasl_authenticated,reject"
	systemctl restart postfix
	info "Dovecot SASL configured; Postfix submission (587) authenticates SMTP users from Postgres"
	info "Submission uses the default (snakeoil) TLS cert — install a real cert for production (see docs/smarthost.md)"
}

provision_spam() {
	# Outbound abuse controls: rspamd (spam scoring + per-authenticated-user rate caps) and
	# ClamAV (malware), chained as Postfix milters after OpenDKIM. This relay only carries
	# outbound/authenticated mail, so every message is scanned on the way out — one
	# compromised SMTP user can't quietly blast the shared IP pool into a blocklist.
	bold "Installing rspamd + ClamAV (outbound spam/malware filtering)…"
	apt_install rspamd clamav clamav-daemon clamav-milter

	# --- rspamd: scan outbound mail via the proxy worker in self-scan (milter) mode ---
	mkdir -p /etc/rspamd/local.d
	cat > /etc/rspamd/local.d/worker-proxy.inc <<'EOF'
milter = yes;
timeout = 120s;
upstream "local" {
  default = yes;
  self_scan = yes;
}
EOF
	# Reuse the app stack's loopback Redis for ratelimit/stats; isolate on db 1.
	cat > /etc/rspamd/local.d/redis.conf <<'EOF'
servers = "127.0.0.1:6379";
db = "1";
EOF
	# Reject clear spam outright (all traffic here is outbound/authenticated).
	cat > /etc/rspamd/local.d/actions.conf <<'EOF'
reject = 15;
add_header = 8;
greylist = null;
EOF
	# Per-SENDING-DOMAIN send caps — the core "one client can't blast" control. Keyed on the
	# envelope-from domain (not the SASL login) so a shared hosting relay, which authenticates
	# every client through one credential, still rate-limits each client independently. A
	# compromised customer is throttled without affecting the others. Tune to your traffic;
	# if the selector ever fails to evaluate, the limit simply doesn't apply (mail still flows).
	# Canonical config lives in the repo (deploy/rspamd/ratelimit.conf): per-authenticated-user
	# buckets AND per-sending-domain buckets, so one runaway client domain is throttled without
	# taking down the other 499 sharing the credential. Copy it if present; otherwise fall back
	# to a minimal per-domain inline config so a partial checkout still gets a working limit.
	if [ -f "$SCRIPT_DIR/rspamd/ratelimit.conf" ]; then
		install -m 0644 "$SCRIPT_DIR/rspamd/ratelimit.conf" /etc/rspamd/local.d/ratelimit.conf
	else
		cat > /etc/rspamd/local.d/ratelimit.conf <<'EOF'
rates {
  sender_domain_hourly {
    selector = "from('smtp').domain";
    bucket = {
      burst = 300;
      rate = "300 / 1h";
    }
  }
  sender_domain_daily {
    selector = "from('smtp').domain";
    bucket = {
      burst = 3000;
      rate = "3000 / 1d";
    }
  }
}
EOF
	fi
	cat > /etc/rspamd/local.d/options.inc <<'EOF'
local_addrs = "127.0.0.0/8, ::1";
EOF
	systemctl enable rspamd >/dev/null 2>&1 || true
	systemctl restart rspamd || warn "rspamd restart failed — check 'journalctl -u rspamd'"

	# --- ClamAV milter: reject mail carrying malware ---
	cp -n /etc/clamav/clamav-milter.conf "/etc/clamav/clamav-milter.conf.bak.$(date +%s)" 2>/dev/null || true
	cat > /etc/clamav/clamav-milter.conf <<'EOF'
MilterSocket inet:7357@localhost
FixStaleSocket true
User clamav
ClamdSocket unix:/var/run/clamav/clamd.ctl
OnInfected Reject
OnFail Defer
AddHeader Replace
LogSyslog true
EOF
	# Ensure clamd's runtime dir exists (some setups don't create it until first boot).
	install -d -o clamav -g clamav /run/clamav 2>/dev/null || mkdir -p /run/clamav
	# Pull signatures before starting clamd (it won't start with an empty DB). freshclam
	# can't run while its service holds the lock, so stop it for the one-shot update.
	systemctl stop clamav-freshclam >/dev/null 2>&1 || true
	bold "Downloading ClamAV signatures (first run can take a few minutes)…"
	freshclam --quiet || warn "freshclam update failed — clamav-daemon will retry on its timer"
	systemctl enable clamav-freshclam clamav-daemon clamav-milter >/dev/null 2>&1 || true
	systemctl start clamav-freshclam >/dev/null 2>&1 || true
	systemctl restart clamav-daemon >/dev/null 2>&1 || true
	# clamd loads the whole signature DB before it opens its socket (30-60s on first run).
	# Start clamav-milter only AFTER the socket exists, or Postfix logs "connect to Milter
	# service inet:localhost:7357: Connection refused" and (with milter_default_action=accept)
	# silently ships unscanned mail.
	bold "Waiting for clamd to load signatures and open its socket…"
	for _ in $(seq 1 60); do
		{ [ -S /run/clamav/clamd.ctl ] || [ -S /var/run/clamav/clamd.ctl ]; } && break
		sleep 2
	done
	if [ -S /run/clamav/clamd.ctl ] || [ -S /var/run/clamav/clamd.ctl ]; then
		info "clamd is up"
	else
		warn "clamd socket not up yet — it's still loading; clamav-milter will connect once it is."
		warn "If mail logs show 'Milter ... Connection refused', re-run: sudo bash deploy/install.sh --wire-relay-spam"
	fi
	systemctl restart clamav-milter >/dev/null 2>&1 || true

	# --- Postfix: chain the milters (OpenDKIM -> rspamd -> ClamAV) + per-client caps ---
	# milter_default_action=accept (set in provision_postfix) keeps mail flowing if a
	# milter is down — availability over a hard block on the relay.
	#
	# These caps are per *client IP* (the smarthost's address), so they're a coarse runaway
	# backstop, deliberately generous — a busy cPanel box shouldn't be throttled. Real
	# per-account limits are rspamd's job (keyed by SASL user). They cause 4xx deferrals
	# (retried), not bounces. Raise them if a legitimate smarthost gets deferred.
	postconf -e \
		"smtpd_milters = inet:localhost:8891, inet:localhost:11332, inet:localhost:7357" \
		"non_smtpd_milters = inet:localhost:8891, inet:localhost:11332, inet:localhost:7357" \
		"smtpd_client_message_rate_limit = 600" \
		"smtpd_client_recipient_rate_limit = 2000" \
		"smtpd_client_connection_rate_limit = 100" \
		"smtpd_recipient_limit = 200" \
		"anvil_rate_time_unit = 60s"
	systemctl restart postfix
	info "Outbound filtering on: rspamd (spam + per-user rate caps) + ClamAV (malware), chained after OpenDKIM"
	info "Outbound rate caps: per-user 200/h·2000/d + per-sending-domain 150/h·1000/d — tune deploy/rspamd/ratelimit.conf"
}

provision_ip_rotation() {
	# $1 = comma-separated sending IPs. Rotate outbound randomly across them: define one
	# smtp transport per IP (each bound to that source address) and select among them with
	# Postfix's randmap, which returns a random entry per lookup — so every message egresses
	# from a random pool IP. Each IP must already have matching PTR/rDNS (FCrDNS) or you'll
	# trade an IP-concentration problem for an auth problem.
	local ips="$1" i=0 ip name rand_entries=""
	IFS=',' read -ra _arr <<< "$ips"
	for ip in "${_arr[@]}"; do
		ip="$(printf '%s' "$ip" | tr -d '[:space:]')"
		[ -z "$ip" ] && continue
		i=$((i + 1))
		name="smtp-ip$i"
		postconf -M "$name/unix=$name unix - - n - - smtp"
		postconf -P "$name/unix/smtp_bind_address=$ip" "$name/unix/syslog_name=postfix/$name"
		rand_entries="$rand_entries${rand_entries:+, }$name:"
	done
	[ "$i" -ge 1 ] || die "no valid IPs given to --wire-ip-rotation"
	# randmap picks a random transport per recipient lookup → random source IP per message.
	postconf -e "transport_maps = randmap:{$rand_entries}"
	systemctl reload postfix || systemctl restart postfix
	info "Outbound IP rotation on: $i IP(s), random per message (transport_maps = randmap)."
	info "Verify: send a few test messages, then 'grep postfix/smtp-ip /var/log/mail.log' — the IP varies."
	info "Each IP needs PTR/rDNS set (FCrDNS). Rollback: postconf -X transport_maps && systemctl reload postfix"
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

# Focused maintenance path: (re-)wire relay SASL on an already-provisioned relay box, then
# exit. Idempotent — safe to run repeatedly. Picks up the Postgres credentials Dovecot
# needs from deploy/.env. Use this to bring an older relay (provisioned before SMTP-user
# support existed) up to date without a full redeploy.
if [ "$WIRE_RELAY_SASL" -eq 1 ]; then
	[ "$(id -u)" -eq 0 ] || die "--wire-relay-sasl needs root — run: sudo bash deploy/install.sh --wire-relay-sasl"
	is_debian || die "--wire-relay-sasl supports Debian/Ubuntu (it installs Dovecot via apt)"
	have postconf || die "Postfix not found on this host — provision the relay first (full install, without --wire-relay-sasl)"
	[ -f "$ENV_FILE" ] || die "$ENV_FILE not found — it holds the Postgres credentials Dovecot connects with"
	set -a
	# shellcheck source=/dev/null
	. "$ENV_FILE"
	set +a
	provision_dovecot
	bold "Done ✓ — relay SASL wired"
	info "Submission endpoint: ${MXS_DOMAIN:-this host}:587 (STARTTLS, AUTH PLAIN/LOGIN)"
	info "Manage users in the dashboard (SMTP Users) or: mxctl smtp-user create … (see docs/smarthost.md)"
	exit 0
fi

# Focused maintenance path: (re-)install rspamd + ClamAV outbound filtering on an existing
# relay box, then exit. Idempotent — safe to re-run (e.g. to retune after editing configs).
if [ "$WIRE_RELAY_SPAM" -eq 1 ]; then
	[ "$(id -u)" -eq 0 ] || die "--wire-relay-spam needs root — run: sudo bash deploy/install.sh --wire-relay-spam"
	is_debian || die "--wire-relay-spam supports Debian/Ubuntu (it installs rspamd/clamav via apt)"
	have postconf || die "Postfix not found on this host — provision the relay first (full install, without --wire-relay-spam)"
	provision_spam
	bold "Done ✓ — outbound spam/malware filtering wired"
	info "Test it: GTUBE string (spam) and the EICAR test file (malware) should be rejected — see docs/deploy-relay.md §9.8"
	exit 0
fi

# Focused maintenance path: rotate outbound mail randomly across a set of sending IPs, then
# exit. Idempotent (re-run to change the set). Prompts for the IPs — each must already have
# PTR/rDNS configured. See docs/deploy-relay.md §4.6.
if [ "$WIRE_IP_ROTATION" -eq 1 ]; then
	[ "$(id -u)" -eq 0 ] || die "--wire-ip-rotation needs root — run: sudo bash deploy/install.sh --wire-ip-rotation"
	have postconf || die "Postfix not found on this host — provision the relay first (full install, without --wire-ip-rotation)"
	ask ROTATE_IPS "Sending IPs to rotate across (comma-separated; each must have PTR/rDNS set)"
	[ -n "$ROTATE_IPS" ] || die "no IPs provided"
	provision_ip_rotation "$ROTATE_IPS"
	bold "Done ✓ — outbound IP rotation wired"
	exit 0
fi

# Focused maintenance path: restart the running stack (no provisioning, no rebuild), then
# exit. Restarts the app profile, plus relay (telemetryd) when this box runs the relay.
if [ "$RESTART_ONLY" -eq 1 ]; then
	have docker || die "Docker not found — nothing to restart"
	[ -f "$ENV_FILE" ] || die "$ENV_FILE not found — run a full install before --restart"
	set -a
	# shellcheck source=/dev/null
	. "$ENV_FILE"
	set +a
	rprof=(--profile app)
	[ -n "${RELAY_NODE_IP:-}" ] && rprof+=(--profile relay)
	bold "Restarting MX Sentinel…"
	docker compose -f "$BASE" -f "$PROD" --env-file "$ENV_FILE" "${rprof[@]}" restart
	bold "Done ✓ — stack restarted"
	info "Logs: docker compose -f $BASE -f $PROD --env-file $ENV_FILE ${rprof[*]} logs -f"
	exit 0
fi

# Provision the OS only on a fresh config. Reusing an existing .env means "just
# redeploy" — the box is already provisioned (and we don't have the original answers).
if [ "$APP_ONLY" -eq 0 ] && [ "$REUSE_ENV" -eq 0 ]; then
	provision_base
	provision_docker
	{ [ "${AI_LOCAL:-0}" -eq 1 ] && [ -n "${AI_MODEL:-}" ]; } && provision_ollama
	[ "${RELAY:-0}" -eq 1 ] && provision_postfix
	[ "${RELAY:-0}" -eq 1 ] && provision_dovecot
	[ "${RELAY:-0}" -eq 1 ] && provision_spam
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
		if yesno "Create an SMTP submission user now (so a smarthost can authenticate)?" n; then
			ask SMTP_USER_NAME "SMTP username" "mailer@$MAIL_DOMAIN"
			ask_secret SMTP_USER_PW "SMTP password (blank = auto-generate)"
			SMTP_PW_GENERATED=0
			if [ -z "$SMTP_USER_PW" ]; then SMTP_USER_PW="$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | cut -c1-20)"; SMTP_PW_GENERATED=1; fi
			if [ -n "$SMTP_USER_NAME" ] && mxctl smtp-user create --tenant "$TENANT_SLUG" --username "$SMTP_USER_NAME" --password "$SMTP_USER_PW" --domain "$MAIL_DOMAIN" >/dev/null 2>&1; then
				info "smtp user '$SMTP_USER_NAME' created"
				[ "$SMTP_PW_GENERATED" -eq 1 ] && info "SMTP password (SAVE THIS NOW): $SMTP_USER_PW"
			else
				warn "smtp-user create failed (username may already exist) — add one later in the dashboard (SMTP Users)"
			fi
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
# Put a host-side `mxctl` on PATH (wraps docker compose: restart/logs/ps act on the stack,
# everything else runs in the apid container).
if ln -sf "$REPO_ROOT/deploy/mxctl" /usr/local/bin/mxctl 2>/dev/null; then
	MXCTL_HINT="mxctl"
else
	MXCTL_HINT="$REPO_ROOT/deploy/mxctl"
fi

bold "Done ✓"
info "Dashboard:  https://${MXS_DOMAIN:-$DOMAIN}/login"
[ "${OWNER_PW_GENERATED:-0}" -eq 1 ] && info "Owner password (SAVE THIS NOW): $OWNER_PASSWORD"
info "Config/secrets: $ENV_FILE (private; never commit)"
info "CLI: $MXCTL_HINT restart | logs | ps | user set-password … | smtp-user create …"
info "Logs: $MXCTL_HINT logs -f apid aid"
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
	info "Smarthost submission endpoint: $DOMAIN:587 (STARTTLS, AUTH PLAIN/LOGIN)."
	info "Manage SMTP submission users in the dashboard (SMTP Users) or: mxctl smtp-user create …"
	info "Smarthost client setup (cPanel/Exim/Postfix/apps): see docs/smarthost.md."
	info "Outbound spam/malware filtering (rspamd + ClamAV) is on; per-sending-domain caps in /etc/rspamd/local.d/ratelimit.conf."
	info "Multi-IP sender pools and warmup: see docs/deploy-relay.md."
fi

# Relay on this host but SASL not wired (e.g. it was provisioned before SMTP-user support).
# Surface the one-shot, idempotent fix rather than silently leaving logins broken.
if [ "${RELAY:-0}" -eq 1 ] && [ ! -f /etc/dovecot/dovecot-sql.conf.ext ]; then
	warn "Relay SASL is not wired on this host yet — SMTP submission users cannot authenticate."
	warn "Wire it (idempotent, no redeploy): sudo bash deploy/install.sh --wire-relay-sasl"
fi

# Relay on this host but outbound spam filtering not wired — surface the one-shot fix.
if [ "${RELAY:-0}" -eq 1 ] && ! have rspamd; then
	warn "Outbound spam/malware filtering is not installed on this host yet."
	warn "Install it (idempotent, no redeploy): sudo bash deploy/install.sh --wire-relay-spam"
fi
