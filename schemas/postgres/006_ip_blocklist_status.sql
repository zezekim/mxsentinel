-- MX Sentinel — PostgreSQL reference DDL: ip_blocklist_status (RBL/DNSBL self-monitoring).
--
-- Runnable migration: migrations/postgres/00006_ip_blocklist_status.sql (keep in sync).
--
-- Per-(egress IP, DNSBL zone) blocklist state maintained by cmd/rbld. rbld does a
-- reversed-octet A lookup against each zone (an A answer = LISTED) and a TXT lookup for the
-- reason, then upserts a row here. The relay watches ITS OWN sending IPs, so rows are global
-- (no tenant_id) and keyed on (ip, zone). An egress IP is healthy when listed on zero zones.
-- listed_since is stamped on clean->listed and cleared on listed->clean.

CREATE TABLE ip_blocklist_status (
    ip           TEXT        NOT NULL,
    zone         TEXT        NOT NULL,
    listed       BOOLEAN     NOT NULL DEFAULT FALSE,
    reason       TEXT,
    checked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    listed_since TIMESTAMPTZ,
    PRIMARY KEY (ip, zone)
);
CREATE INDEX idx_ip_blocklist_status_listed ON ip_blocklist_status (listed) WHERE listed;
