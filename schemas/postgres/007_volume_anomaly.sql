-- MX Sentinel — PostgreSQL reference DDL: send-volume anomaly detection.
--
-- Runnable migration: migrations/postgres/00007_volume_anomaly.sql (keep in sync).
--
-- cmd/anomalyd consumes mxs.smtp.> telemetry, keeps a per-(tenant, sender_domain) rolling
-- hourly count, and at each hour boundary compares the completed hour against an EWMA
-- baseline. A spike trips when the current-hour count exceeds max(baseline*FACTOR,
-- MIN_ABSOLUTE) once the domain is warmed up (samples >= MIN_SAMPLES). tenant_id and
-- sender_domain are TEXT (not FK'd) because attribution is by the From: domain on the
-- wire, which may not yet be a registered domain row.

CREATE TABLE sender_volume_baseline (
    tenant_id     TEXT             NOT NULL,
    sender_domain TEXT             NOT NULL,
    ewma_hourly   DOUBLE PRECISION NOT NULL DEFAULT 0,
    samples       BIGINT           NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, sender_domain)
);

CREATE TABLE volume_anomaly (
    id                  UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           TEXT             NOT NULL,
    sender_domain       TEXT             NOT NULL,
    observed_hour_count BIGINT           NOT NULL,
    baseline            DOUBLE PRECISION NOT NULL,
    factor              DOUBLE PRECISION NOT NULL,
    detected_at         TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX idx_volume_anomaly_tenant_time ON volume_anomaly (tenant_id, detected_at DESC);
