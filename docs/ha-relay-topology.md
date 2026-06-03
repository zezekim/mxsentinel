# HA Outbound Relay Topology

> **Phase 4 design document.** This document describes the target high-availability
> topology for MX Sentinel's relay tier. Per `ARCHITECTURE.md §10`, HA relay clusters are
> a Phase 4 concern — do not over-engineer orchestration early. This document is a
> planning reference, not an immediate build target.

---

## 1. Purpose — when HA is needed, and when it is not

### When a single relay node is acceptable

A single relay node is the right starting point for most deployments. It is easy to
operate, easy to observe, and easy to reason about. If the deployment serves a small
number of tenants with low combined volume and has no contractual SLA for mail delivery
latency, a single node with a well-tuned Postfix queue is sufficient. MX Sentinel
(`telemetryd`) will observe it without any topology changes.

### When HA becomes necessary

The relay tier is a **shared single point of failure** for every tenant's outbound mail.
A relay outage does not affect MX Sentinel's ability to observe historical data — but it
stops the live send path for all tenants simultaneously. HA is justified when any of the
following conditions apply:

| Condition | Why it forces HA |
|---|---|
| Multi-tenant production service with an SLA | A single node outage violates SLA for all tenants at once. |
| Combined volume exceeds one node's throughput | Per-receiver connection and rate limits are per-sending-IP; spreading across nodes/IPs directly multiplies throughput capacity. |
| Reputation isolation between tenant segments | Transactional, marketing, and warmup traffic must use separate IP pools; a single node cannot cleanly isolate these if it holds all IPs. |
| Maintenance windows cannot accept queuing delays | Rolling restarts require draining a node; without a second node, all queued mail defers during the window. |
| IP blocklist events are operationally significant | A blocklisted IP on a single-node setup disables all sending; with multiple nodes, the affected IP can be taken out of rotation without halting mail flow. |

The relay tier's role as a shared, revenue-affecting service makes it the component that
benefits most from HA investment. MX Sentinel itself (the observability plane) is
explicitly designed to **never be in the mail path** — `telemetryd` spools to disk and
replays if the bus is down, so an MX Sentinel outage does not stop mail flow regardless
of relay topology.

---

## 2. Why HA — five specific goals

### 2a. Eliminate the relay as a single point of failure

Every tenant sending through the platform relies on the relay tier. A crash, kernel
panic, NIC failure, or misconfigured firewall rule on a single relay node takes down
outbound mail for all tenants simultaneously. An HA cluster means that losing any one
node shifts its traffic to surviving nodes without operator intervention.

### 2b. Queue durability — no in-flight message loss

Each relay node maintains its own persistent on-disk queue (Postfix `spool`, PowerMTA
job files, Haraka queue directory). Messages accepted by a node are durable on that
node's disk immediately. A node failure does not lose messages: they remain spooled and
are delivered once the node recovers (or are manually migrated to another node during
drain). The goal is **no message loss on single-node failure**, which is achievable with
per-node queues without requiring a distributed shared queue.

### 2c. Throughput and per-receiver per-IP rate limits

Major receivers (Gmail, Microsoft, Yahoo) enforce connection-rate and message-rate limits
**per sending IP**. A single IP on a single node can saturate its allowed rate to a
receiver while thousands of messages queue. Spreading across multiple nodes with multiple
IPs multiplies effective throughput linearly — N nodes with M IPs each gives N×M parallel
delivery lanes.

### 2d. Reputation isolation via IP-pool segmentation

Transactional mail (receipts, password resets), marketing mail (newsletters, campaigns),
and warmup traffic carry different risk profiles. A spam outbreak on a marketing IP
should not affect the transactional IP used for login emails. The Postgres `ip_pools`
table models this segmentation explicitly (`purpose`: `transactional`, `marketing`,
`warmup`, `mixed`; `addresses inet[]`). In an HA cluster, different nodes or node groups
can own different pools, ensuring that a blocklisted pool does not affect delivery on
other pools.

### 2e. Zero-downtime rolling maintenance

Kernel updates, TLS certificate rotations, relay software upgrades, and IP reassignments
all require restarting relay processes. With a single node, every maintenance window
means mail queues and connection attempts fail over the restart period. With multiple
nodes, maintenance is rolling: drain one node, apply changes, restore it, then proceed to
the next — the remaining nodes absorb traffic throughout.

