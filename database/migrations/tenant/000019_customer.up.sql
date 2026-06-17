-- =====================================================================
-- Tenant migration 019: customer — the single CRM master table (v2).
-- Source of truth: StonSuite_DBSchema.xlsx (Customer sheet), ADR-002.
--
-- One physical table holds Lead, Prospect and Customer records, distinguished
-- by record_type (FK -> lkp_record_type: LEAD/PROS/CUST). Stage advances
-- forward-only (LEAD -> PROS -> CUST) by choosing a crm_status of a later type.
-- Supersedes crm_record (migration 015), which is left in place but unused.
--
-- Design notes (ADR-002):
--   * ss_customer_id is a plain integer owner-stamp (no cross-DB FK — the
--     control plane is a separate database); ss_tenant_id is omitted because
--     the DB connection itself is the tenant scope (database-per-tenant).
--   * customer_uuid is the external/API id (non-enumerable); customer_id is
--     the internal serial PK used by FKs.
--   * Business columns that are mandatory only for certain record types per the
--     workbook (lead-cycle, billing/shipping, sales) are stored NULLABLE here;
--     conditional requiredness is enforced in the Go validator, not the DB,
--     because it depends on record_type. Text columns default '' and booleans
--     default FALSE so partial Lead inserts succeed.
-- =====================================================================

