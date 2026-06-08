-- +goose Up
-- RBL/DNSBL self-monitoring state for the relay's own egress IPs (cmd/rbld). For each
-- (egress IP, DNSBL zone) pair rbld does a reversed-octet A lookup against the zone; an A
-- answer means the IP is LISTED on that zone, and a follow-up TXT lookup records the reason.
--
-- This is the relay watching ITS OWN sending IPs, not customer domains — so the rows are
-- keyed only on (ip, zone) and are global (no tenant_id). An egress IP is "healthy" when it
-- is listed on zero zones. listed_since is stamped on a clean->listed transition and cleared
-- on listed->clean, so the dashboard can show how long an IP has been blocklisted.
--
-- See cmd/rbld, internal/rbl, docs/deploy-relay.md §4.6 (the host-side healthy-IP hook that
-- rebuilds the Postfix randmap source from the healthy-IPs file written by rbld).

CREATE TABLE ip_blocklist_status (
    ip           TEXT        NOT NULL,           -- relay egress IP being monitored
    zone         TEXT        NOT NULL,           -- DNSBL zone, e.g. zen.spamhaus.org
    listed       BOOLEAN     NOT NULL DEFAULT FALSE,
    reason       TEXT,                           -- TXT record from the zone (delisting URL/cause)
    checked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    listed_since TIMESTAMPTZ,                    -- set on clean->listed, cleared on listed->clean
    PRIMARY KEY (ip, zone)
);
CREATE INDEX idx_ip_blocklist_status_listed ON ip_blocklist_status (listed) WHERE listed;

-- +goose Down
DROP TABLE IF EXISTS ip_blocklist_status;