---

## 3. Cluster topology

### 3.1 Roles in the data path

Two distinct connections make up the relay's role:

- **Submission path** — smart hosts (cPanel/Exim) inject authenticated mail *to* the
  relay cluster on port 587 (SMTP submission) or 25 (relay-to-relay). This is the
  inbound side of the relay.
- **Delivery path** — each relay node connects *from* one of its assigned IPs *to* the
  receiving MTA (Gmail, Microsoft, Yahoo, etc.) on port 25. This is the outbound side.

These are separate concerns. The submission path is about accepting mail reliably; the
delivery path is about reputation, rate limits, and authentication alignment.

### 3.2 ASCII diagram

```
  cPanel / Exim Smart Hosts (tenants' origin servers)
  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
  │  smarthost-1  │  │  smarthost-2  │  │  smarthost-N  │
  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘
         │                 │                 │
         │  SMTP submission (port 587 / 25, authenticated)
         │                 │                 │
         ▼                 ▼                 ▼
  ┌─────────────────────────────────────────────────┐
  │          SUBMISSION DISTRIBUTION LAYER          │
  │  (DNS round-robin MX / HAProxy / multiple       │
  │   smarthost relayhost entries)                  │
  └──────────┬──────────────────────────┬───────────┘
             │                          │
    ┌────────▼───────┐        ┌─────────▼──────┐
    │  relay-node-1  │        │  relay-node-2  │   ... relay-node-N
    │                │        │                │
    │  IP pool: TXN  │        │  IP pool: MKT  │
    │  203.0.113.10  │        │  203.0.113.20  │
    │  203.0.113.11  │        │  203.0.113.21  │
    │                │        │                │
    │  Postfix spool │        │  Postfix spool │
    │  (on-disk,     │        │  (on-disk,     │
    │   persistent)  │        │   persistent)  │
    └────────┬───────┘        └────────┬───────┘
             │                         │
             │  Outbound SMTP delivery (port 25)
             │  from assigned IPs, per receiver limits
             │                         │
             ▼                         ▼
  ┌──────────────────────────────────────────────┐
  │              Receiving MTAs                  │
  │  Gmail    Microsoft    Yahoo    Regional      │
  └──────────────────────────────────────────────┘

  Observability plane (NOT in the mail path):
  ┌─────────────┐     ┌─────────────┐
  │ telemetryd  │     │ telemetryd  │   (one per relay node, tails maillog)
  │ node-1      │     │ node-2      │
  └──────┬──────┘     └──────┬──────┘
         │  NATS / disk spool │
         ▼                   ▼
  ┌──────────────────────────────┐
  │   MX Sentinel pipeline       │
  │   (correld, repd, aid, ...)  │
  └──────────────────────────────┘
```

### 3.3 Submission distribution methods

Smart hosts are typically configured with a `relayhost` (Postfix) or equivalent that
points to the relay cluster. Three practical distribution approaches:

| Method | How it works | Trade-offs |
|---|---|---|
| **Multiple relayhost entries** (Postfix transport map) | Each smart host lists N relay IPs; Postfix tries them in order and falls over on connection failure. | Simple, no extra infrastructure; failover is per-connection, not load-balanced. |
| **DNS round-robin** (multiple A records for a relay VIP hostname) | Smart hosts resolve the relay hostname; each resolution may return a different node IP. | Distributes load across nodes; no LB required; TTL must be short (30–60 s) for failover to be meaningful. |
| **TCP load balancer** (HAProxy / cloud LB) | A VIP fronts all relay nodes; SMTP connections are distributed by the LB. | Cleanest abstraction; supports health checks and weighted routing; adds one more component to the HA story. |

In practice, the combination of **DNS round-robin with short TTL** plus **multiple
relayhost entries as fallback** is a pragmatic starting point that requires no additional
infrastructure. A dedicated LB makes sense once volume and operational maturity justify
it.

---

## 4. Queue durability

### Per-node persistent queues

Each relay node maintains its own on-disk mail queue. Postfix writes each accepted
message to a durable spool directory before returning `250 OK` to the submitting smart
host. PowerMTA uses per-queue job files. Haraka uses a pluggable queue directory. In all
cases, the message is disk-committed before the SMTP handshake completes — this is the
relay software's core durability guarantee.

