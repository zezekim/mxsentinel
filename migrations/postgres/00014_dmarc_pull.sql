-- +goose Up
-- Tracks the sync cursor per tenant for the external DMARC receiver pull integration.
-- last_date_end is the date_end of the last successfully synced report batch;
-- the puller passes this as `since` on the next poll to fetch only new reports.
CREATE TABLE dmarc_pull_cursors (
    tenant_id    UUID        NOT NULL PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    last_date_end TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01T00:00:00Z',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS dmarc_pull_cursors;
