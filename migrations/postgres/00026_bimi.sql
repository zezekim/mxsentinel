-- +goose Up
-- BIMI / VMC readiness snapshots (cmd/bimid). BIMI (Brand Indicators for Message
-- Identification) lets mailbox providers display a brand's logo beside authenticated mail —
-- but only once the domain reaches DMARC enforcement (p=quarantine or p=reject). bimid polls
-- each monitored domain, resolves the default._bimi.<domain> TXT record, validates the logo
-- (SVG Tiny P/S) and any Verified Mark Certificate (VMC), cross-checks DMARC enforcement from
-- the latest DNS snapshot, and records the resulting readiness state plus a "what's blocking
-- BIMI" checklist here. One row is written per poll only when the assessment changes, giving a
-- drift timeline; the API serves the latest row per domain.
--
-- readiness_state values:
--   not_configured - no BIMI record published yet
--   blocked        - record present but a hard prerequisite is unmet (DMARC / logo)
--   partial        - logo valid + DMARC enforced, but no valid VMC (shows in some providers)
--   vmc_expired    - VMC present but the certificate has expired
--   ready          - record + logo + DMARC enforcement + valid VMC

CREATE TABLE bimi_snapshots (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id       UUID        NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    record          TEXT        NOT NULL DEFAULT '',   -- raw default._bimi TXT record ("" if none)
    logo_url        TEXT        NOT NULL DEFAULT '',   -- l= tag
    vmc_url         TEXT        NOT NULL DEFAULT '',   -- a= tag
    vmc_expiry      TIMESTAMPTZ,                       -- leaf VMC NotAfter, NULL when no/invalid VMC
    dmarc_enforced  BOOLEAN     NOT NULL DEFAULT false,
    readiness_state TEXT        NOT NULL DEFAULT 'not_configured',
    checklist_json  JSONB       NOT NULL DEFAULT '[]',
    checked_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Serve the latest snapshot per domain quickly, and per-tenant summaries.
CREATE INDEX idx_bimi_snapshots_domain_checked ON bimi_snapshots (domain_id, checked_at DESC);
CREATE INDEX idx_bimi_snapshots_tenant ON bimi_snapshots (tenant_id, readiness_state);

-- +goose Down
DROP TABLE IF EXISTS bimi_snapshots;