**Implication:** if a relay node crashes immediately after accepting a message, the
message is on that node's disk. It is not lost; it defers until the node recovers or an
operator migrates the spool.

### Retry and deferral behavior

Receivers routinely issue `4xx` temporary deferral responses (rate limiting,
greylisting, capacity). Relay nodes handle this natively: deferred messages stay in the
local queue and are retried on an exponential schedule (Postfix default: first retry at 5
minutes, doubling up to a 4-hour maximum, with a 5-day expiry). This behavior is
unchanged in an HA cluster — each node retries its own queue independently.

### Draining a node before maintenance

Before taking a node out of rotation:

1. Remove the node from the submission distribution layer (DNS, LB weight to zero, or
   relayhost entry removal) so no new mail is routed to it.
2. Allow the node's queue to flush (monitor via `mailq` / PowerMTA management port /
   Haraka admin API). Assist by temporarily relaxing retry intervals if needed.
3. Once the queue is empty (or acceptably small), stop the relay service and perform
   maintenance.
4. Restore the node and re-add it to the distribution layer.

Skipping the drain step means in-flight messages on that node will defer until it
returns. This is safe (no message loss) but increases delivery latency for those
messages.

### Shared vs per-node queue trade-offs

| Approach | Pros | Cons |
|---|---|---|
| **Per-node queue** (recommended) | Simple, standard, no extra infrastructure, proven by every relay MTA | Node failure leaves messages deferred on that node until recovery or manual migration |
| **Shared/replicated queue** (e.g., NFS spool, distributed FS) | Any node can dequeue any message after a peer fails | Operational complexity, NFS is a latency and reliability liability for mail, not supported natively by most relay MTAs |
| **Re-injection on failure** (smart host retries) | Submitting smart host detects failure and retries to another relay node | Requires smart hosts to implement retry logic; double-acceptance risk if the original node accepted but the smart host did not receive the `250 OK` |

The per-node queue approach is correct for this system. Message loss on single-node
failure is prevented by the relay MTA's own durability guarantee. Recovery is
straightforward: bring the node back up and it drains its own queue.

---

## 5. IP pools and reputation

### Pool-to-node mapping

The Postgres `ip_pools` table records each pool's `purpose` (`transactional`,
`marketing`, `warmup`, `mixed`) and its `addresses inet[]`. The `relay_nodes` table
links a node to its `ip_pool_id`, making the assignment explicit and queryable.

In a cluster, pools can be assigned in two ways:

| Assignment model | When to use |
|---|---|
| **One pool per node** | Cleanest isolation; a marketing spam event cannot affect the transactional node. Preferred for production. |
| **Multiple pools on one node** | Acceptable on small clusters where strict isolation is not required; node count is below the pool count. |

A single node should never mix transactional and marketing pools if reputation isolation
is a goal. Warmup IPs should always be on dedicated nodes or at minimum on a dedicated
interface with strict rate limits.

### Warmup

New IPs must be warmed up gradually — receivers track per-IP sending history and treat
unknown IPs with lower trust. A warmup pool node sends an increasing daily volume
according to a schedule (typically doubling weekly from a few hundred to tens of
thousands of messages per day). MX Sentinel's `repd` monitors warmup IPs against DNSBLs
and `correld` watches for abnormal rejection rates, surfacing signals that warmup is
going poorly before a reputation hole forms.

### Blocklisted IP isolation

When `repd` detects that a sending IP has been listed on a DNSBL:

1. An `reputation.blacklist_hit` event is published and an incident is opened.
2. The operator (or an automated response) removes the affected IP from the pool's
   `addresses` and updates the relay node's interface routing or Postfix
   `smtp_bind_address` configuration to stop using it for new connections.
3. Mail that was already queued and retrying from that IP will fail `4xx`/`5xx` at the
   receiver; the operator can force-requeue it to route via a different IP.
4. The remaining IPs in other pools continue sending without interruption.

This workflow is only possible because pool segmentation prevents a single IP event from
taking down all sending.

### SPF, DKIM, and PTR alignment per IP

Every sending IP in a pool requires:

- **PTR record** — forward-confirmed reverse DNS (FCrDNS). The PTR must resolve back to
  the IP. Receivers reject or penalize senders without valid PTR.
