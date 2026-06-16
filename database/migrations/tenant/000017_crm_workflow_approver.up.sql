-- =====================================================================
-- Tenant migration 016: crm_workflow_approver — configurable approvers (v2).
--
-- Configures which employee may approve a CRM record at a given stage/status
-- (e.g. record_type = CUST, crm_status = Customer Closed Won). When a customer
-- is marked Closed Won it becomes pending approval; only a configured approver
-- may approve it, after which it is eligible for downstream work.
-- =====================================================================

CREATE TABLE IF NOT EXISTS crm_workflow_approver (
    crm_workflow_approver_id SERIAL      PRIMARY KEY,
    record_type_id           INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),
    crm_status_id            INTEGER         NULL REFERENCES lkp_crm_status(crm_status_id),
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at               TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by               INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_crm_workflow_approver UNIQUE (record_type_id, crm_status_id, approver_employee_id)
);

CREATE INDEX IF NOT EXISTS idx_crm_workflow_approver_lookup
    ON crm_workflow_approver (record_type_id, crm_status_id) WHERE is_active;
