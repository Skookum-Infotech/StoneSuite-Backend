-- =====================================================================
-- Tenant-template schema — Phase 5: dedicated Leads table.
-- Mirrors the Lead entity from the CRM module with typed columns.
-- =====================================================================

CREATE TABLE IF NOT EXISTS leads (
    id                       UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    lead_id                  VARCHAR(64)  NOT NULL DEFAULT '',
    owner_user_id            UUID         REFERENCES users(id) ON DELETE SET NULL,

    custom_form              VARCHAR(128) NOT NULL DEFAULT 'Standard Lead Form',
    lead_status              VARCHAR(64)  NOT NULL DEFAULT 'LEAD-Unqualified',
    default_order_priority   VARCHAR(64)  NOT NULL DEFAULT '',
    type                     VARCHAR(32)  NOT NULL DEFAULT 'Company',

    -- Name
    company_name             VARCHAR(255) NOT NULL DEFAULT '',
    first_name               VARCHAR(128) NOT NULL DEFAULT '',
    last_name                VARCHAR(128) NOT NULL DEFAULT '',

    -- Email | Phone | Address
    email                    VARCHAR(255) NOT NULL DEFAULT '',
    phone                    VARCHAR(64)  NOT NULL DEFAULT '',
    fax                      VARCHAR(64)  NOT NULL DEFAULT '',
    address                  TEXT         NOT NULL DEFAULT '',

    -- Assignment
    sales_rep                VARCHAR(255) NOT NULL DEFAULT '',
    territory                VARCHAR(128) NOT NULL DEFAULT '',
    partner                  VARCHAR(255) NOT NULL DEFAULT '',

    -- Classification
    primary_subsidiary       VARCHAR(128) NOT NULL DEFAULT '',
    email_for_payment_notification VARCHAR(255) NOT NULL DEFAULT '',
    white_glove              BOOLEAN      NOT NULL DEFAULT FALSE,
    display_product_code     BOOLEAN      NOT NULL DEFAULT FALSE,
    blackline_ar_cash_app    BOOLEAN      NOT NULL DEFAULT FALSE,
    sfdc_account_id          VARCHAR(128) NOT NULL DEFAULT '',
    prev_external_id         VARCHAR(128) NOT NULL DEFAULT '',
    sfdc_customer_status     VARCHAR(64)  NOT NULL DEFAULT '',
    crm_account_owner        VARCHAR(255) NOT NULL DEFAULT '',
    customer_legal_name      VARCHAR(255) NOT NULL DEFAULT '',
    customer_type            VARCHAR(64)  NOT NULL DEFAULT 'Customer',
    crm_csm_team             VARCHAR(128) NOT NULL DEFAULT '',
    sfdc_external_id         VARCHAR(128) NOT NULL DEFAULT '',
    additional_emails        TEXT         NOT NULL DEFAULT '',
    crm_csm                  VARCHAR(255) NOT NULL DEFAULT '',
    talkdesk_region          VARCHAR(128) NOT NULL DEFAULT '',
    crm_growth_manager       VARCHAR(255) NOT NULL DEFAULT '',
    talkdesk_id_platform     VARCHAR(128) NOT NULL DEFAULT '',
    zuora_invoice_name       VARCHAR(255) NOT NULL DEFAULT '',

    -- Qualification
    estimated_budget         VARCHAR(64)  NOT NULL DEFAULT '',
    budget_approved          BOOLEAN      NOT NULL DEFAULT FALSE,
    sales_readiness          VARCHAR(64)  NOT NULL DEFAULT '',
    buying_reason            VARCHAR(64)  NOT NULL DEFAULT '',
    buying_time_frame        VARCHAR(64)  NOT NULL DEFAULT '',

    created_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_leads_company ON leads(company_name);
CREATE INDEX IF NOT EXISTS idx_leads_email   ON leads(email);
CREATE INDEX IF NOT EXISTS idx_leads_status  ON leads(lead_status);
CREATE INDEX IF NOT EXISTS idx_leads_owner   ON leads(owner_user_id);
