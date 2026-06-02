# Event Contracts

All MX Sentinel services communicate asynchronously through an event bus
(**NATS JetStream** initially; Kafka at large scale). This document defines the wire
contract: the common envelope, the subject hierarchy, delivery semantics, and the event
families. The machine-readable JSON Schemas live in
[`../schemas/events/`](../schemas/events/) and are the source of truth — Go types in
`pkg/contracts` are generated from them.

---

## The envelope

Every message published to the bus is a JSON object that wraps a type-specific `payload`
in a common envelope. This lets every consumer route, deduplicate, and correlate without
parsing the payload.

Schema: [`../schemas/events/envelope.schema.json`](../schemas/events/envelope.schema.json)

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string (semver) | ✓ | Version of the *payload* schema for `event_type`. |
| `event_id` | string (UUIDv7) | ✓ | Globally unique; UUIDv7 so it sorts by time. Consumers dedupe on this. |
| `event_type` | string (enum) | ✓ | Dotted type, e.g. `smtp.delivered`, `dns.changed`. Determines `payload` shape. |
| `tenant_id` | string (UUID) | ✓ | Owning tenant. Also encoded in the subject for routing. |
| `occurred_at` | string (RFC 3339) | ✓ | When the real-world event happened (relay timestamp, DNS poll time). |
| `ingested_at` | string (RFC 3339) | ✓ | When MX Sentinel observed/published it. `ingested_at − occurred_at` = pipeline lag. |
| `source` | string | ✓ | Emitting component + node, e.g. `telemetryd@relay-03`. |
| `correlation` | object | ✓ | Correlation keys lifted out of the payload for fast joining (see below). |
| `payload` | object | ✓ | Type-specific body, validated against the matching schema. |

### Correlation block

Carried in the envelope so the correlation engine never has to crack open payloads it
doesn't understand. All fields optional (presence depends on event type):

```json
"correlation": {
  "message_id": "<abc123@mail.example.com>",
  "queue_id": "4F1aB2c3",
  "session_id": "01J9...",
  "source_ip": "203.0.113.10",
  "relay_ip": "198.51.100.5",
  "dkim_selector": "s1",
  "envelope_from": "bounce@example.com",
  "domain": "example.com"
}
```

These are exactly the **primary and secondary correlation keys** from
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) §4.

---

## Subject hierarchy (NATS)

Subjects are hierarchical and tenant-scoped so consumers can subscribe at any granularity:

```
mxs.<family>.<tenant_id>.<event>
```

Examples:

| Subject | Event |
| --- | --- |
| `mxs.smtp.<tenant>.delivered` | message delivered |
| `mxs.smtp.<tenant>.deferred` | temporary failure (4xx) |
| `mxs.smtp.<tenant>.bounced` | permanent failure (5xx) post-acceptance |
| `mxs.smtp.<tenant>.rejected` | rejected at SMTP time |
| `mxs.smtp.<tenant>.received` | accepted into the relay (inbound to our queue) |
| `mxs.dns.<tenant>.changed` | a domain's DNS state changed (new snapshot) |
| `mxs.dns.<tenant>.validation_failed` | a validation finding crossed a severity threshold |
| `mxs.reputation.<tenant>.blacklist_hit` | an IP/domain appeared on an RBL |
| `mxs.reputation.<tenant>.complaint_spike` | FBL/complaint rate anomaly |
| `mxs.ai.<tenant>.anomaly` | AI-detected anomaly |
| `mxs.ai.<tenant>.remediation` | AI remediation recommendation |
| `mxs.ai.<tenant>.rca` | AI root-cause summary |

Wildcard subscriptions:

- `mxs.smtp.*.>` — all SMTP events, all tenants (the ClickHouse writer, `ingestd`).
- `mxs.*.<tenant>.>` — everything for one tenant (per-tenant correlation worker).
- `mxs.dns.<tenant>.>` — a tenant's DNS events (alerting).

### JetStream streams

| Stream | Subjects captured | Retention | Consumers |
| --- | --- | --- | --- |
| `SMTP` | `mxs.smtp.>` | by age/size (telemetry is bulk) | `ingestd` (→ClickHouse), correlation engine |
| `DNS` | `mxs.dns.>` | longer (low volume, high value) | alerting, correlation engine |
| `REPUTATION` | `mxs.reputation.>` | longer | alerting, correlation engine |
| `AI` | `mxs.ai.>` | longer | API/dashboard, notifications |

---

## Delivery semantics

- **At-least-once.** JetStream redelivers on un-acked messages. Consumers **must be
  idempotent** — dedupe on `event_id` (short-TTL Redis set or ClickHouse
  `ReplacingMergeTree` semantics).
- **Ordering** is not globally guaranteed. Consumers reconstruct timelines using
  `occurred_at`, never bus arrival order.
- **Poison messages** that fail schema validation are routed to a dead-letter subject
  `mxs.dlq.<family>` with the validation error attached — never silently dropped.
- **Backpressure:** producers (relay telemetry) buffer locally and never block mail flow;
  if the bus is unavailable, events spool to disk on the relay node and replay on recovery.
  *Mail delivery must never depend on MX Sentinel being up.*

---

## Event families

### `smtp.*` — SMTP telemetry

Schema: [`../schemas/events/smtp_event.schema.json`](../schemas/events/smtp_event.schema.json).
One event per SMTP transaction outcome. Payload carries the message/queue/session ids,
source & relay IPs, classified `provider`, auth results (spf/dkim/dmarc), TLS metadata,
SMTP status (`smtp_code`, `enhanced_status_code`, truncated `response_text`), bounce
classification, sizes, and timing. **No body, no subject** (see privacy boundary in
`data-model.md`). This is the highest-volume family and maps 1:1 to the ClickHouse
`smtp_events` table.

### `dns.*` — DNS intelligence

Schema: [`../schemas/events/dns_event.schema.json`](../schemas/events/dns_event.schema.json).
Emitted by `dnsd` when a snapshot changes or a finding crosses a threshold. Payload
carries the domain, the snapshot id, what changed (diff of record types), and findings
(category/severity/code). This is the event that lets us correlate "DNS changed at 10:44 →
rejections at 10:45."

### `reputation.*` — reputation signals

Schema: [`../schemas/events/reputation_event.schema.json`](../schemas/events/reputation_event.schema.json).
Blacklist (RBL) hits, complaint/FBL spikes, and rate anomalies for an IP, IP pool, or
sending domain. Payload identifies the subject (ip / domain / pool), the signal kind, the
source (e.g. which RBL), and a severity.

### `ai.*` — AI reasoning output

Schema: [`../schemas/events/ai_event.schema.json`](../schemas/events/ai_event.schema.json).
Produced by the AI reasoning layer (Phase 3). Three kinds: `anomaly` (classification),
`remediation` (recommended action), `rca` (root-cause summary). Payload carries a
human-readable narrative, structured recommendations, a confidence score, and the
`event_id`s of the evidence it reasoned over (so the UI can link back to the raw signals).

---

## Versioning

- The envelope schema version is independent of payload versions.
- Payload `schema_version` follows semver. **Additive** changes (new optional fields) bump
  the minor version and require no consumer changes. **Breaking** changes bump the major
  version and publish under a new `event_type` suffix if a transition window is needed.
- Consumers must ignore unknown fields (forward compatibility) and reject only on missing
  *required* fields.
