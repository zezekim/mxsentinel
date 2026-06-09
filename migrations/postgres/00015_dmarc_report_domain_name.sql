-- +goose Up
-- Store the domain name directly on dmarc_reports so pulled reports (where the
-- domain is not registered in the tenant's domains table) still display correctly.
ALTER TABLE dmarc_reports ADD COLUMN IF NOT EXISTS domain_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE dmarc_reports DROP COLUMN IF EXISTS domain_name;
