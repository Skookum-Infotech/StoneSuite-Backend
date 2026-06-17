-- =====================================================================
-- Control-plane migration 007: switch all tenants to design version v2.
--
-- The v2 relational design (lkp_* + customer table) is now the only
-- supported CRM design. This migration promotes all tenants — including
-- those still on the default 'v1' — to 'v2' so they use the relational
-- store with the correct status lists (lkp_crm_status).
-- =====================================================================

UPDATE tenants SET design_version = 'v2' WHERE design_version IS NULL OR design_version = 'v1';

ALTER TABLE tenants ALTER COLUMN design_version SET DEFAULT 'v2';
