-- =====================================================================
-- Tenant migration 020: enrich audit_logs into the unified change trail.
--
-- audit_logs was created by migration 011 (record attachments) with:
--   id, actor_user_id, action, resource, resource_id, details, created_at
-- The workbook (Audit_Logs sheet) asks for a richer row-level change trail.
-- We add its columns additively so ONE table serves both attachment events
-- and CRM mutations (ADR-002). Mapping to the workbook's field names:
--   actor_user_id = Changed By   created_at = Changed At   resource_id = Record ID
-- =====================================================================

ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS table_name  TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS old_value   JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS new_value   JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS ip_address  INET;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS session_id  TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS app_version TEXT;

CREATE INDEX IF NOT EXISTS idx_audit_logs_table ON audit_logs(table_name);