- **SPF** — the sending IP must be included in the SPF record of the envelope sender
  domain. With multiple nodes and IPs, SPF records must enumerate all sending IPs (or use
  `include:` mechanisms pointing to maintained IP lists). SPF's 10-lookup limit is a
  practical concern with large pools.
- **DKIM** — signing is per-domain, not per-IP, so DKIM is not directly affected by IP
  pool changes. However, DKIM keys and selectors must be provisioned and rotated across
  all nodes.

`cmd/dnsd` continuously validates SPF, DKIM, DMARC, PTR, and MX for monitored domains
and writes timestamped `dns_snapshots`. Any IP added to a pool that is not yet covered
by the tenant's SPF record will cause deliverability failures that MX Sentinel will
surface through the DNS validation and correlation pipeline.

---

## 6. Failure modes and responses

| Failure | Detection | Response |
|---|---|---|
| **Node crash / unreachable** | Submission distribution layer fails connections (LB health check, DNS TTL expiry, smart host connection timeout). `telemetryd` on that node stops emitting events. | Traffic routes to surviving nodes. Mail queued on the failed node defers. Operator investigates and recovers or drains the node manually. No tenant outage if N≥2. |
| **IP blocklisted** | `repd` queries DNSBLs on a polling schedule; `reputation.blacklist_hit` event and incident opened. | Remove IP from active pool. Requeue affected messages to alternate IP. Investigate abuse source; request delisting. |
| **Receiver throttling (4xx tempfail)** | `telemetryd` emits `smtp.deferred` events; `correld` detects a spike in deferrals to a specific provider. | Relay node reduces connection rate to that receiver (Postfix `smtp_destination_rate_delay`, PowerMTA `max-msg-rate`). No operator action required unless the spike is abnormal. MX Sentinel surfaces the pattern and duration. |
| **Receiver hard rejection (5xx)** | `telemetryd` emits `smtp.bounced` or `smtp.rejected`; `correld` correlates with recent DNS changes or IP changes. | Investigate root cause (blocklist, authentication failure, content policy). MX Sentinel's AI layer (`aid`) produces a root-cause narrative and remediation recommendation. |
| **Node NIC / IP routing failure** | Some messages deliver, others fail; `smtp.deferred` events spike for messages routed through that IP. PTR/FCrDNS checks begin failing. | Swap IP assignment on the node, update pool record in Postgres. `dnsd` will detect and surface any SPF/PTR misalignment. |
| **Region / datacenter outage** | All nodes in the affected region stop accepting connections. Smart hosts time out on submission. | For single-region deployments: mail queues at smart hosts (Exim queues) and retries begin. For multi-region deployments (Phase 4 full build-out): smart hosts can submit to nodes in the surviving region via the distribution layer. Relay nodes in the surviving region continue delivery. MX Sentinel nodes in the surviving region continue observing their respective relay nodes. |

### Multi-region note

A full multi-region relay HA deployment adds a second layer of distribution: a
geo-aware DNS or anycast VIP routes smart host submissions to the nearest healthy region.
Each region runs its own cluster of relay nodes with region-specific IP pools. This
requires coordinating SPF records (all regional IPs must be authorized), DKIM key
replication, and cross-region pool assignments in Postgres. MX Sentinel's observability
plane follows the same structure: one `telemetryd` instance per relay node, each
publishing to the regional NATS cluster, which can federate to a central analytics store.
Multi-region is explicitly a Phase 4 concern.

---

## 7. How MX Sentinel observes the cluster

### The observability plane is not in the mail path

This is the most important architectural property. `telemetryd` tails the relay node's
maillog as a passive observer — it does not sit between the smart host and the relay, and
it does not sit between the relay and the receiver. If `telemetryd` crashes, if NATS is
down, or if the MX Sentinel API is unreachable, **mail continues to flow unaffected**.
`telemetryd` spools parsed events to local disk and replays them when the bus comes back
up, so telemetry is also not permanently lost.

### Per-node telemetry

Each relay node runs one `telemetryd` instance that:

- Tails the node's maillog (Postfix `mail.log`, PowerMTA activity log, Haraka log).
- Parses each log line into a structured `smtp.*` event (delivered, deferred, bounced,
  rejected) with `relay_ip`, `relay_node` (hostname), `queue_id`, `message_id`,
  `envelope_from`, `recipient_domain`, `provider`, auth results, and timing metadata.
