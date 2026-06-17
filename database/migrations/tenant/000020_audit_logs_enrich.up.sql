-- =====================================================================
-- Tenant migration 020: enrich audit_logs into the unified change trail.
--
-- audit_logs was created by migration 011 (record attachments) with:
--   id, actor_user_id, action, resource, resource_id, details, created_at
-- The workbook (Audit_Logs sheet) asks for a richer row-level change trail.
-- We add its columns additively so ONE table serves both attachment events
-- and CRM mutations (ADR-002). Mapping to the workbook's field names:
--   actor_user_id = Changed By   created_at = Changed At   resource_id = Record ID
--
-- Guard: tenants whose schema_version already recorded 011 before audit_logs
-- was added to that migration will not have the table yet. Create it here so
-- the ALTER TABLE statements below always succeed.
-- =====================================================================

CREATE TABLE IF NOT EXISTS audit_logs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    action        TEXT        NOT NULL,
    resource      TEXT        NOT NULL,
    resource_id   TEXT        NOT NULL DEFAULT '',
    details       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- DESIGN NOTE: actor_user_id references the UUID users table (v1 identity model).
-- The v2 CRM uses INTEGER employee IDs. Until the identity model is unified, CRM
-- audit entries written via the employee path should set actor_user_id = NULL and
-- store the employee_id in the details JSONB field as {"employee_id": N}.
-- See: https://github.com/Skookum-Infotech/StoneSuite/issues/XXX (track unification)
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor      ON audit_logs(actor_user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action     ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_resource   ON audit_logs(resource, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC);

ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS table_name  TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS old_value   JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS new_value   JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS ip_address  INET;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS session_id  TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS app_version TEXT;

CREATE INDEX IF NOT EXISTS idx_audit_logs_table ON audit_logs(table_name);
