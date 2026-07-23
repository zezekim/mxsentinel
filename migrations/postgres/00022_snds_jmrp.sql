-- +goose Up
-- Microsoft Outlook/Hotmail deliverability signals (cmd/sndsd). This is the Microsoft mirror
-- of 00008_fbl_reputation.sql (which covers the Gmail/Google half): two tables feed the
-- /v1/microsoft/snds + /v1/microsoft/jmrp views and the abuse-incident rollup.
--
--   snds_ip_data     one row per (sending IP, day) from Microsoft's Smart Network Data Services
--                    automated-data CSV. Carries the filter verdict (GREEN/YELLOW/RED), the
--                    complaint-rate band, spam-trap hits, and sample HELO/MAIL FROM. Attributed
--                    to the tenant that owns the egress IP (relay_nodes / ip_pools). This is the
--                    authoritative operational state; long-horizon per-IP-per-day history for
--                    trend charts is additionally streamed to ClickHouse
--                    (migrations/clickhouse/00006_snds.sql). SNDS volume is low (a handful of
--                    egress IPs x ~30 retained days), so Postgres is the primary store.
--
--   jmrp_complaints  per (sending domain, sending IP, day) SUMMARY of Junk Mail Reporting
--                    Program ARF complaints, upserted with a running count. JMRP uses the same
--                    ARF format as the Google FBL (parsed via internal/fbl.ParseARF), attributed
--                    per SENDING DOMAIN like fbl (one relay credential fronts many client
--                    domains). Privacy boundary: no bodies/subjects are ever stored.

CREATE TABLE snds_ip_data (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    ip                 TEXT        NOT NULL,                    -- sending (egress) IP
    data_date          DATE        NOT NULL,                    -- UTC day of the activity period
    rcpt_count         BIGINT      NOT NULL DEFAULT 0,          -- RCPT commands seen
    data_count         BIGINT      NOT NULL DEFAULT 0,          -- DATA commands (messages) seen
    message_recipients BIGINT      NOT NULL DEFAULT 0,
    filter_result      TEXT        NOT NULL DEFAULT '',         -- GREEN | YELLOW | RED
    complaint_band     TEXT        NOT NULL DEFAULT '',         -- e.g. '< 0.1%'
    trap_hits          INTEGER     NOT NULL DEFAULT 0,          -- spam-trap hits
    sample_helo        TEXT        NOT NULL DEFAULT '',
    sample_from        TEXT        NOT NULL DEFAULT '',
    activity_start     TIMESTAMPTZ,
    activity_end       TIMESTAMPTZ,
    fetched_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, ip, data_date)
);
-- Current-state view (latest row per IP) and per-IP trend both scan by tenant/ip/date.
CREATE INDEX idx_snds_ip_data_tenant_ip_date ON snds_ip_data (tenant_id, ip, data_date DESC);

CREATE TABLE jmrp_complaints (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sender_domain   TEXT        NOT NULL DEFAULT '',            -- From: domain of complained-about mail
    sending_ip      TEXT        NOT NULL DEFAULT '',            -- Source-IP the complaint attributes to
    feedback_type   TEXT        NOT NULL DEFAULT 'abuse',       -- ARF Feedback-Type
    provider        TEXT        NOT NULL DEFAULT 'microsoft',
    complaint_date  DATE        NOT NULL,
    complaint_count INTEGER     NOT NULL DEFAULT 0,
    first_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, sender_domain, sending_ip, complaint_date)
);
CREATE INDEX idx_jmrp_complaints_tenant_domain_date ON jmrp_complaints (tenant_id, sender_domain, complaint_date DESC);

-- +goose Down
DROP TABLE IF EXISTS jmrp_complaints;
DROP TABLE IF EXISTS snds_ip_data;
