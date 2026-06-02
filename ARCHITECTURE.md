# MX Sentinel — Architecture

> **Canonical vision document.** This is the authoritative description of *what* MX
> Sentinel is and the shape of the system. Implementation decisions (language,
> libraries, repo layout) live in [`docs/tech-stack.md`](docs/tech-stack.md); the
> concrete near-term plan lives in [`docs/phase-1-plan.md`](docs/phase-1-plan.md).

## Overview

MX Sentinel is an AI-powered email infrastructure observability and deliverability
intelligence platform.

The system combines:

- SMTP relay telemetry
- DNS intelligence
- DMARC/SPF/DKIM analysis
- deliverability analytics
- reputation monitoring
- AI-based root cause analysis

The platform is designed for:

- hosting providers
- MSPs
- enterprise mail operators
- cPanel/Exim infrastructures
- outbound relay providers

MX Sentinel is **not** merely a DMARC reporting tool. It is an operational email
intelligence platform similar in philosophy to Datadog, Grafana, Cloudflare analytics,
and OpenTelemetry systems — but focused on email infrastructure.

---

## Core philosophy

The system is built around the following pipeline. Every subsystem should support this
workflow:

```
Collect → Normalize → Correlate → Analyze → Explain → Remediate
```

---

## High-level architecture

```
+------------------------------------------------------+
|                  Client DNS Zones                    |
|  SPF / DKIM / DMARC / MX / MTA-STS / TLS-RPT / BIMI  |
+-------------------------+----------------------------+
                          |
                          v
                +----------------------+
                | DNS Intelligence     |
                | Validation Engine    |
                +----------+-----------+
                           |
                           v
+------------------------------------------------------+
|                 cPanel / Exim Servers                |
|             SMTP Authenticated Smart Hosts           |
+-------------------------+----------------------------+
                          |
                          v
+------------------------------------------------------+
|                Relay Infrastructure                  |
|  Postfix / PowerMTA / Haraka                         |
|   - Queue Management        - Abuse Detection        |
|   - DKIM Signing            - SMTP Telemetry         |
|   - IP Pool Assignment      - Bounce Processing      |
|   - Rate Limiting           - TLS Validation         |
+-------------------------+----------------------------+
                          |
                          v
+------------------------------------------------------+
|                 Event Streaming Layer                |
|                    NATS / Kafka                      |
+-------------------------+----------------------------+
                          |
                          v
+------------------------------------------------------+
|          Correlation & Intelligence Engine           |
|   - SMTP Event Correlation   - Provider Analysis     |
|   - DNS State Correlation    - Deliverability Stats  |
|   - Reputation Analysis                              |
+-------------------------+----------------------------+
                          |
                          v
+------------------------------------------------------+
|                 AI Reasoning Layer                   |
|   - Root Cause Analysis     - Pattern Detection      |
|   - Failure Summaries       - Anomaly Classification |
|   - Remediation Guidance                             |
+-------------------------+----------------------------+
                          |
                          v
+------------------------------------------------------+
|                  API + Dashboard                     |
|   - Domain Health           - Deliverability Metrics |
|   - Message Explorer        - Alerts & Notifications |
|   - DNS Validation          - Client Self-Service    |
+------------------------------------------------------+
```

---

## Primary components

### 1. Relay infrastructure

The relay infrastructure is both the outbound mail transport **and** the telemetry
collection layer. Every SMTP transaction must emit structured telemetry events. This is
the operational core of MX Sentinel.

**Responsibilities**

- **SMTP relay:** outbound delivery, retry handling, queue management, TLS negotiation.
- **Authentication:** DKIM signing, SPF alignment support, DMARC compliance enforcement.
- **Reputation management:** IP pool segmentation, warmup logic, provider-specific tuning.
- **Abuse protection:** compromised-account detection, PHP-mail abuse detection, outbound
  spam heuristics, anomaly detection.
- **Telemetry generation:** every transaction emits delivery events, reject events,
  tempfails, bounce classifications, TLS metadata, and queue timing metrics.

**Recommended stack**

- Preferred: Postfix · Rspamd · OpenDKIM · Redis
- Optional enterprise: PowerMTA
- Optional programmable edge: Haraka

### 2. DNS Intelligence engine

Continuously validate and snapshot customer DNS state. Critical for root-cause
correlation.

**Validation targets**

- Authentication: SPF, DKIM, DMARC
- Infrastructure: MX, PTR, A/AAAA
- Security: DNSSEC, MTA-STS, TLS-RPT, SMTP TLS
- Modern standards: BIMI, ARC

**Advanced detection**

