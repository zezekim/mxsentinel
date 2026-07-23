-- +goose Up
-- Deliverability Health Score snapshots (cmd/scored). A composite 0–100 score per domain that
-- fuses signals already collected across the platform — DMARC/auth alignment, feedback-loop
-- complaint volume, blocklist/reputation posture (repd/rbld), bounce/rejection ratio,
-- send-volume anomaly state, and Gmail Postmaster reputation — into one number, a letter grade,
-- and a component breakdown. cmd/scored recomputes and appends a snapshot per domain on an
-- interval so the dashboard can render a trend and so incident/AI explanations can cite "why".
--
-- This table is append-only history; the API reads the latest row per domain for the summary
-- view and the ordered rows for the trend/history view. The component breakdown is stored as
-- JSONB (the marshalled []healthscore.ComponentScore) so the exact per-component scores,
-- weights, impacts and human-readable details that produced the number are preserved without a
-- rigid column-per-signal schema.
--
-- See internal/healthscore (pure scorer + read-only collector), cmd/scored, and
-- docs/health-score.md.

CREATE TABLE health_score_snapshots (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id    UUID        NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    domain_name  TEXT        NOT NULL DEFAULT '',       -- denormalized for cheap listing/trend reads
    score        NUMERIC(5,2) NOT NULL,                 -- 0.00 .. 100.00
    grade        TEXT        NOT NULL,                  -- A|B|C|D|F, or N/A when no signal data
    has_data     BOOLEAN     NOT NULL DEFAULT TRUE,     -- false => grade N/A (insufficient signals)
    coverage     NUMERIC(4,3) NOT NULL DEFAULT 0,       -- fraction of weighted signals that had data
    components   JSONB       NOT NULL DEFAULT '[]'::jsonb, -- []healthscore.ComponentScore breakdown
    computed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Latest-per-domain lookups and per-domain trend reads both scan by (domain, time).
CREATE INDEX idx_health_score_domain_time  ON health_score_snapshots (domain_id, computed_at DESC);
-- Tenant-wide summary ("latest score for every domain") scans by (tenant, time).
CREATE INDEX idx_health_score_tenant_time  ON health_score_snapshots (tenant_id, computed_at DESC);

-- +goose Down
DROP TABLE IF EXISTS health_score_snapshots;
