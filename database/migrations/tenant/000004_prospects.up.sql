-- =====================================================================
-- Tenant-template schema — Phase 4: dedicated Prospects table.
-- Provides a first-class CRM prospects entity with typed, indexed columns
-- instead of storing everything in the generic workflow_records JSONB blob.
--
-- All VARCHAR/TEXT columns are NOT NULL DEFAULT '' so the Go layer can
-- scan into plain strings without null-pointer handling.
-- Only NUMERIC columns (optional monetary/numeric fields) allow NULL.
-- =====================================================================

CREATE TABLE IF NOT EXISTS prospects (
    id                       UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id            UUID         REFERENCES users(id) ON DELETE SET NULL,

    -- Primary Information
    custom_form              VARCHAR(128) NOT NULL DEFAULT '',
    status                   VARCHAR(128) NOT NULL DEFAULT 'PROSPECT-In Discussion',
    comments                 TEXT         NOT NULL DEFAULT '',
    customer_id              VARCHAR(64)  NOT NULL DEFAULT '',
    customer_id_auto         BOOLEAN      NOT NULL DEFAULT TRUE,
    parent_company           VARCHAR(255) NOT NULL DEFAULT '',
    sfdc_customer_status     VARCHAR(64)  NOT NULL DEFAULT '',
    company_name             VARCHAR(255) NOT NULL,
    zuora_invoice_name       VARCHAR(255) NOT NULL DEFAULT '',
    account_status           VARCHAR(64)  NOT NULL DEFAULT '',
    customer_type            VARCHAR(64)  NOT NULL DEFAULT 'Customer',
    ar_status                VARCHAR(64)  NOT NULL DEFAULT '',
    billing_account_name     VARCHAR(255) NOT NULL DEFAULT '',

    -- Email | Phone | Address
    email                    VARCHAR(255) NOT NULL DEFAULT '',
    phone                    VARCHAR(64)  NOT NULL DEFAULT '',
    address                  TEXT         NOT NULL DEFAULT '',
    multiple_email_invoices  TEXT         NOT NULL DEFAULT '',
    alt_phone                VARCHAR(64)  NOT NULL DEFAULT '',

    -- Classification
    subsidiary               VARCHAR(128) NOT NULL DEFAULT '',
    talkdesk_region          VARCHAR(128) NOT NULL DEFAULT '',
    talkdesk_id_platform     VARCHAR(128) NOT NULL DEFAULT '',
    web_address              VARCHAR(512) NOT NULL DEFAULT '',
    crm_account_owner        VARCHAR(255) NOT NULL DEFAULT '',
    ar_analyst               VARCHAR(255) NOT NULL DEFAULT '',
    crm_csm                  VARCHAR(255) NOT NULL DEFAULT '',
    crm_csm_team             VARCHAR(255) NOT NULL DEFAULT '',
    crm_growth_manager       VARCHAR(255) NOT NULL DEFAULT '',
    white_glove              BOOLEAN      NOT NULL DEFAULT FALSE,
    display_product_code     BOOLEAN      NOT NULL DEFAULT FALSE,

    -- Sales
    territory                VARCHAR(64)  NOT NULL DEFAULT '',
    estimated_budget         NUMERIC(15,2),
    budget_approved          BOOLEAN      NOT NULL DEFAULT FALSE,
    sales_readiness          VARCHAR(64)  NOT NULL DEFAULT '',
    buying_reason            VARCHAR(64)  NOT NULL DEFAULT '',
    buying_time_frame        VARCHAR(64)  NOT NULL DEFAULT '',

    -- Financial
    credit_limit             NUMERIC(15,2),
    payment_terms            VARCHAR(64)  NOT NULL DEFAULT '',
    currency                 VARCHAR(16)  NOT NULL DEFAULT '',
    tax_id                   VARCHAR(128) NOT NULL DEFAULT '',

    -- Subsidiaries
    primary_subsidiary       VARCHAR(128) NOT NULL DEFAULT '',
    consolidated_balance     NUMERIC(15,2),

    -- Address tab
    default_billing_address  TEXT         NOT NULL DEFAULT '',
    default_shipping_address TEXT         NOT NULL DEFAULT '',

    -- Relationships
    sales_rep                VARCHAR(255) NOT NULL DEFAULT '',
    partner                  VARCHAR(255) NOT NULL DEFAULT '',
    primary_contact          VARCHAR(255) NOT NULL DEFAULT '',
    contact_role             VARCHAR(128) NOT NULL DEFAULT '',

    -- Communication
    preferred_channel        VARCHAR(64)  NOT NULL DEFAULT '',
    email_preference         VARCHAR(255) NOT NULL DEFAULT '',
    unsubscribe_all          BOOLEAN      NOT NULL DEFAULT FALSE,

    -- ZAB Subscriptions
    zab_account_id           VARCHAR(128) NOT NULL DEFAULT '',
    subscription_plan        VARCHAR(255) NOT NULL DEFAULT '',
    billing_cycle            VARCHAR(32)  NOT NULL DEFAULT '',

    -- Zuora Sync Details
    zuora_account_id         VARCHAR(128) NOT NULL DEFAULT '',
    sync_status              VARCHAR(32)  NOT NULL DEFAULT '',
    last_synced              VARCHAR(64)  NOT NULL DEFAULT '',

    -- Zuora Account
    zuora_account_number     VARCHAR(128) NOT NULL DEFAULT '',
    zuora_balance            NUMERIC(15,2),
    zuora_auto_pay           BOOLEAN      NOT NULL DEFAULT FALSE,

    -- Stripe
    stripe_customer_id       VARCHAR(128) NOT NULL DEFAULT '',
    stripe_payment_method    VARCHAR(128) NOT NULL DEFAULT '',
    stripe_currency          VARCHAR(16)  NOT NULL DEFAULT '',

    -- CCH® SureTax®
    suretax_customer_number  VARCHAR(128) NOT NULL DEFAULT '',
    tax_exempt               BOOLEAN      NOT NULL DEFAULT FALSE,
    exemption_certificate    VARCHAR(255) NOT NULL DEFAULT '',

    -- E-Document
    edoc_enabled             BOOLEAN      NOT NULL DEFAULT FALSE,
    edoc_format              VARCHAR(16)  NOT NULL DEFAULT '',
    edoc_email               VARCHAR(255) NOT NULL DEFAULT '',

    -- Custom fields
    custom_field_1           TEXT         NOT NULL DEFAULT '',
    custom_field_2           TEXT         NOT NULL DEFAULT '',
    custom_notes             TEXT         NOT NULL DEFAULT '',

    -- Preferences
    language                 VARCHAR(64)  NOT NULL DEFAULT '',
    timezone                 VARCHAR(64)  NOT NULL DEFAULT '',
    date_format              VARCHAR(32)  NOT NULL DEFAULT '',
    receive_newsletter       BOOLEAN      NOT NULL DEFAULT FALSE,

    created_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_prospects_company ON prospects(company_name);
CREATE INDEX IF NOT EXISTS idx_prospects_email   ON prospects(email);
CREATE INDEX IF NOT EXISTS idx_prospects_status  ON prospects(status);
CREATE INDEX IF NOT EXISTS idx_prospects_owner   ON prospects(owner_user_id);
