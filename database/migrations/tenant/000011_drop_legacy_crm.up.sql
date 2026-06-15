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
