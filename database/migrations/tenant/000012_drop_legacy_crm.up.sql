-- SAFETY: This migration permanently drops the `leads` and `prospects` tables (CASCADE).
-- Before this migration was embedded, the following was verified:
--   1. No application code queries leads/prospects (replaced by workflow_records + crm_record).
--   2. All active tenants had their data migrated to workflow_records before this ran.
--   3. A Neon branch snapshot was taken as a recovery point.
-- Recovery: Neon point-in-time restore or branch restore only. No down-migration exists.

-- =====================================================================
-- Tenant migration 011: drop legacy CRM tables.
--
-- The dedicated `leads` and `prospects` typed tables (migrations 004/005)
-- are dead code: the UI and API route CRM through workflow_records (v1) and
-- now the relational crm_record (v2). Remove them so the CRM is a single
-- table per design. Forward-only; recovery is via Neon branch/restore.
-- =====================================================================

DROP TABLE IF EXISTS leads CASCADE;
DROP TABLE IF EXISTS prospects CASCADE;
