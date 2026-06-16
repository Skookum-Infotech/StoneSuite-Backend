-- =====================================================================
-- Tenant migration 021: customer_history — CRM stage/status change trail (v2).
--
-- One row per stage/status change (and approval) on a customer record, so the
-- lead -> prospect -> customer journey and approvals are auditable. Mirrors the
-- (now superseded) crm_record_history but keyed to customer(customer_id).
-- =====================================================================

CREATE TABLE IF NOT EXISTS customer_history (
    customer_history_id     SERIAL       PRIMARY KEY,
    customer_id             INTEGER      NOT NULL REFERENCES customer(customer_id) ON DELETE CASCADE,
    from_type_id            INTEGER          NULL REFERENCES lkp_record_type(record_type_id),
    to_type_id              INTEGER          NULL REFERENCES lkp_record_type(record_type_id),
    from_crm_status_id      INTEGER          NULL REFERENCES lkp_crm_status(crm_status_id),
    to_crm_status_id        INTEGER          NULL REFERENCES lkp_crm_status(crm_status_id),
    action                  VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | approve
    actor_employee_id       INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                JSONB        NOT NULL DEFAULT '{}',
    at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_customer_history_record ON customer_history (customer_id);