- **SPF:** lookup-limit exceeded, recursive includes, flattening problems, permerror.
- **DKIM:** stale selectors, weak keys, selector mismatch.
- **DMARC:** policy drift, missing rua/ruf, alignment failures.

**Snapshotting.** DNS state is versioned and timestamped, enabling correlations like:

```
10:41 — DKIM selector valid
10:44 — customer modified DNS
10:45 — Outlook rejections began
```

This capability is a major differentiator.

### 3. Event streaming layer

Decouple ingestion from processing; all systems communicate asynchronously.

- Initial: **NATS** (JetStream)
- Large scale: **Kafka**

Event categories: SMTP events (delivered/rejected/bounced/deferred), DNS events
(changes/validation failures), reputation events (blacklist hits/complaint spikes), AI
events (anomaly detections/remediation recommendations). See
[`docs/event-contracts.md`](docs/event-contracts.md).

### 4. Correlation engine

Combine SMTP telemetry, DNS state, provider responses, reputation data, and tenant
metadata into unified operational traces.

**Correlation keys** — primary: Message-ID, source IP, relay IP, DKIM selector, envelope
sender, timestamp window. Secondary: SMTP session, TLS fingerprint, queue ID.

**Example output**

```
Delivery Failure Detected
Provider:   Microsoft
Root Cause: DKIM selector missing after DNS change
Impact:     37 rejected messages
Remediate:  Restore selector selector2._domainkey.example.com
```

### 5. AI reasoning layer

Convert telemetry into actionable operational intelligence. The AI system is **not**
chatbot-centric; it is a diagnostic engine, summarization layer, and anomaly classifier.

- **Inputs:** SMTP events, DNS state, provider responses, historical trends, reputation
  telemetry.
- **Outputs:** human explanations (root-cause summaries, failure narratives),
  recommendations (DNS corrections, relay tuning, IP isolation), detection (spam
  outbreaks, rejection spikes, suspicious behavior).
- **Model strategy:** prefer local inference on metadata only. Candidate models: Mistral,
  Llama 3, Qwen, DeepSeek — served via Ollama, vLLM, or llama.cpp.

> **Privacy guardrail:** do **not** store or process full customer email bodies. Analyze
> headers, metadata, SMTP telemetry, and auth results. This avoids privacy liability,
> compliance risk, and storage explosion.

### 6. Storage architecture

- **PostgreSQL** — tenants, domains, users, DNS states, alert rules, configuration.
- **ClickHouse** — SMTP telemetry, event analytics, time-series observability.
- **Redis** — caching, queues, rate limiting, session state.
- **Object storage (S3/MinIO)** — raw DMARC XML, compressed logs, forensic reports.

See [`docs/data-model.md`](docs/data-model.md) for the rationale and entity model.

### 7. Dashboard & API

- **Domain health:** SPF/DKIM/DMARC status, reputation score.
- **Message explorer:** search by domain, sender, message-id, rejection reason.
- **Deliverability analytics:** per provider (Gmail, Microsoft, Yahoo, regional).
- **DNS validation:** real-time checks, historical drift, remediation guidance.
- **Alerts:** DNS breakage, blacklist events, rejection spikes, TLS failures.
- **API:** REST-first initially; possible future GraphQL / streaming APIs.

### 8. Multi-tenant design

Support hosting providers, MSPs, and enterprises. Each tenant has isolated telemetry,
isolated alerts, RBAC, and API credentials.

### 9. Security model

- **Never store:** full message bodies, attachments, sensitive content.
- **Encrypt:** API tokens, SMTP credentials, tenant secrets — via Vault / KMS / envelope
  encryption.

### 10. Deployment strategy

- Initial: Docker Compose, dedicated relay nodes, dedicated DB nodes.
- Later: Kubernetes, HA relay clusters, multi-region.
- Do **not** over-engineer orchestration early. Operational simplicity matters.

---

## Development phases

- **Phase 1 — Foundation:** relay telemetry, DNS validator, DMARC ingestion, PostgreSQL
  schema, dashboard MVP.
- **Phase 2 — Intelligence:** correlation engine, provider analytics, rejection analysis,
  reputation tracking.
- **Phase 3 — AI Diagnostics:** root-cause analysis, anomaly detection, remediation
  recommendations.
- **Phase 4 — Enterprise:** HA clusters, RBAC, APIs, multi-region, tenant federation.

---

## Long-term vision

MX Sentinel evolves into a unified email observability platform, relay intelligence
system, reputation management platform, DNS intelligence platform, and deliverability
operations center for modern email infrastructure.
