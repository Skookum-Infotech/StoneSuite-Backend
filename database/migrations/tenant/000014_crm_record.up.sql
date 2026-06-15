-- =====================================================================
-- Tenant migration 014: crm_record — the single CRM master table (v2).
--
-- One physical table holds Lead, Prospect and Customer records; the
-- crm_record_type_id (LEAD/PROS/CUST in lkp_record_type) decides which
-- listing a record appears in. Stage advances forward-only
-- (LEAD -> PROS -> CUST) by choosing a crm_status of a later type.
-- Hybrid storage: typed columns + FKs to lkp_* PLUS a custom_fields JSONB
-- for the <=15 admin-defined dynamic fields (validated against
-- workflow_field_definitions of the matching workflow).
-- =====================================================================

CREATE TABLE IF NOT EXISTS crm_record (
    crm_record_id            SERIAL       PRIMARY KEY,
    crm_record_uuid          UUID         NOT NULL DEFAULT gen_random_uuid(),

    -- CRM stage + status (drive listing + transitions)
    crm_record_type_id       INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    crm_record_status_id     INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    crm_record_crm_status_id INTEGER          NULL REFERENCES lkp_crm_status(crm_status_id),

    -- Core typed fields
    crm_record_company_name  VARCHAR(255) NOT NULL DEFAULT '',
    crm_record_first_name    VARCHAR(100) NOT NULL DEFAULT '',
    crm_record_last_name     VARCHAR(100) NOT NULL DEFAULT '',
    crm_record_email         VARCHAR(255) NOT NULL DEFAULT '',
    crm_record_phone         VARCHAR(50)  NOT NULL DEFAULT '',
    crm_record_address       TEXT         NOT NULL DEFAULT '',

    -- Lookup FKs (optional)
    crm_record_customer_type_id INTEGER       NULL REFERENCES lkp_customer_type(customer_type_id),
    crm_record_ar_status_id     INTEGER       NULL REFERENCES lkp_customer_ar_status(customer_ar_status_id),
    crm_record_payment_terms_id INTEGER       NULL REFERENCES lkp_payment_terms(payment_terms_id),
    crm_record_currency_id      INTEGER       NULL REFERENCES lkp_currency(currency_id),
    crm_record_country_id       INTEGER       NULL REFERENCES lkp_country(country_id),
    crm_record_state_id         INTEGER       NULL REFERENCES lkp_state(state_id),
    crm_record_lead_source_id   INTEGER       NULL REFERENCES lkp_crm_lead_source(lead_source_id),
    crm_record_contact_method_id INTEGER      NULL REFERENCES lkp_contact_method(contact_method_id),
    crm_record_owner_employee_id INTEGER      NULL REFERENCES employee(employee_id),

    -- Lineage (lead -> prospect -> customer conversion)
    crm_record_parent_id     INTEGER          NULL REFERENCES crm_record(crm_record_id),

    -- Approval (Customer Closed Won requires approver sign-off)
    crm_record_is_approved      BOOLEAN   NOT NULL DEFAULT FALSE,
    crm_record_approval_status  VARCHAR(10) NOT NULL DEFAULT 'none', -- none | pending | approved
    crm_record_approved_by      INTEGER       NULL REFERENCES employee(employee_id),
    crm_record_approved_at      TIMESTAMP     NULL,

    -- Dynamic fields (<=15, validated against workflow_field_definitions)
    crm_record_custom_fields JSONB        NOT NULL DEFAULT '{}',

    -- Audit / soft-delete / optimistic concurrency
    crm_record_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    crm_record_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    crm_record_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    crm_record_created_by    INTEGER          NULL REFERENCES employee(employee_id),
    crm_record_updated_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    crm_record_deleted_at    TIMESTAMP        NULL,
    crm_record_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    crm_record_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_crm_record_uuid UNIQUE (crm_record_uuid),
    CONSTRAINT chk_crm_record_approval_status CHECK (crm_record_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_crm_record_soft_delete CHECK (
        (crm_record_deleted_at IS NULL AND crm_record_deleted_by IS NULL) OR
        (crm_record_deleted_at IS NOT NULL AND crm_record_deleted_by IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_crm_record_type   ON crm_record (crm_record_type_id) WHERE crm_record_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_record_status ON crm_record (crm_record_crm_status_id) WHERE crm_record_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_record_owner  ON crm_record (crm_record_owner_employee_id) WHERE crm_record_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_record_parent ON crm_record (crm_record_parent_id) WHERE crm_record_parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_crm_record_custom_fields ON crm_record USING GIN (crm_record_custom_fields);
