-- =====================================================================
-- Tenant-template schema — Phase 6: custom_fields JSONB on CRM tables.
-- Allows admins to add up to 15 custom fields (via workflow_field_definitions)
-- to Lead and Prospect records without requiring schema migrations.
-- =====================================================================

ALTER TABLE leads     ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}';
ALTER TABLE prospects ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_leads_custom     ON leads     USING gin(custom_fields);
CREATE INDEX IF NOT EXISTS idx_prospects_custom ON prospects USING gin(custom_fields);
