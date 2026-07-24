# Outbound failover to a fallback smarthost

When the relay's **direct** delivery to a receiver provider (e.g. Microsoft/Outlook) is
being **transiently throttled** — repeated `4xx` deferrals rather than accepted mail —
MX Sentinel can automatically reroute that provider's mail through a **fallback smarthost**
(e.g. mail.baby) until the throttling clears, then revert to direct delivery.

This is the `relayfailoverd` daemon plus a host-side Postfix hook.

---

## 1. What this does — and, importantly, what it does NOT do

Outlook (and other providers) refuse mail in two very different ways, and only one of them
can be helped by a fallback relay:

| Provider response | Meaning | Failover helps? |
|---|---|---|
| **`4xx` transient defer** (e.g. `421 4.7.0`, throttling / greylist / temporary block) | "Try again later." The message is **retryable** and still in your queue. | **Yes** — routing it through another smarthost's IPs can get it delivered now. |
| **`5xx` spam / reputation block** (e.g. `550 5.7.1 S3150`, "high probability of spam", DNSBL) | "Rejected on the merits." | **No.** Rerouting the *same mail from the same senders* through mail.baby just launders your reputation problem onto **their** IPs — which gets *their* IPs blocked and violates most relay providers' terms. |
| **Async bounce / silent junking** (accepted `250`, NDR later or foldered) | The message already left your relay. | **No** — it's gone; there is nothing to reroute. |

`relayfailoverd` therefore triggers **only on sustained transient `4xx` defers**. It reads
the `bounce_class` on each deferred event (`internal/bounce` classifies these) and counts
spam/reputation blocks **separately, for context only** — they never trip failover.

> **If Outlook is `5xx`-blocking you for reputation, failover is the wrong tool.** Fix it at
> the source: SPF/DKIM/DMARC alignment, PTR/FCrDNS, IP warmup, and complaint rate. The
> `fbld`, `sndsd`, `scored`, and `rbld` daemons exist to diagnose exactly that.

---

## 2. How it works (circuit breaker)

`relayfailoverd` runs a per-provider **circuit breaker**:

```
CLOSED (direct delivery)                OPEN (failover to smarthost)
  │  watch relay-wide 4xx defer rate       │  provider's mail -> fallback transport
  │  over a sliding window                  │  (no direct telemetry while open)
  │                                         │
  └── trip when: attempts ≥ MIN_ATTEMPTS ──►│
       AND defers ≥ MIN_DEFERS              │
       AND defer_rate ≥ TRIP_RATE           │
                                            │
   ◄──── auto-revert after HOLD ────────────┘   (half-open: re-probe direct;
         (time-based, NOT rate-based)             re-trips if still throttling)
```

**Why recovery is time-based, not rate-based:** once mail is failed over, we no longer send
directly to the provider, so we get **no direct-defer telemetry** to measure recovery from.
The breaker instead holds `OPEN` for a fixed window (`MXS_FAILOVER_HOLD`, default 30m), then
reverts to direct to re-probe with real traffic. If the provider is still throttling, the
next windows trip it again.

### The mail-path mechanism (never depends on MX Sentinel being up)

Mirroring the `rbld` healthy-IPs design, the daemon **never touches host Postfix directly**:

1. `relayfailoverd` (container) writes the set of recipient domains currently in failover to
   a bind-mounted **state file** (`/var/lib/mxsentinel/failover-domains` →
   `deploy/failover-state/failover-domains` on the host).
2. A **host-side hook** (`deploy/hooks/relay-failover-hook.sh`, on cron every ~2 min) reads
   that file, rebuilds a Postfix **transport overlay map** (`/etc/postfix/mxs_failover`)
   routing each failover domain to the fallback transport, reloads Postfix, and **requeues
   deferred mail** (`postsuper -r`) so it takes the new route immediately.

If `relayfailoverd` is down, the overlay simply stops changing — mail keeps flowing. The
overlay is the **first** entry in `transport_maps` (before the IP-rotation `randmap`
catch-all), so a failover domain wins by first-match; everything else routes normally.

---

## 3. Setup

### 3a. Configure in the dashboard (recommended)

**Settings → Outbound fallback smarthost.** Set the host/port/username/password, choose the
recipient domains, and pick a mode:

- **Always route** — pin those domains to the smarthost unconditionally. Use this for a
  persistent block (e.g. Outlook has your sending IP on S3140/S3150). This is the common case.
- **Only on throttling** — the 4xx circuit breaker below (auto-reverts when defers clear).

