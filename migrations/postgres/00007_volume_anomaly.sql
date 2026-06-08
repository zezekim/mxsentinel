-- +goose Up
-- Send-volume anomaly detection (cmd/anomalyd). On a shared outbound relay, a single
-- client domain that suddenly sends far more than usual is the classic signature of a
-- compromised account or a runaway script — the kind of spike that gets the shared IP
-- pool blocklisted. anomalyd consumes mxs.smtp.> telemetry, keeps a per-(tenant,domain)
-- rolling hourly count, and compares each completed hour against an EWMA baseline.
--
-- sender_volume_baseline holds the learned per-domain hourly baseline (exponentially
-- weighted moving average) plus a sample count so we know when a domain is "warmed up"
-- enough to trust. tenant_id/sender_domain are TEXT (not FK'd to tenants/domains) because
-- the producer attributes by the From: domain seen on the wire, which may not yet be a
-- registered domain row — we still want to baseline and alert on it.
--
-- volume_anomaly is the append-only log of tripped spikes (one row per detection), read
-- by GET /v1/anomaly/recent and the dashboard's Velocity page. The incident itself is
-- opened separately in the incidents table (critical, deduped per source_event_id).

CREATE TABLE sender_volume_baseline (
    tenant_id     TEXT             NOT NULL,
    sender_domain TEXT             NOT NULL,
    ewma_hourly   DOUBLE PRECISION NOT NULL DEFAULT 0,  -- EWMA of completed-hour message counts
    samples       BIGINT           NOT NULL DEFAULT 0,  -- completed hours folded into the EWMA
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, sender_domain)
);

CREATE TABLE volume_anomaly (
    id                 UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          TEXT             NOT NULL,
    sender_domain      TEXT             NOT NULL,
    observed_hour_count BIGINT          NOT NULL,             -- messages in the spiking hour
    baseline           DOUBLE PRECISION NOT NULL,             -- EWMA baseline at trip time
    factor             DOUBLE PRECISION NOT NULL,             -- observed / baseline (∞ guarded to 0 baseline)
    detected_at        TIMESTAMPTZ      NOT NULL DEFAULT now()
);

-- Recent-first lookups, per tenant, for the API and dashboard.
CREATE INDEX idx_volume_anomaly_tenant_time ON volume_anomaly (tenant_id, detected_at DESC);

-- +goose Down
DROP TABLE IF EXISTS volume_anomaly;
DROP TABLE IF EXISTS sender_volume_baseline;
