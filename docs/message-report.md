# Per-message report (drill-down)

A MagicSpam/mail.baby-style per-email view for the operator: for any single message that
transited the relay, show its envelope, authentication results, the full delivery timeline,
and — when rspamd capture is enabled — the spam verdict/symbols and the complete headers.

It is the per-message counterpart to the message explorer (`/v1/messages`) and reuses the same
stable handle: the relay **queue id**.

## What it shows

| Tab | Source | Scope |
|---|---|---|
| **Envelope** | `smtp_events` (collapsed) — originating IP, SASL user, MAIL FROM, provider, SPF/DKIM/DMARC, TLS, size, final SMTP response | ScopeRead |
| **Spam** | `message_content` — rspamd score, action, per-symbol weights ("Spam Tests") | ScopeRead |
| **Headers** | `message_content` — subject + full verbatim header block | **ScopeAdmin** |
| **Timeline** | `smtp_events` — every recorded event, oldest first ("Log entries") | ScopeRead |

## API

- `GET /v1/messages/{queueID}` — returns `{ envelope, timeline, content_captured, spam?, content?, content_restricted? }`.
  - `spam` (score/action/symbols) is classification **metadata** and is returned to any ScopeRead caller.
  - `content` (subject + `raw_headers` + `parsed_headers`) is message **content** and is returned
    **only** to callers holding the admin scope. A ScopeRead caller instead gets
    `content_restricted: true` so the UI can explain why the Headers tab is empty.
- `POST /v1/messages/{queueID}/reclassify` `{ "spam": false }` (ScopeWrite) — flips the operator's
  spam judgement **for the report**. It does *not* retrain rspamd (MX Sentinel stores no bodies).
- `POST /v1/ingest/message-content` (ScopeWrite) — the capture ingest, called by the relay's rspamd
  plugin. Tenant is taken from the token, never the body.

## Capture: the rspamd exporter

The Spam and Headers tabs are populated by `deploy/rspamd/mxs_trace.lua`, an **idempotent** rspamd
symbol that fire-and-forgets each message's `{queue_id, message_id, subject, raw_headers, score,
action, symbols[]}` to `POST /v1/ingest/message-content`.

Install on the relay host:

1. `cp deploy/rspamd/mxs_trace.lua /etc/rspamd/lua/mxs_trace.lua`
2. Edit the file: set `MXS_ENDPOINT` and `MXS_TOKEN` (a **write**-scoped token for the relay tenant:
   `mxctl apikey create --tenant <slug> --scopes write --name rspamd-trace`).
3. `echo "dofile('/etc/rspamd/lua/mxs_trace.lua')" >> /etc/rspamd/rspamd.local.lua`
4. `systemctl reload rspamd` — confirm with `journalctl -u rspamd | grep mxs_trace`.

The call is asynchronous with a short timeout and every failure is swallowed: **mail flow and the
spam verdict never depend on MX Sentinel being reachable.** If the endpoint is down, the message is
simply not captured; the Envelope and Timeline tabs still work from `smtp_events`.

## Privacy carve-out

MX Sentinel's default invariant is *metadata only*: the telemetry parser drops bodies and hashes
recipients, and the AI layer never sees content. This feature deliberately introduces the **one
exception** — `message_content` stores the subject line and the full raw header block (which
includes `To:` in clear) so the operator can triage their own outbound like a hosted spam filter.

Guardrails that keep the exception contained:

- **Separate table.** Content lives only in `message_content`, never in `smtp_events`. `cmd/aid`
  and every AI path read metadata/incidents only and never touch this table — the boundary is
  structural, not just a convention.
- **30-day TTL.** Content auto-expires (`TTL received_at + 30 DAY`); metadata keeps its own
  90-day retention. Old messages keep Envelope/Timeline but lose Spam/Headers.
- **Admin-gated.** Subject and headers are returned only to admin-scoped tokens; a plain read
  token cannot pull message content.