CREATE TABLE IF NOT EXISTS customer (
    customer_id                        SERIAL       PRIMARY KEY,
    customer_uuid                      UUID         NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                     INTEGER          NULL,  -- platform owner stamp, no FK
    customer_doc_num                   VARCHAR(20)      NULL,  -- generated post-insert (e.g. LEAD-000001)

    -- Stage + statuses
    record_type                        INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    customer_crm_status                INTEGER          NULL REFERENCES lkp_crm_status(crm_status_id),
    customer_status                    INTEGER          NULL REFERENCES lkp_record_status(record_status_id),

    -- Primary information
    customer_name                      VARCHAR(100) NOT NULL DEFAULT '',
    customer_dba_name                  VARCHAR(100) NOT NULL DEFAULT '',
    customer_tax_id                    VARCHAR(50)  NOT NULL DEFAULT '',
    customer_type                      INTEGER          NULL REFERENCES lkp_customer_type(customer_type_id),
    customer_authorized_person_fname   VARCHAR(50)  NOT NULL DEFAULT '',
    customer_authorized_person_lname   VARCHAR(50)  NOT NULL DEFAULT '',
    customer_is_child                  BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_parent_company            INTEGER          NULL REFERENCES customer(customer_id),
    customer_ar_status                 INTEGER          NULL REFERENCES lkp_customer_ar_status(customer_ar_status_id),

    -- Contact information
    customer_primary_phonenum          VARCHAR(20)  NOT NULL DEFAULT '',
    customer_alt_phonenum              VARCHAR(20)  NOT NULL DEFAULT '',
    customer_faxnum                    VARCHAR(20)  NOT NULL DEFAULT '',
    customer_cmpny_website             VARCHAR(150) NOT NULL DEFAULT '',
    customer_contact_email             VARCHAR(100) NOT NULL DEFAULT '',
    customer_accounts_email            VARCHAR(100) NOT NULL DEFAULT '',
    customer_addl_email                VARCHAR(100) NOT NULL DEFAULT '',

    -- Primary address
    customer_addr_line1                VARCHAR(100) NOT NULL DEFAULT '',
    customer_addr_line2                VARCHAR(100) NOT NULL DEFAULT '',
    customer_addr_suitenum             VARCHAR(20)  NOT NULL DEFAULT '',
    customer_addr_city                 VARCHAR(100) NOT NULL DEFAULT '',
    customer_addr_state                INTEGER          NULL REFERENCES lkp_state(state_id),
    customer_addr_zip                  VARCHAR(10)  NOT NULL DEFAULT '',
    customer_addr_country              INTEGER          NULL REFERENCES lkp_country(country_id),

    -- Billing address
    customer_is_bill_as_primary        BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_bill_addr_line1           VARCHAR(100) NOT NULL DEFAULT '',
    customer_bill_addr_line2           VARCHAR(100) NOT NULL DEFAULT '',
    customer_bill_addr_suitenum        VARCHAR(20)  NOT NULL DEFAULT '',
    customer_bill_addr_city            VARCHAR(100) NOT NULL DEFAULT '',
    customer_bill_addr_state           INTEGER          NULL REFERENCES lkp_state(state_id),
    customer_bill_addr_zip             VARCHAR(10)  NOT NULL DEFAULT '',
    customer_bill_addr_country         INTEGER          NULL REFERENCES lkp_country(country_id),

    -- Shipping address
    customer_is_ship_as_primary        BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_ship_addr_line1           VARCHAR(100) NOT NULL DEFAULT '',
    customer_ship_addr_line2           VARCHAR(100) NOT NULL DEFAULT '',
    customer_ship_addr_suitenum        VARCHAR(20)  NOT NULL DEFAULT '',
    customer_ship_addr_city            VARCHAR(100) NOT NULL DEFAULT '',
    customer_ship_addr_state           INTEGER          NULL REFERENCES lkp_state(state_id),
    customer_ship_addr_zip             VARCHAR(10)  NOT NULL DEFAULT '',
    customer_ship_addr_country         INTEGER          NULL REFERENCES lkp_country(country_id),

    -- CRM / sales-cycle fields (mandatory for LEAD/PROS — enforced in Go)
    customer_crm_owner_user_id         INTEGER          NULL REFERENCES employee(employee_id),
    customer_lead_source               INTEGER          NULL REFERENCES lkp_crm_lead_source(lead_source_id),
    customer_lead_score                INTEGER          NULL,
    customer_expected_close_date       DATE             NULL,
    customer_expected_deal_value       DECIMAL(15,2)    NULL,
    customer_last_contacted_date       DATE             NULL,
    customer_preferred_contact_method  INTEGER          NULL REFERENCES lkp_contact_method(contact_method_id),
    customer_do_not_contact            BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_internal_notes            TEXT         NOT NULL DEFAULT '',

    -- Sales fields
    customer_sales_rep_user_id         INTEGER          NULL REFERENCES employee(employee_id),
    customer_price_level               INTEGER          NULL REFERENCES lkp_price_level(price_level_id),
    customer_is_tax_exempt             BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_tax_exempt_reason         TEXT         NOT NULL DEFAULT '',
    customer_tax_exempt_cert_num       VARCHAR(50)  NOT NULL DEFAULT '',
    customer_tax_exempt_cert_file_id   VARCHAR(250) NOT NULL DEFAULT '',
    customer_tax_exempt_expiry_date    DATE             NULL,
    customer_sales_tax_percent         DECIMAL(6,4)     NULL,
    customer_payment_terms             INTEGER          NULL REFERENCES lkp_payment_terms(payment_terms_id),
    customer_credit_limit              DECIMAL(15,2)    NULL,
    customer_is_credit_lock            BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_credit_lock_reason        TEXT         NOT NULL DEFAULT '',

    -- Balances
    customer_total_balance             DECIMAL(15,2)    NULL,
    customer_deposit_balance           DECIMAL(15,2)    NULL,
    customer_overdue_balance           DECIMAL(15,2)    NULL,
    customer_days_overdue              INTEGER          NULL,
    customer_currency                  INTEGER          NULL REFERENCES lkp_currency(currency_id),

    -- Dynamic fields (<=15, validated against workflow_field_definitions)
    customer_custom_fields             JSONB        NOT NULL DEFAULT '{}',

    -- Lineage (lead -> prospect -> customer conversion) + approval
    customer_parent_id                 INTEGER          NULL REFERENCES customer(customer_id),
    customer_is_approved               BOOLEAN      NOT NULL DEFAULT FALSE,
    customer_approval_status           VARCHAR(10)  NOT NULL DEFAULT 'none', -- none | pending | approved
    customer_approved_by               INTEGER          NULL REFERENCES employee(employee_id),
    customer_approved_at               TIMESTAMP        NULL,

    -- Audit / soft-delete / optimistic concurrency
    customer_created_at                TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    customer_created_by                INTEGER          NULL REFERENCES employee(employee_id),
    customer_updated_at                TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    customer_deleted_at                TIMESTAMP        NULL,
    customer_deleted_by                INTEGER          NULL REFERENCES employee(employee_id),
    customer_record_version            INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_customer_uuid    UNIQUE (customer_uuid),
    CONSTRAINT uq_customer_doc_num UNIQUE (customer_doc_num),
    CONSTRAINT chk_customer_approval_status CHECK (customer_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_customer_soft_delete CHECK (
        (customer_deleted_at IS NULL AND customer_deleted_by IS NULL) OR
        (customer_deleted_at IS NOT NULL AND customer_deleted_by IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_customer_type     ON customer (record_type) WHERE customer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_status   ON customer (customer_crm_status) WHERE customer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_owner    ON customer (customer_crm_owner_user_id) WHERE customer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_parent   ON customer (customer_parent_id) WHERE customer_parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_customer_custom_fields ON customer USING GIN (customer_custom_fields);
