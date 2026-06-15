-- =====================================================================
-- Control-plane migration 006: per-tenant database design version.
--
-- design_version selects which CRM data design a tenant's requests use:
--   'v1' = the JSONB workflow_records engine (original design)
--   'v2' = the relational lkp_* + crm_record design
--
-- Both schemas coexist in a tenant database (table names do not collide),
-- so switching is a flag flip with no data migration. Defaults to 'v1'
-- so all existing tenants keep their current behavior.
-- =====================================================================

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS design_version VARCHAR(16) NOT NULL DEFAULT 'v1';
