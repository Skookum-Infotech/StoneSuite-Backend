-- =====================================================================
-- Tenant migration 022: deactivate legacy record_status entries for
-- Lead, Prospect, and Customer record types.
--
-- The CRM workflow is now driven exclusively by lkp_crm_status
-- (Lead-Qualified, Prospect-In Discussion, Customer-Closed Won, etc.).
-- The generic Active/Inactive/Cancelled entries in lkp_record_status
-- for record types LEAD (1), PROS (2), and CUST (3) are no longer
-- surfaced in any UI or API. Deactivating (not deleting) preserves
-- referential integrity on any existing rows that used them.
-- =====================================================================

UPDATE lkp_record_status
SET record_status_is_active = FALSE
WHERE record_status_record_type IN (
    SELECT record_type_id FROM lkp_record_type
    WHERE record_type_code IN ('LEAD', 'PROS', 'CUST')
);
