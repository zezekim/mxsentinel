-- +goose Up
-- Synthetic SMTP probing (cmd/probed). Active health checks of the relay's own SMTP
-- endpoints (ports 25/465/587) that complement the platform's passive maillog telemetry.
-- Each probe records TCP connect latency, the SMTP banner, parsed EHLO capabilities,
-- STARTTLS / implicit-TLS negotiation, the TLS certificate's expiry and chain validity,
-- AUTH advertisement, and optional greylisting behaviour.
--
-- Like ip_blocklist_status (00006), this is the relay watching ITS OWN endpoints — it is
-- relay-wide infrastructure state, NOT customer/tenant data, so the rows carry no tenant_id.
-- Postgres holds the current status (latest row per endpoint) and a rolling recent history;
-- the high-frequency latency/uptime time series lives in ClickHouse (00008_smtp_probe).
--
-- See cmd/probed, internal/smtpprobe, docs/smtp-probing.md.

-- smtp_probe_targets: the configured endpoint universe (optional; probed also derives targets
-- from MXS_PROBE_* env). Lets the dashboard show an endpoint before its first result lands.
CREATE TABLE smtp_probe_targets (
    endpoint   TEXT        PRIMARY KEY,          -- "host:port"
    host       TEXT        NOT NULL,
    port       INTEGER     NOT NULL,
    mode       TEXT        NOT NULL DEFAULT 'starttls', -- plain | starttls | implicit_tls
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- smtp_probe_results: one row per probe execution per endpoint.
CREATE TABLE smtp_probe_results (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint               TEXT        NOT NULL,        -- "host:port"
    host                   TEXT        NOT NULL,
    port                   INTEGER     NOT NULL,
    mode                   TEXT        NOT NULL DEFAULT 'starttls',
    ok                     BOOLEAN     NOT NULL DEFAULT FALSE,
    stage                  TEXT,                        -- complete | connect | banner | ehlo | starttls | tls_handshake | response
    error                  TEXT,
    latency_ms             BIGINT      NOT NULL DEFAULT 0,   -- TCP connect latency
    banner                 TEXT,
    starttls_offered       BOOLEAN     NOT NULL DEFAULT FALSE,
    auth_advertised        BOOLEAN     NOT NULL DEFAULT FALSE,
    auth_mechs             TEXT[]      NOT NULL DEFAULT '{}',
    tls_negotiated         BOOLEAN     NOT NULL DEFAULT FALSE,
    tls_version            TEXT,
    tls_cipher             TEXT,
    tls_chain_valid        BOOLEAN     NOT NULL DEFAULT FALSE,
    cert_subject           TEXT,
    cert_issuer            TEXT,
    cert_not_after         TIMESTAMPTZ,
    cert_days_until_expiry INTEGER,
    cert_expiring          BOOLEAN     NOT NULL DEFAULT FALSE,
    greylisting            BOOLEAN     NOT NULL DEFAULT FALSE,
    response_code          INTEGER     NOT NULL DEFAULT 0,
    probed_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Time-series-friendly: current status = DISTINCT ON (endpoint) ORDER BY endpoint, probed_at DESC.
CREATE INDEX idx_smtp_probe_results_endpoint_time ON smtp_probe_results (endpoint, probed_at DESC);
-- Fast scans of recent activity across all endpoints, and of open failures / expiring certs.
CREATE INDEX idx_smtp_probe_results_probed_at ON smtp_probe_results (probed_at DESC);
CREATE INDEX idx_smtp_probe_results_problems ON smtp_probe_results (probed_at DESC)
    WHERE ok = FALSE OR cert_expiring = TRUE;

-- +goose Down
DROP TABLE IF EXISTS smtp_probe_results;
DROP TABLE IF EXISTS smtp_probe_targets;
