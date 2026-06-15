-- =====================================================================
-- Tenant migration 015: crm_record_history — CRM transition audit (v2).
--
-- One row per stage/status change (and approval) on a crm_record, so the
-- lead -> prospect -> customer journey and approvals are auditable.
-- =====================================================================

CREATE TABLE IF NOT EXISTS crm_record_history (
    crm_record_history_id   SERIAL       PRIMARY KEY,
    crm_record_id           INTEGER      NOT NULL REFERENCES crm_record(crm_record_id) ON DELETE CASCADE,
    from_type_id            INTEGER          NULL REFERENCES lkp_record_type(record_type_id),
    to_type_id              INTEGER          NULL REFERENCES lkp_record_type(record_type_id),
    from_crm_status_id      INTEGER          NULL REFERENCES lkp_crm_status(crm_status_id),
    to_crm_status_id        INTEGER          NULL REFERENCES lkp_crm_status(crm_status_id),
    action                  VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | approve
    actor_employee_id       INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                JSONB        NOT NULL DEFAULT '{}',
    at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_crm_record_history_record ON crm_record_history (crm_record_id);