The password is encrypted at rest (write-only in the API). `relayfailoverd` reads this config
and renders the Postfix credentials + transport onto the relay via the host hook, so you never
hand-edit `/etc/postfix`. One host-side bootstrap is still required once (defines the
`relay-mailbaby` transport + the `transport_maps` overlay): run **§3b** a single time; after
that, everything is dashboard-managed. See `docs/settings-inventory.md`.

### 3b. Wire the host side (one-time, on the relay box)

```bash
sudo bash deploy/install.sh --wire-relay-failover
```

It prompts for the fallback smarthost `host[:port]` and SASL username/password. **Leave them
blank** to install a placeholder (safe — failover stays inert until creds are filled in).
This step:

- defines the `relay-mailbaby` Postfix transport (distinct syslog tag `postfix/relay-mailbaby`
  so `telemetryd` can tell failover traffic apart from direct sends);
- writes the fallback SASL creds to `/etc/postfix/mxs_failover_sasl` (`smtp_sasl_*` enabled);
- writes the nexthop (`relay-mailbaby:[host]:port`, brackets = no MX lookup) to
  `/etc/postfix/mxs_failover.env` for the hook;
- creates the (empty) overlay map and puts it first in `transport_maps`;
- installs the host hook on cron.

To add credentials later:

```bash
sudo sh -c 'echo "[smtp.mailbaby.net]:587 USER:PASS" > /etc/postfix/mxs_failover_sasl \
  && postmap /etc/postfix/mxs_failover_sasl && systemctl reload postfix'
```

### 3b. Arm the daemon

In `deploy/.env`:

```bash
MXS_FAILOVER_ENABLED=true
RELAY_TENANT_ID=<uuid>     # optional: attach an incident when the breaker trips
```

Then restart: `docker compose ... up -d relayfailoverd`. Until `MXS_FAILOVER_ENABLED=true`
the daemon idles (writes an empty state file and does nothing).

---

## 4. Configuration (env)

| Var | Default | Meaning |
|---|---|---|
| `MXS_FAILOVER_ENABLED` | `false` | Master switch. Off = idle. |
| `MXS_FAILOVER_PROVIDER` | `microsoft` | Provider label to watch (matches `internal/telemetry.Provider`). |
| `MXS_FAILOVER_DOMAINS` | Outlook/Hotmail/Live/MSN set | Recipient domains written to the overlay when OPEN. |
| `MXS_FAILOVER_WINDOW` | `10m` | Sliding window for the defer-rate measurement. |
| `MXS_FAILOVER_INTERVAL` | `1m` | Evaluation tick. |
| `MXS_FAILOVER_HOLD` | `30m` | How long to stay in failover before re-probing direct. |
| `MXS_FAILOVER_TRIP_RATE` | `0.60` | Fraction of attempts that are 4xx defers to trip. |
| `MXS_FAILOVER_MIN_ATTEMPTS` | `50` | Minimum attempts in the window before the rate is trusted. |
| `MXS_FAILOVER_MIN_DEFERS` | `20` | Absolute 4xx-defer floor to trip (with the rate). |
| `MXS_FAILOVER_MAX_DOMAINS` | `25` | Hard cap on domains in failover (safety valve). |
| `MXS_FAILOVER_STATE_FILE` | `/var/lib/mxsentinel/failover-domains` | State file the host hook reads. |
| `RELAY_TENANT_ID` | — | Tenant to open incidents against on trip (empty = log only). |

Hook-side env (host, in `/etc/postfix/mxs_failover.env` or the cron line):
`FALLBACK_TRANSPORT`, `MAP_FILE`, `FAILOVER_DOMAINS_FILE`, `REQUEUE` (`targeted`|`all`|`none`).

---

## 5. Observability & rollback

- **Trip/revert** are logged (`slog`) and, when `RELAY_TENANT_ID` is set, a `high`-severity
  incident is opened on each trip with the defer counts and remediation guidance.
- **Verify a failover in the maillog:** `grep 'postfix/relay-mailbaby' /var/log/mail.log` —
  failover sends carry that tag; direct sends carry `postfix/smtp` / `postfix/smtp-ipN`.
- **Manual rollback (host):**
  ```bash
  : > /etc/postfix/mxs_failover && postmap /etc/postfix/mxs_failover && systemctl reload postfix
  # and disarm: set MXS_FAILOVER_ENABLED=false, restart relayfailoverd
  ```

---

## 6. Interaction with IP rotation (`rbld`)

Both features share Postfix's single `transport_maps` setting. The failover overlay is kept
as the **first** map (`hash:/etc/postfix/mxs_failover, randmap:{…}`): specific failover
domains match first; everything else falls through to the IP-rotation `randmap`. The
installer's `provision_ip_rotation` and the failover hook both preserve this ordering (the
hook self-heals it every run), so the two hooks don't clobber each other.