- Publishes to NATS JetStream; spools to disk on publish failure; replays on reconnect.

The `relay_node` and `relay_ip` fields in every event make it possible to filter,
aggregate, and compare behavior across nodes and IPs in ClickHouse.

### Per-IP reputation via repd

`cmd/repd` reads the `ip_pools` table's `addresses inet[]` and queries DNSBLs for each
IP on a configurable polling interval. It emits `reputation.blacklist_hit` events and
opens incidents. Because the pool-to-node mapping is explicit in Postgres, a blocklist
event is immediately attributable to a specific node and pool — operators see not just
"an IP is listed" but "the marketing pool IP on relay-node-2 is listed on Spamhaus ZEN."

### Per-provider, per-pool deliverability analytics

ClickHouse `smtp_events` is ordered by `(tenant_id, event_time, message_id)` and carries
`relay_ip`, `provider`, and `bounce_class` as first-class columns. The pre-aggregated
materialized views and the `correld` correlation engine enable:

- Per-node delivery rate vs. deferral rate vs. bounce rate.
- Per-IP reputation trend (delivery success rate over time for a given `relay_ip`).
- Per-provider throttling patterns (when does Gmail start issuing 4xx for a given IP
  pool?).
- Cross-node comparison (are all nodes seeing the same rejection rate from Microsoft, or
  is it isolated to one node's IPs?).

### Spike correlation to nodes, IPs, and DNS changes

`correld` joins `smtp.*` events against recent `dns.changed` events by timestamp window.
In an HA cluster, spikes that appear only on specific `relay_ip` or `relay_node` values
are distinct from spikes that appear across all nodes — the former points to an IP or
node-specific problem (blocklist, misconfigured PTR, TLS certificate mismatch); the
latter points to a sending-domain problem (DKIM selector removed, SPF broken) or a
receiver-side policy change. This cross-node correlation is a capability that a
single-node relay cannot provide.

### Observability summary

| Signal | Source | MX Sentinel component |
|---|---|---|
| SMTP delivery / deferral / bounce events per node | Relay maillog | `telemetryd` → NATS → ClickHouse |
| IP blocklist status | DNSBL queries | `repd` → `reputation.blacklist_hit` → incidents |
| DNS authentication state (SPF/DKIM/DMARC/PTR) | Live DNS resolver | `dnsd` → `dns_snapshots` |
| Rejection spike correlation | ClickHouse + DNS snapshots | `correld` → `reputation.rate_anomaly` |
| Root-cause narrative | Incident + telemetry | `aid` → `ai.rca` event |
| Deliverability analytics (per-provider/pool) | ClickHouse rollups | `apid` → dashboard |

---

## 8. What to build vs. what is deployment topology

### Deployment topology (ops, not MX Sentinel code)

The HA relay topology is achieved entirely through standard email infrastructure
operations and commodity tooling:

- **Relay nodes:** provision two or more Linux hosts, install and configure Postfix (or
  PowerMTA/Haraka), assign IP pools, set up DKIM signing, configure PTR records.
- **Submission distribution:** configure smart host `relayhost` entries or DNS
  round-robin or HAProxy for the submission VIP.
- **IP allocation:** request additional IPs from the hosting provider or cloud, assign
  them to relay node interfaces, configure PTR/FCrDNS with the DNS provider, add to SPF
  records.
- **Queue management:** Postfix spool directories on local fast disks; no shared
  filesystem.
- **Rolling maintenance:** drain scripts, monitoring of `mailq` depth, graceful process
  restart procedures.

None of this is MX Sentinel code. It is standard email operations.

### MX Sentinel's role

MX Sentinel provides the **visibility layer** that makes operating an HA relay cluster
tractable at scale:

- It tells you which node is having problems, not just that problems exist.
- It correlates a delivery failure to its root cause (IP blocklisted, DNS changed, TLS
  broken) rather than requiring manual log grep across N nodes.
- It tracks reputation trends per IP over time so warmup progress and blocklist risk are
  visible before a crisis.
- It surfaces anomalies (a node's deferral rate diverging from its peers) that would be
  invisible without cross-node telemetry aggregation.
- It provides the data model (`ip_pools`, `relay_nodes`) that makes pool-to-node
  assignments queryable and auditable.

The relay tier owns mail flow. MX Sentinel owns understanding of that mail flow.
