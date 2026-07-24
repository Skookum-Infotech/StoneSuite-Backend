-- =====================================================================
-- StoneSuite Tenant Schema -- single canonical file.
--
-- Applied to EACH tenant's isolated database at provisioning time and
-- on every startup for existing tenants (idempotent via CREATE IF NOT EXISTS,
-- INSERT ON CONFLICT DO NOTHING, ADD COLUMN IF NOT EXISTS).
--
-- To change the tenant schema, edit this file directly.
-- History lives in git. No numbered migration files exist any more.
-- =====================================================================


-- -- 000001_tenant_base --------------------------------------------------
-- =====================================================================
-- Tenant-template schema (applied to EACH tenant's isolated database).
-- Phase 0 baseline: tenant-local user profiles. Roles/RBAC (Phase 2)
-- and the workflow engine (Phase 3) are added as later tenant migrations.
--
-- NOTE: identity_id references a row in the CONTROL-PLANE database, which
-- is a different database. Cross-database foreign keys are impossible in
-- Postgres, so identity_id is stored as a plain UUID with no FK constraint.
-- =====================================================================

CREATE TABLE IF NOT EXISTS users (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id  UUID NOT NULL,              -- control-plane identities.id (no cross-DB FK)
    email        VARCHAR(255) NOT NULL,      -- denormalized for convenience/display
    full_name    VARCHAR(255) NOT NULL DEFAULT '',
    status       VARCHAR(32)  NOT NULL DEFAULT 'active', -- active | invited | disabled
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_identity ON users(identity_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email    ON users(LOWER(email));


-- -- 000002_tenant_rbac --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 2: dynamic RBAC.
-- Applied to EACH tenant's isolated database after the base schema.
--
-- Model: roles are bundles of {resource, action, scope} permissions.
-- The permission CATALOG (which resources/actions exist) lives in Go;
-- these tables store which roles grant what, and who has which roles.
--
-- The seeded `super_admin` system role is granted a single wildcard
-- permission ('*','*','all') which the Go enforcer treats as match-all,
-- so it does not need a row per catalog entry.
-- =====================================================================

CREATE TABLE IF NOT EXISTS roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         VARCHAR(64)  NOT NULL,                 -- stable machine key, e.g. super_admin
    name        VARCHAR(128) NOT NULL,                 -- human label
    description TEXT         NOT NULL DEFAULT '',
    is_system   BOOLEAN      NOT NULL DEFAULT FALSE,   -- system roles cannot be deleted/renamed-key
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_roles_key ON roles(LOWER(key));

CREATE TABLE IF NOT EXISTS role_permissions (
    id        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id   UUID        NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    resource  VARCHAR(64) NOT NULL,                    -- catalog resource, or '*' (wildcard)
    action    VARCHAR(32) NOT NULL,                    -- catalog action, or '*' (wildcard)
    scope     VARCHAR(16) NOT NULL DEFAULT 'all',      -- all | own ('team' retired)
    -- 'team' is retained in the CHECK only so pre-existing rows stay valid; the
    -- scope was retired and no code path grants or honours it (it fails closed).
    CONSTRAINT role_permissions_scope_chk CHECK (scope IN ('all', 'team', 'own')),
    CONSTRAINT role_permissions_unique UNIQUE (role_id, resource, action)
);
CREATE INDEX IF NOT EXISTS idx_role_permissions_role ON role_permissions(role_id);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     UUID        NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX IF NOT EXISTS idx_user_roles_user ON user_roles(user_id);

-- VESTIGIAL: the 'team' permission scope was retired and no code reads these
-- tables. They are kept because dropping them would be a destructive migration.
-- Do not build on them without reinstating the team scope end to end.
CREATE TABLE IF NOT EXISTS teams (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name       VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS team_members (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_id);


-- -- 000003_tenant_workflow --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 3: dynamic workflow engine.
-- Applied to EACH tenant's isolated database after RBAC.
--
-- Workflows are state machines defined as DATA (these tables), edited by a
-- super admin in the UI. Lead/Prospect/Customer ship as seeded default
-- workflows (rows), not hardcoded tables. Each workflow has built-in
-- (core_fields) plus up to 15 admin-defined custom keys (custom_fields),
-- governed by workflow_field_definitions and validated in Go.
-- =====================================================================

CREATE TABLE IF NOT EXISTS workflows (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    key         VARCHAR(64)  NOT NULL,                 -- lead | prospect | customer | ...
    name        VARCHAR(128) NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    enabled     BOOLEAN      NOT NULL DEFAULT TRUE,    -- super admin can disable
    is_default  BOOLEAN      NOT NULL DEFAULT FALSE,   -- seeded default workflow
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_key ON workflows(LOWER(key));

CREATE TABLE IF NOT EXISTS workflow_states (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id UUID         NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    key         VARCHAR(64)  NOT NULL,
    name        VARCHAR(128) NOT NULL,
    is_initial  BOOLEAN      NOT NULL DEFAULT FALSE,
    is_terminal BOOLEAN      NOT NULL DEFAULT FALSE,
    sort_order  INT          NOT NULL DEFAULT 0,
    color       VARCHAR(16)  NOT NULL DEFAULT '',
    CONSTRAINT workflow_states_unique UNIQUE (workflow_id, key)
);
CREATE INDEX IF NOT EXISTS idx_workflow_states_workflow ON workflow_states(workflow_id);

CREATE TABLE IF NOT EXISTS workflow_transitions (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id         UUID         NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    from_state_id       UUID         NOT NULL REFERENCES workflow_states(id) ON DELETE CASCADE,
    to_state_id         UUID         NOT NULL REFERENCES workflow_states(id) ON DELETE CASCADE,
    name                VARCHAR(128) NOT NULL,
    required_permission VARCHAR(128) NOT NULL DEFAULT '', -- "resource:action" (optional refinement)
    guard               JSONB        NOT NULL DEFAULT '{}'::jsonb, -- e.g. {"requiredFields":["email"]}
    sort_order          INT          NOT NULL DEFAULT 0,
    CONSTRAINT workflow_transitions_unique UNIQUE (workflow_id, from_state_id, to_state_id)
);
CREATE INDEX IF NOT EXISTS idx_workflow_transitions_workflow ON workflow_transitions(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_transitions_from ON workflow_transitions(from_state_id);

-- Actions fired on transition. Execution is the Phase 4 concern; the schema is
-- defined now so transitions can carry their action config.
CREATE TABLE IF NOT EXISTS workflow_transition_actions (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    transition_id UUID        NOT NULL REFERENCES workflow_transitions(id) ON DELETE CASCADE,
    type          VARCHAR(32) NOT NULL, -- send_email|assign_owner|set_field|webhook|create_record
    config        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    sort_order    INT         NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_wf_transition_actions_transition ON workflow_transition_actions(transition_id);

CREATE TABLE IF NOT EXISTS workflow_field_definitions (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id UUID         NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    key         VARCHAR(64)  NOT NULL,
    label       VARCHAR(128) NOT NULL,
    data_type   VARCHAR(16)  NOT NULL, -- string|number|date|bool|enum|email
    required    BOOLEAN      NOT NULL DEFAULT FALSE,
    options     JSONB        NOT NULL DEFAULT '[]'::jsonb, -- enum options
    validation  JSONB        NOT NULL DEFAULT '{}'::jsonb, -- {regex, min, max}
    sort_order  INT          NOT NULL DEFAULT 0,
    CONSTRAINT wf_field_type_chk CHECK (data_type IN ('string','number','date','bool','enum','email')),
    CONSTRAINT wf_field_unique UNIQUE (workflow_id, key)
);
CREATE INDEX IF NOT EXISTS idx_wf_field_defs_workflow ON workflow_field_definitions(workflow_id);

CREATE TABLE IF NOT EXISTS workflow_records (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id      UUID        NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    current_state_id UUID        REFERENCES workflow_states(id) ON DELETE SET NULL,
    owner_user_id    UUID        REFERENCES users(id) ON DELETE SET NULL,
    team_id          UUID        REFERENCES teams(id) ON DELETE SET NULL,
    core_fields      JSONB       NOT NULL DEFAULT '{}'::jsonb, -- workflow built-ins
    custom_fields    JSONB       NOT NULL DEFAULT '{}'::jsonb, -- the <=15 dynamic keys
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_workflow_records_workflow ON workflow_records(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_records_state   ON workflow_records(current_state_id);
CREATE INDEX IF NOT EXISTS idx_workflow_records_owner   ON workflow_records(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_workflow_records_team    ON workflow_records(team_id);
-- GIN index keeps custom_fields filtering (custom_fields->>'key') fast.
CREATE INDEX IF NOT EXISTS idx_workflow_records_custom_gin ON workflow_records USING GIN (custom_fields);
-- Composite indexes backing the filter/keyset-pagination engine (query pkg):
-- the default newest-first sort + id tiebreaker for "all" scope, and the same
-- ordering narrowed by owner for "own"/"team" scope. These bound the candidate
-- set so filtered lists stay fast at thousands of records per tenant.
CREATE INDEX IF NOT EXISTS idx_workflow_records_wf_created
    ON workflow_records(workflow_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_workflow_records_wf_owner_created
    ON workflow_records(workflow_id, owner_user_id, created_at DESC, id);

CREATE TABLE IF NOT EXISTS workflow_record_history (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    record_id     UUID        NOT NULL REFERENCES workflow_records(id) ON DELETE CASCADE,
    from_state_id UUID,
    to_state_id   UUID,
    actor_user_id UUID,
    transition_id UUID,
    at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    snapshot      JSONB       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_wf_record_history_record ON workflow_record_history(record_id);


-- -- 000004_prospects --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 4: dedicated Prospects table.
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

    -- CCH(R) SureTax(R)
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


-- -- 000005_leads --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 5: dedicated Leads table.
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


-- -- 000006_crm_custom_fields --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 6: custom_fields JSONB on CRM tables.
-- Allows admins to add up to 15 custom fields (via workflow_field_definitions)
-- to Lead and Prospect records without requiring schema migrations.
-- =====================================================================

ALTER TABLE leads     ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}';
ALTER TABLE prospects ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_leads_custom     ON leads     USING gin(custom_fields);
CREATE INDEX IF NOT EXISTS idx_prospects_custom ON prospects USING gin(custom_fields);


-- -- 000007_crm_unified --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 7: Unified CRM workflow fields.
--
-- Adds pipeline_order to workflows so the Lead->Prospect->Customer
-- dependency chain can be enforced server-side when toggling enabled.
-- Adds parent_record_id to workflow_records to track lineage when a
-- lead is converted to a prospect or a prospect to a customer.
-- =====================================================================

-- pipeline_order: position in the CRM dependency chain.
-- 0 = unordered (non-CRM workflows). 1=Lead, 2=Prospect, 3=Customer.
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS pipeline_order INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_workflows_pipeline_order ON workflows(pipeline_order) WHERE pipeline_order > 0;

-- parent_record_id: the workflow_record this record was converted from.
-- NULL for records that were created directly (no conversion lineage).
ALTER TABLE workflow_records ADD COLUMN IF NOT EXISTS parent_record_id UUID REFERENCES workflow_records(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_workflow_records_parent ON workflow_records(parent_record_id) WHERE parent_record_id IS NOT NULL;


-- -- 000008_record_numbering --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 8: per-workflow record auto-numbering.
--
-- Lets a super admin configure auto-generated record numbers (prefix +
-- zero-padded sequence + suffix) per workflow. One row per workflow,
-- created lazily via upsert when the config is first set -- no seeding
-- required for new or future workflows.
-- =====================================================================

CREATE TABLE IF NOT EXISTS workflow_numbering_configs (
    workflow_id  UUID PRIMARY KEY REFERENCES workflows(id) ON DELETE CASCADE,
    enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    prefix       TEXT NOT NULL DEFAULT '',
    suffix       TEXT NOT NULL DEFAULT '',
    min_digits   INT NOT NULL DEFAULT 1,
    next_number  BIGINT NOT NULL DEFAULT 1,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- record_number: the generated number assigned at creation time, e.g. "LEAD-0001".
-- NULL when numbering is not enabled for the record's workflow.
ALTER TABLE workflow_records ADD COLUMN IF NOT EXISTS record_number TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_records_record_number
    ON workflow_records(workflow_id, record_number) WHERE record_number IS NOT NULL;


-- -- 000012_drop_legacy_crm --------------------------------------------------
-- SAFETY: This migration permanently drops the `leads` and `prospects` tables (CASCADE).
-- Before this migration was embedded, the following was verified:
--   1. No application code queries leads/prospects (replaced by workflow_records + crm_record).
--   2. All active tenants had their data migrated to workflow_records before this ran.
--   3. A Neon branch snapshot was taken as a recovery point.
-- Recovery: Neon point-in-time restore or branch restore only. No down-migration exists.

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


-- -- 000013_employee --------------------------------------------------
-- =====================================================================
-- Tenant migration 012: employee table (v2 relational design).
--
-- The relational lkp_* and crm_record tables reference employee(employee_id)
-- for audit columns and ownership. employee_user_id links back to the
-- existing UUID users table (and through it to the control-plane identity).
-- A system employee with employee_id = 1 is seeded for system/seed rows.
-- =====================================================================

CREATE TABLE IF NOT EXISTS employee (
    employee_id             SERIAL       PRIMARY KEY,
    employee_user_id        UUID             NULL REFERENCES users(id) ON DELETE SET NULL,
    employee_first_name     VARCHAR(100) NOT NULL DEFAULT '',
    employee_last_name      VARCHAR(100) NOT NULL DEFAULT '',
    employee_email          VARCHAR(255) NOT NULL,
    -- Audit
    employee_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    employee_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    employee_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    employee_created_by     INTEGER          NULL,
    employee_deleted_at     TIMESTAMP        NULL,
    employee_deleted_by     INTEGER          NULL,
    employee_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_employee_email UNIQUE (employee_email),
    CONSTRAINT chk_employee_soft_delete CHECK (
        (employee_deleted_at IS NULL AND employee_deleted_by IS NULL) OR
        (employee_deleted_at IS NOT NULL AND employee_deleted_by IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_employee_user ON employee(employee_user_id)
    WHERE employee_user_id IS NOT NULL;

-- Seed the system employee at id = 1 (used as created_by for lkp seed rows).
INSERT INTO employee (employee_id, employee_first_name, employee_last_name, employee_email, employee_is_system)
VALUES (1, 'System', 'User', 'system@stonesuite.local', TRUE)
ON CONFLICT (employee_id) DO NOTHING;

-- Keep the SERIAL sequence ahead of the explicit id we just inserted.
SELECT setval(
    pg_get_serial_sequence('employee', 'employee_id'),
    GREATEST((SELECT MAX(employee_id) FROM employee), 1)
);


-- -- 000014_lkp_tables --------------------------------------------------
-- =====================================================================
-- Tenant migration 013: ERP lookup tables (v2 relational design).
-- Source: StoneSuite_Lookup_DDL_DML_v1.sql (Elevation Stone). Converted to
-- idempotent form: CREATE TABLE IF NOT EXISTS with inline constraints, and
-- INSERT ... ON CONFLICT DO NOTHING so the runner can replay safely.
-- All audit columns reference employee(employee_id); seed rows use id 1.
-- =====================================================================

-- 1. lkp_currency -----------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_currency (
    currency_id             SERIAL       PRIMARY KEY,
    currency_name           VARCHAR(50)  NOT NULL,
    currency_code           VARCHAR(5)   NOT NULL,
    currency_symbol         VARCHAR(5)   NOT NULL,
    currency_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    currency_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    currency_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    currency_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    currency_deleted_at     TIMESTAMP        NULL,
    currency_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    currency_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_currency_code UNIQUE (currency_code)
);

INSERT INTO lkp_currency (currency_name, currency_code, currency_symbol, currency_is_active, currency_is_system, currency_created_by) VALUES
    ('US Dollar',          'USD',  '$',  TRUE, TRUE, 1),
    ('Canadian Dollar',    'CAD',  'C$', TRUE, TRUE, 1),
    ('Mexican Peso',       'MXN',  '$',  TRUE, TRUE, 1),
    ('Indian Rupee',       'INR',  '₹',  TRUE, TRUE, 1),
    ('Euro',               'EUR',  '€',  TRUE, TRUE, 1),
    ('British Pound',      'GBP',  '£',  TRUE, TRUE, 1),
    ('Australian Dollar',  'AUD',  'A$', TRUE, TRUE, 1),
    ('UAE Dirham',         'AED',  'د.إ',TRUE, TRUE, 1)
ON CONFLICT (currency_code) DO NOTHING;

-- 2. lkp_country ------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_country (
    country_id                  SERIAL       PRIMARY KEY,
    country_name                VARCHAR(50)  NOT NULL,
    country_code2               VARCHAR(5)   NOT NULL,
    country_code3               VARCHAR(5)   NOT NULL,
    country_locale              VARCHAR(10)      NULL,
    country_phone_code          VARCHAR(10)  NOT NULL,
    country_default_currency_id INTEGER      NOT NULL REFERENCES lkp_currency(currency_id),
    country_is_active           BOOLEAN      NOT NULL DEFAULT TRUE,
    country_is_system           BOOLEAN      NOT NULL DEFAULT FALSE,
    country_created_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    country_created_by          INTEGER      NOT NULL REFERENCES employee(employee_id),
    country_deleted_at          TIMESTAMP        NULL,
    country_deleted_by          INTEGER          NULL REFERENCES employee(employee_id),
    country_record_version      INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_country_code2 UNIQUE (country_code2),
    CONSTRAINT uq_country_code3 UNIQUE (country_code3)
);

INSERT INTO lkp_country (country_name, country_code2, country_code3, country_locale, country_phone_code, country_default_currency_id, country_is_active, country_is_system, country_created_by) VALUES
    ('United States of America', 'US', 'USA', 'en-US', '+1',   1, TRUE, TRUE, 1),
    ('Canada',                   'CA', 'CAN', 'en-CA', '+1',   2, TRUE, TRUE, 1),
    ('Mexico',                   'MX', 'MEX', 'es-MX', '+52',  3, TRUE, TRUE, 1),
    ('India',                    'IN', 'IND', 'en-IN', '+91',  4, TRUE, TRUE, 1)
ON CONFLICT (country_code2) DO NOTHING;

-- 3. lkp_state --------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_state (
    state_id            SERIAL       PRIMARY KEY,
    state_country_id    INTEGER      NOT NULL REFERENCES lkp_country(country_id),
    state_name          VARCHAR(50)  NOT NULL,
    state_code          VARCHAR(5)   NOT NULL,
    state_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    state_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    state_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    state_created_by    INTEGER      NOT NULL REFERENCES employee(employee_id),
    state_deleted_at    TIMESTAMP        NULL,
    state_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    state_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_state_code_country UNIQUE (state_country_id, state_code)
);

INSERT INTO lkp_state (state_country_id, state_name, state_code, state_is_active, state_is_system, state_created_by) VALUES
    (1, 'Alabama', 'AL', TRUE, TRUE, 1), (1, 'Alaska', 'AK', TRUE, TRUE, 1), (1, 'Arizona', 'AZ', TRUE, TRUE, 1),
    (1, 'Arkansas', 'AR', TRUE, TRUE, 1), (1, 'California', 'CA', TRUE, TRUE, 1), (1, 'Colorado', 'CO', TRUE, TRUE, 1),
    (1, 'Connecticut', 'CT', TRUE, TRUE, 1), (1, 'Delaware', 'DE', TRUE, TRUE, 1), (1, 'Florida', 'FL', TRUE, TRUE, 1),
    (1, 'Georgia', 'GA', TRUE, TRUE, 1), (1, 'Hawaii', 'HI', TRUE, TRUE, 1), (1, 'Idaho', 'ID', TRUE, TRUE, 1),
    (1, 'Illinois', 'IL', TRUE, TRUE, 1), (1, 'Indiana', 'IN', TRUE, TRUE, 1), (1, 'Iowa', 'IA', TRUE, TRUE, 1),
    (1, 'Kansas', 'KS', TRUE, TRUE, 1), (1, 'Kentucky', 'KY', TRUE, TRUE, 1), (1, 'Louisiana', 'LA', TRUE, TRUE, 1),
    (1, 'Maine', 'ME', TRUE, TRUE, 1), (1, 'Maryland', 'MD', TRUE, TRUE, 1), (1, 'Massachusetts', 'MA', TRUE, TRUE, 1),
    (1, 'Michigan', 'MI', TRUE, TRUE, 1), (1, 'Minnesota', 'MN', TRUE, TRUE, 1), (1, 'Mississippi', 'MS', TRUE, TRUE, 1),
    (1, 'Missouri', 'MO', TRUE, TRUE, 1), (1, 'Montana', 'MT', TRUE, TRUE, 1), (1, 'Nebraska', 'NE', TRUE, TRUE, 1),
    (1, 'Nevada', 'NV', TRUE, TRUE, 1), (1, 'New Hampshire', 'NH', TRUE, TRUE, 1), (1, 'New Jersey', 'NJ', TRUE, TRUE, 1),
    (1, 'New Mexico', 'NM', TRUE, TRUE, 1), (1, 'New York', 'NY', TRUE, TRUE, 1), (1, 'North Carolina', 'NC', TRUE, TRUE, 1),
    (1, 'North Dakota', 'ND', TRUE, TRUE, 1), (1, 'Ohio', 'OH', TRUE, TRUE, 1), (1, 'Oklahoma', 'OK', TRUE, TRUE, 1),
    (1, 'Oregon', 'OR', TRUE, TRUE, 1), (1, 'Pennsylvania', 'PA', TRUE, TRUE, 1), (1, 'Rhode Island', 'RI', TRUE, TRUE, 1),
    (1, 'South Carolina', 'SC', TRUE, TRUE, 1), (1, 'South Dakota', 'SD', TRUE, TRUE, 1), (1, 'Tennessee', 'TN', TRUE, TRUE, 1),
    (1, 'Texas', 'TX', TRUE, TRUE, 1), (1, 'Utah', 'UT', TRUE, TRUE, 1), (1, 'Vermont', 'VT', TRUE, TRUE, 1),
    (1, 'Virginia', 'VA', TRUE, TRUE, 1), (1, 'Washington', 'WA', TRUE, TRUE, 1), (1, 'West Virginia', 'WV', TRUE, TRUE, 1),
    (1, 'Wisconsin', 'WI', TRUE, TRUE, 1), (1, 'Wyoming', 'WY', TRUE, TRUE, 1), (1, 'District of Columbia', 'DC', TRUE, TRUE, 1),
    (2, 'Alberta', 'AB', TRUE, TRUE, 1), (2, 'British Columbia', 'BC', TRUE, TRUE, 1), (2, 'Manitoba', 'MB', TRUE, TRUE, 1),
    (2, 'New Brunswick', 'NB', TRUE, TRUE, 1), (2, 'Newfoundland and Labrador', 'NL', TRUE, TRUE, 1), (2, 'Nova Scotia', 'NS', TRUE, TRUE, 1),
    (2, 'Ontario', 'ON', TRUE, TRUE, 1), (2, 'Prince Edward Island', 'PE', TRUE, TRUE, 1), (2, 'Quebec', 'QC', TRUE, TRUE, 1),
    (2, 'Saskatchewan', 'SK', TRUE, TRUE, 1), (2, 'Northwest Territories', 'NT', TRUE, TRUE, 1), (2, 'Nunavut', 'NU', TRUE, TRUE, 1),
    (2, 'Yukon', 'YT', TRUE, TRUE, 1),
    (3, 'Aguascalientes', 'AG', TRUE, TRUE, 1), (3, 'Baja California', 'BC', TRUE, TRUE, 1), (3, 'Baja California Sur', 'BS', TRUE, TRUE, 1),
    (3, 'Campeche', 'CM', TRUE, TRUE, 1), (3, 'Chiapas', 'CS', TRUE, TRUE, 1), (3, 'Chihuahua', 'CH', TRUE, TRUE, 1),
    (3, 'Ciudad de Mexico', 'CX', TRUE, TRUE, 1), (3, 'Coahuila', 'CO', TRUE, TRUE, 1), (3, 'Colima', 'CL', TRUE, TRUE, 1),
    (3, 'Durango', 'DG', TRUE, TRUE, 1), (3, 'Guanajuato', 'GT', TRUE, TRUE, 1), (3, 'Guerrero', 'GR', TRUE, TRUE, 1),
    (3, 'Hidalgo', 'HG', TRUE, TRUE, 1), (3, 'Jalisco', 'JA', TRUE, TRUE, 1), (3, 'Mexico State', 'EM', TRUE, TRUE, 1),
    (3, 'Michoacan', 'MI', TRUE, TRUE, 1), (3, 'Morelos', 'MO', TRUE, TRUE, 1), (3, 'Nayarit', 'NA', TRUE, TRUE, 1),
    (3, 'Nuevo Leon', 'NL', TRUE, TRUE, 1), (3, 'Oaxaca', 'OA', TRUE, TRUE, 1), (3, 'Puebla', 'PU', TRUE, TRUE, 1),
    (3, 'Queretaro', 'QT', TRUE, TRUE, 1), (3, 'Quintana Roo', 'QR', TRUE, TRUE, 1), (3, 'San Luis Potosi', 'SL', TRUE, TRUE, 1),
    (3, 'Sinaloa', 'SI', TRUE, TRUE, 1), (3, 'Sonora', 'SO', TRUE, TRUE, 1), (3, 'Tabasco', 'TB', TRUE, TRUE, 1),
    (3, 'Tamaulipas', 'TM', TRUE, TRUE, 1), (3, 'Tlaxcala', 'TL', TRUE, TRUE, 1), (3, 'Veracruz', 'VE', TRUE, TRUE, 1),
    (3, 'Yucatan', 'YU', TRUE, TRUE, 1), (3, 'Zacatecas', 'ZA', TRUE, TRUE, 1),
    (4, 'Andhra Pradesh', 'AP', TRUE, TRUE, 1), (4, 'Arunachal Pradesh', 'AR', TRUE, TRUE, 1), (4, 'Assam', 'AS', TRUE, TRUE, 1),
    (4, 'Bihar', 'BR', TRUE, TRUE, 1), (4, 'Chhattisgarh', 'CG', TRUE, TRUE, 1), (4, 'Goa', 'GA', TRUE, TRUE, 1),
    (4, 'Gujarat', 'GJ', TRUE, TRUE, 1), (4, 'Haryana', 'HR', TRUE, TRUE, 1), (4, 'Himachal Pradesh', 'HP', TRUE, TRUE, 1),
    (4, 'Jharkhand', 'JH', TRUE, TRUE, 1), (4, 'Karnataka', 'KA', TRUE, TRUE, 1), (4, 'Kerala', 'KL', TRUE, TRUE, 1),
    (4, 'Madhya Pradesh', 'MP', TRUE, TRUE, 1), (4, 'Maharashtra', 'MH', TRUE, TRUE, 1), (4, 'Manipur', 'MN', TRUE, TRUE, 1),
    (4, 'Meghalaya', 'ML', TRUE, TRUE, 1), (4, 'Mizoram', 'MZ', TRUE, TRUE, 1), (4, 'Nagaland', 'NL', TRUE, TRUE, 1),
    (4, 'Odisha', 'OD', TRUE, TRUE, 1), (4, 'Punjab', 'PB', TRUE, TRUE, 1), (4, 'Rajasthan', 'RJ', TRUE, TRUE, 1),
    (4, 'Sikkim', 'SK', TRUE, TRUE, 1), (4, 'Tamil Nadu', 'TN', TRUE, TRUE, 1), (4, 'Telangana', 'TG', TRUE, TRUE, 1),
    (4, 'Tripura', 'TR', TRUE, TRUE, 1), (4, 'Uttar Pradesh', 'UP', TRUE, TRUE, 1), (4, 'Uttarakhand', 'UK', TRUE, TRUE, 1),
    (4, 'West Bengal', 'WB', TRUE, TRUE, 1), (4, 'Andaman and Nicobar Islands', 'AN', TRUE, TRUE, 1), (4, 'Chandigarh', 'CH', TRUE, TRUE, 1),
    (4, 'Dadra and Nagar Haveli and Daman and Diu', 'DD', TRUE, TRUE, 1), (4, 'Delhi', 'DL', TRUE, TRUE, 1), (4, 'Jammu and Kashmir', 'JK', TRUE, TRUE, 1),
    (4, 'Ladakh', 'LA', TRUE, TRUE, 1), (4, 'Lakshadweep', 'LD', TRUE, TRUE, 1), (4, 'Puducherry', 'PY', TRUE, TRUE, 1)
ON CONFLICT (state_country_id, state_code) DO NOTHING;

-- 4. lkp_record_type --------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_record_type (
    record_type_id          SERIAL       PRIMARY KEY,
    record_type_code        VARCHAR(10)  NOT NULL,
    record_type_code_full   VARCHAR(50)  NOT NULL,
    record_type_name        VARCHAR(50)  NOT NULL,
    record_type_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    record_type_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    record_type_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    record_type_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    record_type_deleted_at  TIMESTAMP        NULL,
    record_type_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    record_type_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_record_type_code UNIQUE (record_type_code)
);

INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name, record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('LEAD', 'lead',             'Lead',             TRUE, TRUE, 1),
    ('PROS', 'prospect',         'Prospect',         TRUE, TRUE, 1),
    ('CUST', 'customer',         'Customer',         TRUE, TRUE, 1),
    ('ESTM', 'estimate',         'Estimate',         TRUE, TRUE, 1),
    ('QUOT', 'quote',            'Quote',            TRUE, TRUE, 1),
    ('SORD', 'salesorder',       'Sales Order',      TRUE, TRUE, 1),
    ('INVC', 'invoice',          'Invoice',          TRUE, TRUE, 1),
    ('PYMT', 'payment',          'Payment',          TRUE, TRUE, 1),
    ('CRDT', 'creditmemo',       'Credit Memo',      TRUE, TRUE, 1),
    ('RFND', 'customerrefund',   'Customer Refund',  TRUE, TRUE, 1),
    ('VNDR', 'vendor',           'Vendor',           TRUE, TRUE, 1),
    ('REQN', 'requisition',      'Requisition',      TRUE, TRUE, 1),
    ('PORD', 'purchaseorder',    'Purchase Order',   TRUE, TRUE, 1),
    ('IRCT', 'itemreceipt',      'Item Receipt',     TRUE, TRUE, 1),
    ('VBIL', 'vendorbill',       'Vendor Bill',      TRUE, TRUE, 1),
    ('VPAY', 'vendorpayment',    'Vendor Payment',   TRUE, TRUE, 1),
    ('VCRD', 'vendorcredit',     'Vendor Credit',    TRUE, TRUE, 1)
ON CONFLICT (record_type_code) DO NOTHING;

-- 5. lkp_record_status ------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_record_status (
    record_status_id            SERIAL       PRIMARY KEY,
    record_status_code          VARCHAR(10)  NOT NULL,
    record_status_name          VARCHAR(50)  NOT NULL,
    record_status_record_type   INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    record_status_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    record_status_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    record_status_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    record_status_created_by    INTEGER      NOT NULL REFERENCES employee(employee_id),
    record_status_deleted_at    TIMESTAMP        NULL,
    record_status_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    record_status_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_record_status_code_type UNIQUE (record_status_code, record_status_record_type)
);

INSERT INTO lkp_record_status (record_status_code, record_status_name, record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by) VALUES
    ('ACT_', 'Active', 1, TRUE, TRUE, 1), ('INA_', 'Inactive', 1, TRUE, TRUE, 1), ('CANC', 'Cancelled', 1, TRUE, TRUE, 1),
    ('ACT_', 'Active', 2, TRUE, TRUE, 1), ('INA_', 'Inactive', 2, TRUE, TRUE, 1), ('CANC', 'Cancelled', 2, TRUE, TRUE, 1),
    ('ACT_', 'Active', 3, TRUE, TRUE, 1), ('INA_', 'Inactive', 3, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 4, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 4, TRUE, TRUE, 1), ('APPV', 'Approved', 4, TRUE, TRUE, 1),
    ('SENT', 'Sent', 4, TRUE, TRUE, 1), ('CANC', 'Cancelled', 4, TRUE, TRUE, 1), ('RJCT', 'Rejected', 4, TRUE, TRUE, 1), ('EXPR', 'Expired', 4, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 5, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 5, TRUE, TRUE, 1), ('APPV', 'Approved', 5, TRUE, TRUE, 1),
    ('SENT', 'Sent', 5, TRUE, TRUE, 1), ('CANC', 'Cancelled', 5, TRUE, TRUE, 1), ('RJCT', 'Rejected', 5, TRUE, TRUE, 1), ('EXPR', 'Expired', 5, TRUE, TRUE, 1), ('CONV', 'Converted', 5, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 6, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 6, TRUE, TRUE, 1), ('APPV', 'Approved', 6, TRUE, TRUE, 1),
    ('OPEN', 'Open', 6, TRUE, TRUE, 1), ('PART', 'Partially Filled', 6, TRUE, TRUE, 1), ('FILL', 'Filled', 6, TRUE, TRUE, 1), ('CANC', 'Cancelled', 6, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 7, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 7, TRUE, TRUE, 1), ('APPV', 'Approved', 7, TRUE, TRUE, 1),
    ('SENT', 'Sent', 7, TRUE, TRUE, 1), ('PART', 'Partially Paid', 7, TRUE, TRUE, 1), ('PAID', 'Paid', 7, TRUE, TRUE, 1), ('ODUE', 'Overdue', 7, TRUE, TRUE, 1), ('VOID', 'Void', 7, TRUE, TRUE, 1),
    ('PEND', 'Pending', 8, TRUE, TRUE, 1), ('APPV', 'Approved', 8, TRUE, TRUE, 1), ('DEPO', 'Deposited', 8, TRUE, TRUE, 1), ('VOID', 'Void', 8, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 9, TRUE, TRUE, 1), ('APPV', 'Approved', 9, TRUE, TRUE, 1), ('APPL', 'Applied', 9, TRUE, TRUE, 1), ('VOID', 'Void', 9, TRUE, TRUE, 1),
    ('PEND', 'Pending', 10, TRUE, TRUE, 1), ('APPV', 'Approved', 10, TRUE, TRUE, 1), ('SENT', 'Sent', 10, TRUE, TRUE, 1), ('VOID', 'Void', 10, TRUE, TRUE, 1),
    ('ACT_', 'Active', 11, TRUE, TRUE, 1), ('INA_', 'Inactive', 11, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 12, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 12, TRUE, TRUE, 1), ('APPV', 'Approved', 12, TRUE, TRUE, 1), ('CANC', 'Cancelled', 12, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 13, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 13, TRUE, TRUE, 1), ('APPV', 'Approved', 13, TRUE, TRUE, 1),
    ('SENT', 'Sent to Vendor', 13, TRUE, TRUE, 1), ('PART', 'Partially Received', 13, TRUE, TRUE, 1), ('RCVD', 'Received', 13, TRUE, TRUE, 1), ('CLSD', 'Closed', 13, TRUE, TRUE, 1), ('CANC', 'Cancelled', 13, TRUE, TRUE, 1),
    ('PEND', 'Pending', 14, TRUE, TRUE, 1), ('RCVD', 'Received', 14, TRUE, TRUE, 1), ('PART', 'Partial', 14, TRUE, TRUE, 1), ('VOID', 'Void', 14, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 15, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 15, TRUE, TRUE, 1), ('APPV', 'Approved', 15, TRUE, TRUE, 1),
    ('PART', 'Partially Paid', 15, TRUE, TRUE, 1), ('PAID', 'Paid', 15, TRUE, TRUE, 1), ('ODUE', 'Overdue', 15, TRUE, TRUE, 1), ('VOID', 'Void', 15, TRUE, TRUE, 1),
    ('PEND', 'Pending', 16, TRUE, TRUE, 1), ('APPV', 'Approved', 16, TRUE, TRUE, 1), ('SENT', 'Sent', 16, TRUE, TRUE, 1), ('VOID', 'Void', 16, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 17, TRUE, TRUE, 1), ('APPV', 'Approved', 17, TRUE, TRUE, 1), ('APPL', 'Applied', 17, TRUE, TRUE, 1), ('VOID', 'Void', 17, TRUE, TRUE, 1)
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- 6. lkp_crm_status ---------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_crm_status (
    crm_status_id           SERIAL       PRIMARY KEY,
    crm_status_code         VARCHAR(10)  NOT NULL,
    crm_status_name         VARCHAR(50)  NOT NULL,
    crm_status_record_type  INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    crm_status_is_active    BOOLEAN      NOT NULL DEFAULT TRUE,
    crm_status_is_system    BOOLEAN      NOT NULL DEFAULT FALSE,
    crm_status_created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    crm_status_created_by   INTEGER      NOT NULL REFERENCES employee(employee_id),
    crm_status_deleted_at   TIMESTAMP        NULL,
    crm_status_deleted_by   INTEGER          NULL REFERENCES employee(employee_id),
    crm_status_record_version INTEGER    NOT NULL DEFAULT 1,
    CONSTRAINT uq_crm_status_code_type UNIQUE (crm_status_code, crm_status_record_type),
    CONSTRAINT uq_crm_status_name_type UNIQUE (crm_status_name, crm_status_record_type)
);

INSERT INTO lkp_crm_status (crm_status_code, crm_status_name, crm_status_record_type, crm_status_is_active, crm_status_is_system, crm_status_created_by) VALUES
    ('LQUA', 'Lead Qualified',                       1, TRUE, TRUE, 1),
    ('LUNQ', 'Lead Unqualified',                     1, TRUE, TRUE, 1),
    ('PDIS', 'Prospect In Discussion',               2, TRUE, TRUE, 1),
    ('PNEG', 'Prospect In Negotiation',              2, TRUE, TRUE, 1),
    ('PPRP', 'Prospect Proposal',                    2, TRUE, TRUE, 1),
    ('PIDM', 'Prospect Identified Decision Makers',  2, TRUE, TRUE, 1),
    ('PPUR', 'Prospect Purchasing',                  2, TRUE, TRUE, 1),
    ('PCLL', 'Prospect Closed Lost',                 2, TRUE, TRUE, 1),
    ('CCLW', 'Customer Closed Won',                  3, TRUE, TRUE, 1),
    ('CCLL', 'Customer Closed Lost',                 3, TRUE, TRUE, 1),
    ('CREN', 'Customer Renewal',                     3, TRUE, TRUE, 1)
ON CONFLICT (crm_status_code, crm_status_record_type) DO NOTHING;

-- 7. lkp_customer_type ------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_customer_type (
    customer_type_id    SERIAL        PRIMARY KEY,
    customer_type_name  VARCHAR(100)  NOT NULL,
    customer_type_code  VARCHAR(10)   NOT NULL,
    customer_type_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    customer_type_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    customer_type_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    customer_type_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    customer_type_deleted_at  TIMESTAMP   NULL,
    customer_type_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    customer_type_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_customer_type_code UNIQUE (customer_type_code)
);

INSERT INTO lkp_customer_type (customer_type_name, customer_type_code, customer_type_is_active, customer_type_is_system, customer_type_created_by) VALUES
    ('Individual',          'INDV',  TRUE, TRUE, 1),
    ('Retail',              'RETL',  TRUE, TRUE, 1),
    ('Designer',            'DSGN',  TRUE, TRUE, 1),
    ('National Builder',    'NBLD',  TRUE, TRUE, 1),
    ('Custom Builder',      'CBLD',  TRUE, TRUE, 1),
    ('Regional Builder',    'RBLD',  TRUE, TRUE, 1),
    ('Multi-Family Builder','MFBLD', TRUE, TRUE, 1),
    ('Commercial Builder',  'COMBLD',TRUE, TRUE, 1),
    ('General Contractor',  'GCON',  TRUE, TRUE, 1)
ON CONFLICT (customer_type_code) DO NOTHING;

-- 8. lkp_customer_ar_status -------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_customer_ar_status (
    customer_ar_status_id    SERIAL       PRIMARY KEY,
    customer_ar_status_name  VARCHAR(50)  NOT NULL,
    customer_ar_status_code  VARCHAR(10)  NOT NULL,
    customer_ar_status_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    customer_ar_status_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    customer_ar_status_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    customer_ar_status_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    customer_ar_status_deleted_at  TIMESTAMP   NULL,
    customer_ar_status_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    customer_ar_status_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_customer_ar_status_code UNIQUE (customer_ar_status_code)
);

INSERT INTO lkp_customer_ar_status (customer_ar_status_name, customer_ar_status_code, customer_ar_status_is_active, customer_ar_status_is_system, customer_ar_status_created_by) VALUES
    ('Current',      'CURR', TRUE, TRUE, 1), ('Due Soon',     'DUSN', TRUE, TRUE, 1), ('Past Due',     'PDUE', TRUE, TRUE, 1),
    ('Delinquent',   'DLNQ', TRUE, TRUE, 1), ('Credit Hold',  'CRHD', TRUE, TRUE, 1), ('Collections',  'COLL', TRUE, TRUE, 1),
    ('Bad Debt',     'BDBT', TRUE, TRUE, 1)
ON CONFLICT (customer_ar_status_code) DO NOTHING;

-- 9. lkp_payment_terms ------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_payment_terms (
    payment_terms_id    SERIAL       PRIMARY KEY,
    payment_terms_name  VARCHAR(50)  NOT NULL,
    payment_terms_code  VARCHAR(10)  NOT NULL,
    payment_terms_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    payment_terms_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    payment_terms_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_terms_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    payment_terms_deleted_at  TIMESTAMP   NULL,
    payment_terms_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    payment_terms_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_payment_terms_code UNIQUE (payment_terms_code)
);

INSERT INTO lkp_payment_terms (payment_terms_name, payment_terms_code, payment_terms_is_active, payment_terms_is_system, payment_terms_created_by) VALUES
    ('Net 10',            'N10_', TRUE, TRUE, 1), ('Net 15',            'N15_', TRUE, TRUE, 1), ('Net 30',            'N30_', TRUE, TRUE, 1),
    ('Net 45',            'N45_', TRUE, TRUE, 1), ('Net 60',            'N60_', TRUE, TRUE, 1), ('Net 90',            'N90_', TRUE, TRUE, 1),
    ('Net 120',           'N120', TRUE, TRUE, 1), ('Cash on Receipt',   'COR_', TRUE, TRUE, 1), ('Cash on Delivery',  'COD_', TRUE, TRUE, 1),
    ('Due on Receipt',    'DOR_', TRUE, TRUE, 1), ('50% Deposit Net 30','D50N', TRUE, TRUE, 1)
ON CONFLICT (payment_terms_code) DO NOTHING;

-- 10. lkp_crm_lead_source ---------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_crm_lead_source (
    lead_source_id    SERIAL       PRIMARY KEY,
    lead_source_name  VARCHAR(50)  NOT NULL,
    lead_source_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    lead_source_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    lead_source_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lead_source_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    lead_source_deleted_at  TIMESTAMP   NULL,
    lead_source_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    lead_source_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_lead_source_name UNIQUE (lead_source_name)
);

INSERT INTO lkp_crm_lead_source (lead_source_name, lead_source_is_active, lead_source_is_system, lead_source_created_by) VALUES
    ('Web Search', TRUE, TRUE, 1), ('Facebook', TRUE, TRUE, 1), ('Instagram', TRUE, TRUE, 1), ('LinkedIn', TRUE, TRUE, 1),
    ('Trade Show', TRUE, TRUE, 1), ('Referral', TRUE, TRUE, 1), ('Email Campaign', TRUE, TRUE, 1)
ON CONFLICT (lead_source_name) DO NOTHING;

-- 11. lkp_contact_method ----------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_contact_method (
    contact_method_id    SERIAL       PRIMARY KEY,
    contact_method_name  VARCHAR(50)  NOT NULL,
    contact_method_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    contact_method_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    contact_method_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    contact_method_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    contact_method_deleted_at  TIMESTAMP   NULL,
    contact_method_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    contact_method_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_contact_method_name UNIQUE (contact_method_name)
);

INSERT INTO lkp_contact_method (contact_method_name, contact_method_is_active, contact_method_is_system, contact_method_created_by) VALUES
    ('Email', TRUE, TRUE, 1), ('Phone', TRUE, TRUE, 1), ('Text', TRUE, TRUE, 1), ('Postal Mail', TRUE, TRUE, 1)
ON CONFLICT (contact_method_name) DO NOTHING;

-- Indexes -- active-record queries -------------------------------------
CREATE INDEX IF NOT EXISTS idx_currency_active ON lkp_currency (currency_is_active) WHERE currency_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_country_active ON lkp_country (country_is_active) WHERE country_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_state_active ON lkp_state (state_is_active) WHERE state_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_state_country ON lkp_state (state_country_id) WHERE state_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_record_type_active ON lkp_record_type (record_type_is_active) WHERE record_type_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_record_status_type ON lkp_record_status (record_status_record_type) WHERE record_status_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_status_type ON lkp_crm_status (crm_status_record_type) WHERE crm_status_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_type_active ON lkp_customer_type (customer_type_is_active) WHERE customer_type_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_ar_status_active ON lkp_customer_ar_status (customer_ar_status_is_active) WHERE customer_ar_status_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_payment_terms_active ON lkp_payment_terms (payment_terms_is_active) WHERE payment_terms_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_lead_source_active ON lkp_crm_lead_source (lead_source_is_active) WHERE lead_source_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_contact_method_active ON lkp_contact_method (contact_method_is_active) WHERE contact_method_deleted_at IS NULL;


-- -- 000018_lkp_price_level --------------------------------------------------
-- =====================================================================
-- Tenant migration 018: lkp_price_level -- the 12th CRM lookup table.
-- Source: StonSuite_DBSchema.xlsx (Look Up Tables sheet, Price Levels).
-- Created before customer (019), which FKs customer_price_level here.
-- Idempotent: CREATE TABLE IF NOT EXISTS + INSERT ... ON CONFLICT DO NOTHING.
-- =====================================================================

CREATE TABLE IF NOT EXISTS lkp_price_level (
    price_level_id          SERIAL       PRIMARY KEY,
    price_level_name        VARCHAR(50)  NOT NULL,
    price_level_code        VARCHAR(10)  NOT NULL,
    price_level_discount    DECIMAL(5,2) NOT NULL DEFAULT 0,
    price_level_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    price_level_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    price_level_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    price_level_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    price_level_deleted_at  TIMESTAMP        NULL,
    price_level_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    price_level_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_price_level_code UNIQUE (price_level_code)
);

INSERT INTO lkp_price_level (price_level_name, price_level_code, price_level_discount, price_level_is_active, price_level_is_system, price_level_created_by) VALUES
    ('Base Price',     'PL0',  0.00,  TRUE, TRUE, 1),
    ('Price Level 1',  'PL1',  5.00,  TRUE, TRUE, 1),
    ('Price Level 2',  'PL2',  10.00, TRUE, TRUE, 1),
    ('Price Level 3',  'PL3',  15.00, TRUE, TRUE, 1),
    ('Price Level 4',  'PL4',  20.00, TRUE, TRUE, 1),
    ('Wholesale',      'PLWS', 25.00, TRUE, TRUE, 1)
ON CONFLICT (price_level_code) DO NOTHING;


-- -- 000015_crm_record --------------------------------------------------
-- =====================================================================
-- Tenant migration 014: crm_record -- the single CRM master table (v2).
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


-- -- 000016_crm_record_history --------------------------------------------------
-- =====================================================================
-- Tenant migration 015: crm_record_history -- CRM transition audit (v2).
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


-- -- 000017_crm_workflow_approver --------------------------------------------------
-- =====================================================================
-- Tenant migration 016: crm_workflow_approver -- configurable approvers (v2).
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


-- -- 000019_customer --------------------------------------------------
-- =====================================================================
-- Tenant migration 019: customer -- the single CRM master table (v2).
-- Source of truth: StonSuite_DBSchema.xlsx (Customer sheet), ADR-002.
--
-- One physical table holds Lead, Prospect and Customer records, distinguished
-- by record_type (FK -> lkp_record_type: LEAD/PROS/CUST). Stage advances
-- forward-only (LEAD -> PROS -> CUST) by choosing a crm_status of a later type.
-- Supersedes crm_record (migration 015), which is left in place but unused.
--
-- Design notes (ADR-002):
--   * ss_customer_id is a plain integer owner-stamp (no cross-DB FK -- the
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

    -- CRM / sales-cycle fields (mandatory for LEAD/PROS -- enforced in Go)
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


-- -- 000021_customer_history --------------------------------------------------
-- =====================================================================
-- Tenant migration 021: customer_history -- CRM stage/status change trail (v2).
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


-- -- 000025_customer_approval --------------------------------------------------
-- =====================================================================
-- Tenant migration 025: customer_approval -- per-approver approval tracking (v2).
--
-- One row per (customer record, approver) who has signed off. Lets a workflow
-- require more than one approver: customer.customer_approval_status stays
-- 'pending' until every currently-configured active approver for the record's
-- type/status has a row here, at which point Approve() finalizes it. The
-- UNIQUE constraint is the DB-level guard against the same approver approving
-- twice (customer.customer_approved_by/_at remain the single "final approver"
-- summary columns, unchanged).
-- =====================================================================

CREATE TABLE IF NOT EXISTS customer_approval (
    customer_approval_id    SERIAL       PRIMARY KEY,
    customer_id              INTEGER     NOT NULL REFERENCES customer(customer_id) ON DELETE CASCADE,
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at              TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_customer_approval UNIQUE (customer_id, approver_employee_id)
);

CREATE INDEX IF NOT EXISTS idx_customer_approval_customer ON customer_approval (customer_id);


-- -- 000020_audit_logs_enrich --------------------------------------------------
-- =====================================================================
-- Tenant migration 020: enrich audit_logs into the unified change trail.
--
-- audit_logs was created by migration 011 (record attachments) with:
--   id, actor_user_id, action, resource, resource_id, details, created_at
-- The workbook (Audit_Logs sheet) asks for a richer row-level change trail.
-- We add its columns additively so ONE table serves both attachment events
-- and CRM mutations (ADR-002). Mapping to the workbook's field names:
--   actor_user_id = Changed By   created_at = Changed At   resource_id = Record ID
--
-- Guard: tenants whose schema_version already recorded 011 before audit_logs
-- was added to that migration will not have the table yet. Create it here so
-- the ALTER TABLE statements below always succeed.
-- =====================================================================

CREATE TABLE IF NOT EXISTS audit_logs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    action        TEXT        NOT NULL,
    resource      TEXT        NOT NULL,
    resource_id   TEXT        NOT NULL DEFAULT '',
    details       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- DESIGN NOTE: actor_user_id references the UUID users table (v1 identity model).
-- The v2 CRM uses INTEGER employee IDs. Until the identity model is unified, CRM
-- audit entries written via the employee path should set actor_user_id = NULL and
-- store the employee_id in the details JSONB field as {"employee_id": N}.
-- See: https://github.com/Skookum-Infotech/StoneSuite/issues/XXX (track unification)
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor      ON audit_logs(actor_user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action     ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_resource   ON audit_logs(resource, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC);

ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS table_name  TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS old_value   JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS new_value   JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS ip_address  INET;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS session_id  TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS app_version TEXT;

CREATE INDEX IF NOT EXISTS idx_audit_logs_table ON audit_logs(table_name);


-- -- 000024_attachments_recover --------------------------------------------------
-- Migration 024: recreate workflow_record_attachments if absent.
--
-- Migration 011 created this table with a FK to workflow_records(id).
-- Migration 023 dropped that FK so attachments can reference any record UUID
-- regardless of which table it lives in.
--
-- In dev environments the table may have been manually dropped while
-- schema_version still records 011 as applied. This migration is a safe
-- recovery guard: CREATE TABLE IF NOT EXISTS is a no-op when the table exists,
-- so tenants that already have the table are unaffected.
--
-- The table is created WITHOUT the workflow_records FK because 023 already
-- removed it on tenants where it originally existed.

CREATE TABLE IF NOT EXISTS workflow_record_attachments (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    record_id            UUID        NOT NULL,
    file_name            TEXT        NOT NULL,
    content_type         TEXT        NOT NULL,
    size_bytes           BIGINT      NOT NULL DEFAULT 0,
    storage_key          TEXT        NOT NULL UNIQUE,
    checksum_sha256      TEXT        NOT NULL DEFAULT '',
    status               TEXT        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'clean', 'infected', 'failed')),
    uploaded_by_user_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_wf_record_attachments_record ON workflow_record_attachments(record_id);


-- -- 000023_attachments_drop_wf_fk --------------------------------------------------
-- Migration 023: decouple workflow_record_attachments from workflow_records.
--
-- The attachment table was originally keyed to workflow_records(id), but the
-- v2 relational CRM design stores records in the customer table instead. Drop
-- the FK so attachments can be associated with any record UUID regardless of
-- which table it lives in. Existence is now enforced in application code.
ALTER TABLE IF EXISTS workflow_record_attachments
  DROP CONSTRAINT IF EXISTS workflow_record_attachments_record_id_fkey;


-- -- 000009_seed_crm_workflows --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 8: Seed CRM workflows (Lead/Prospect/Customer).
--
-- On first apply (new tenant): inserts default workflows with states, transitions, and fields.
-- On re-apply (existing tenant): skips workflows that already exist (idempotent).
-- =====================================================================

-- Create a temporary table to store state IDs for use in transitions.
CREATE TEMP TABLE _wf_states (workflow_key TEXT, state_key TEXT, state_id UUID) ON COMMIT DROP;

DO $$
DECLARE
  v_workflow_id UUID;
BEGIN

-- ===== LEAD WORKFLOW =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'lead') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('lead', 'Lead', 'Inbound leads pipeline.', TRUE, TRUE, 1)
  RETURNING id INTO v_workflow_id;

  -- Insert states and track IDs.
  WITH inserted_states AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color)
    VALUES
      (v_workflow_id, 'lead_new', 'LEAD-New', TRUE, FALSE, 0, '#64748b'),
      (v_workflow_id, 'lead_in_progress', 'LEAD-In Progress', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'lead_qualified', 'LEAD-Qualified', FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'lead_unqualified', 'LEAD-UnQualified', FALSE, TRUE, 3, '#ef4444'),
      (v_workflow_id, 'lead_converted', 'LEAD-Converted', FALSE, TRUE, 4, '#22c55e'),
      (v_workflow_id, 'lead_dead', 'LEAD-Dead', FALSE, TRUE, 5, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states (workflow_key, state_key, state_id)
  SELECT 'lead', key, id FROM inserted_states;

  -- Insert transitions using stored state IDs.
  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT
    v_workflow_id,
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'lead' AND state_key = t.from_key),
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'lead' AND state_key = t.to_key),
    t.name, '{}'::jsonb, t.sort_order
  FROM (
    VALUES
      ('lead_new', 'lead_in_progress', 'Start Progress', 0),
      ('lead_new', 'lead_unqualified', 'Disqualify', 1),
      ('lead_in_progress', 'lead_qualified', 'Qualify', 2),
      ('lead_in_progress', 'lead_unqualified', 'Disqualify', 3),
      ('lead_in_progress', 'lead_dead', 'Mark Dead', 4),
      ('lead_qualified', 'lead_converted', 'Convert', 5),
      ('lead_qualified', 'lead_dead', 'Mark Dead', 6)
  ) AS t(from_key, to_key, name, sort_order);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order)
  VALUES
    (v_workflow_id, 'company_name', 'Company Name', 'string', TRUE, '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email', 'Email', 'email', TRUE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone', 'Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'first_name', 'First Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'last_name', 'Last Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4),
    (v_workflow_id, 'source', 'Source', 'enum', FALSE, '["web", "referral", "event", "cold_call", "partner"]'::jsonb, '{}'::jsonb, 5),
    (v_workflow_id, 'estimated_value', 'Estimated Value', 'number', FALSE, '[]'::jsonb, '{}'::jsonb, 6),
    (v_workflow_id, 'sales_rep', 'Sales Rep', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 7),
    (v_workflow_id, 'territory', 'Territory', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 8);

  DELETE FROM _wf_states WHERE workflow_key = 'lead';
END IF;

-- ===== PROSPECT WORKFLOW =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'prospect') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('prospect', 'Prospect', 'Active sales opportunities.', TRUE, TRUE, 2)
  RETURNING id INTO v_workflow_id;

  WITH inserted_states AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color)
    VALUES
      (v_workflow_id, 'prospect_in_discussion', 'PROSPECT-In Discussion', TRUE, FALSE, 0, '#64748b'),
      (v_workflow_id, 'prospect_identified_dms', 'PROSPECT-Identified Decision Makers', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'prospect_qualified', 'PROSPECT-Qualified', FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'prospect_proposal', 'PROSPECT-Proposal', FALSE, FALSE, 3, '#f59e0b'),
      (v_workflow_id, 'prospect_in_negotiation', 'PROSPECT-In Negotiation', FALSE, FALSE, 4, '#f97316'),
      (v_workflow_id, 'prospect_purchasing', 'PROSPECT-Purchasing', FALSE, FALSE, 5, '#a855f7'),
      (v_workflow_id, 'prospect_closed_lost', 'PROSPECT-Closed Lost', FALSE, TRUE, 6, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states (workflow_key, state_key, state_id)
  SELECT 'prospect', key, id FROM inserted_states;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT
    v_workflow_id,
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'prospect' AND state_key = t.from_key),
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'prospect' AND state_key = t.to_key),
    t.name, '{}'::jsonb, t.sort_order
  FROM (
    VALUES
      ('prospect_in_discussion', 'prospect_identified_dms', 'Identify Decision Makers', 0),
      ('prospect_in_discussion', 'prospect_closed_lost', 'Close Lost', 1),
      ('prospect_identified_dms', 'prospect_qualified', 'Qualify', 2),
      ('prospect_identified_dms', 'prospect_closed_lost', 'Close Lost', 3),
      ('prospect_qualified', 'prospect_proposal', 'Send Proposal', 4),
      ('prospect_qualified', 'prospect_closed_lost', 'Close Lost', 5),
      ('prospect_proposal', 'prospect_in_negotiation', 'Begin Negotiation', 6),
      ('prospect_proposal', 'prospect_closed_lost', 'Close Lost', 7),
      ('prospect_in_negotiation', 'prospect_purchasing', 'Move to Purchase', 8),
      ('prospect_in_negotiation', 'prospect_closed_lost', 'Close Lost', 9)
  ) AS t(from_key, to_key, name, sort_order);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order)
  VALUES
    (v_workflow_id, 'company_name', 'Company Name', 'string', TRUE, '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email', 'Email', 'email', TRUE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone', 'Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'deal_size', 'Deal Size', 'number', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'close_date', 'Expected Close Date', 'date', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states WHERE workflow_key = 'prospect';
END IF;

-- ===== CUSTOMER WORKFLOW =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'customer') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('customer', 'Customer', 'Customer lifecycle.', TRUE, TRUE, 3)
  RETURNING id INTO v_workflow_id;

  WITH inserted_states AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color)
    VALUES
      (v_workflow_id, 'customer_closed_won', 'CUSTOMER-Closed Won', TRUE, FALSE, 0, '#22c55e'),
      (v_workflow_id, 'customer_renewal', 'CUSTOMER-Renewal', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'customer_closed_lost', 'CUSTOMER-Closed Lost', FALSE, TRUE, 2, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states (workflow_key, state_key, state_id)
  SELECT 'customer', key, id FROM inserted_states;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT
    v_workflow_id,
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'customer' AND state_key = t.from_key),
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'customer' AND state_key = t.to_key),
    t.name, '{}'::jsonb, t.sort_order
  FROM (
    VALUES
      ('customer_closed_won', 'customer_renewal', 'Up for Renewal', 0),
      ('customer_closed_won', 'customer_closed_lost', 'Mark Lost', 1),
      ('customer_renewal', 'customer_closed_won', 'Renew', 2),
      ('customer_renewal', 'customer_closed_lost', 'Mark Lost', 3)
  ) AS t(from_key, to_key, name, sort_order);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order)
  VALUES
    (v_workflow_id, 'company_name', 'Company Name', 'string', TRUE, '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email', 'Email', 'email', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone', 'Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'legal_name', 'Legal Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'industry', 'Industry', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4),
    (v_workflow_id, 'website', 'Website', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 5),
    (v_workflow_id, 'country', 'Country', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 6),
    (v_workflow_id, 'currency', 'Currency', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 7),
    (v_workflow_id, 'timezone', 'Timezone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 8),
    (v_workflow_id, 'tax_id', 'Tax / VAT ID', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 9),
    (v_workflow_id, 'billing_address', 'Billing Address', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 10),
    (v_workflow_id, 'shipping_address', 'Shipping Address', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 11),
    (v_workflow_id, 'super_admin_name', 'Super Admin Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 12),
    (v_workflow_id, 'super_admin_email', 'Super Admin Email', 'email', TRUE, '[]'::jsonb, '{}'::jsonb, 13),
    (v_workflow_id, 'super_admin_phone', 'Super Admin Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 14);

  DELETE FROM _wf_states WHERE workflow_key = 'customer';
END IF;

END $$;


-- -- 000010_seed_sales_purchases_workflows --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 10: Seed Sales & Purchases workflows.
--
-- Seeds 16 new workflows (8 Sales + 8 Purchases) with states, transitions,
-- and basic field definitions. Idempotent: skips workflows that already exist.
-- All use pipeline_order = 0 (no CRM conversion chain).
-- =====================================================================

CREATE TEMP TABLE _wf_states10 (workflow_key TEXT, state_key TEXT, state_id UUID) ON COMMIT DROP;

DO $$
DECLARE
  v_workflow_id UUID;
BEGIN

-- ===== ESTIMATE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'estimate') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('estimate', 'Estimate', 'Price estimates for customers.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'estimate_draft',    'ESTIMATE-Draft',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'estimate_sent',     'ESTIMATE-Sent',     FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'estimate_accepted', 'ESTIMATE-Accepted', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'estimate_rejected', 'ESTIMATE-Rejected', FALSE, TRUE,  3, '#ef4444'),
      (v_workflow_id, 'estimate_expired',  'ESTIMATE-Expired',  FALSE, TRUE,  4, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'estimate', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='estimate' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='estimate' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('estimate_draft', 'estimate_sent',     'Send to Customer', 0),
    ('estimate_sent',  'estimate_accepted', 'Accept',           1),
    ('estimate_sent',  'estimate_rejected', 'Reject',           2),
    ('estimate_sent',  'estimate_expired',  'Mark Expired',     3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'valid_until',   'Valid Until',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'estimate';
END IF;

-- ===== QUOTE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'quote') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('quote', 'Quote', 'Formal quotes issued to customers.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'quote_draft',    'QUOTE-Draft',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'quote_sent',     'QUOTE-Sent',     FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'quote_accepted', 'QUOTE-Accepted', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'quote_rejected', 'QUOTE-Rejected', FALSE, TRUE,  3, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'quote', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='quote' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='quote' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('quote_draft', 'quote_sent',     'Send Quote', 0),
    ('quote_sent',  'quote_accepted', 'Accept',     1),
    ('quote_sent',  'quote_rejected', 'Reject',     2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'valid_until',   'Valid Until',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'quote';
END IF;

-- ===== SALES ORDER =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'sales_order') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('sales_order', 'Sales Order', 'Confirmed customer orders.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'so_new',       'SO-New',       TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'so_confirmed', 'SO-Confirmed', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'so_processing','SO-Processing',FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'so_fulfilled', 'SO-Fulfilled', FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'so_cancelled', 'SO-Cancelled', FALSE, TRUE,  4, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'sales_order', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='sales_order' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='sales_order' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('so_new',       'so_confirmed',  'Confirm Order',  0),
    ('so_new',       'so_cancelled',  'Cancel',         1),
    ('so_confirmed', 'so_processing', 'Start Processing',2),
    ('so_confirmed', 'so_cancelled',  'Cancel',         3),
    ('so_processing','so_fulfilled',  'Mark Fulfilled', 4),
    ('so_processing','so_cancelled',  'Cancel',         5)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'order_date',    'Order Date',    'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'sales_order';
END IF;

-- ===== INSTALLATION / FABRICATION =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'installation') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('installation', 'Installation / Fabrication', 'Installation and fabrication job management.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'inst_scheduled',  'INST-Scheduled',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'inst_in_progress','INST-In Progress',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'inst_on_hold',    'INST-On Hold',      FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'inst_completed',  'INST-Completed',    FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'inst_cancelled',  'INST-Cancelled',    FALSE, TRUE,  4, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'installation', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='installation' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='installation' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('inst_scheduled',  'inst_in_progress','Start Work',   0),
    ('inst_scheduled',  'inst_cancelled',  'Cancel',       1),
    ('inst_in_progress','inst_on_hold',    'Put On Hold',  2),
    ('inst_in_progress','inst_completed',  'Mark Complete',3),
    ('inst_in_progress','inst_cancelled',  'Cancel',       4),
    ('inst_on_hold',    'inst_in_progress','Resume',       5),
    ('inst_on_hold',    'inst_cancelled',  'Cancel',       6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name',  'Customer Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'scheduled_date', 'Scheduled Date',  'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'location',       'Location/Address','string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'technician',     'Assigned Technician','string',FALSE,'[]'::jsonb,'{}'::jsonb, 3),
    (v_workflow_id, 'notes',          'Notes',           'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'installation';
END IF;

-- ===== INVOICE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'invoice') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('invoice', 'Invoice', 'Customer invoices and billing.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'inv_draft',   'INV-Draft',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'inv_issued',  'INV-Issued',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'inv_overdue', 'INV-Overdue', FALSE, FALSE, 2, '#f97316'),
      (v_workflow_id, 'inv_paid',    'INV-Paid',    FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'inv_void',    'INV-Void',    FALSE, TRUE,  4, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'invoice', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='invoice' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='invoice' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('inv_draft',  'inv_issued',  'Issue Invoice',  0),
    ('inv_draft',  'inv_void',    'Void',           1),
    ('inv_issued', 'inv_paid',    'Mark Paid',      2),
    ('inv_issued', 'inv_overdue', 'Mark Overdue',   3),
    ('inv_issued', 'inv_void',    'Void',           4),
    ('inv_overdue','inv_paid',    'Mark Paid',      5),
    ('inv_overdue','inv_void',    'Void',           6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'invoice_date',  'Invoice Date',  'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'due_date',      'Due Date',      'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'invoice';
END IF;

-- ===== PAYMENT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'payment') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('payment', 'Payment', 'Customer payment tracking and reconciliation.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'pmt_pending',    'PMT-Pending',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'pmt_received',   'PMT-Received',   FALSE, TRUE,  1, '#22c55e'),
      (v_workflow_id, 'pmt_refunded',   'PMT-Refunded',   FALSE, TRUE,  2, '#f97316'),
      (v_workflow_id, 'pmt_voided',     'PMT-Voided',     FALSE, TRUE,  3, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'payment', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='payment' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='payment' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('pmt_pending', 'pmt_received', 'Mark Received', 0),
    ('pmt_pending', 'pmt_voided',   'Void',          1),
    ('pmt_received','pmt_refunded', 'Issue Refund',  2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name',  'Customer Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'amount',         'Amount',         'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'payment_date',   'Payment Date',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'payment_method', 'Payment Method', 'enum',   FALSE,
      '["cash","check","credit_card","bank_transfer","other"]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'payment';
END IF;

-- ===== CREDIT MEMO =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'credit_memo') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('credit_memo', 'Credit Memo', 'Credit memos issued against customer invoices.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'cm_draft',   'CM-Draft',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'cm_issued',  'CM-Issued',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'cm_applied', 'CM-Applied', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'cm_void',    'CM-Void',    FALSE, TRUE,  3, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'credit_memo', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='credit_memo' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='credit_memo' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('cm_draft',  'cm_issued',  'Issue Credit Memo', 0),
    ('cm_draft',  'cm_void',    'Void',              1),
    ('cm_issued', 'cm_applied', 'Apply to Invoice',  2),
    ('cm_issued', 'cm_void',    'Void',              3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'credit_amount', 'Credit Amount', 'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'reason',        'Reason',        'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'credit_memo';
END IF;

-- ===== REFUND =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'refund') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('refund', 'Refund', 'Customer refund requests and processing.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'ref_requested', 'REFUND-Requested', TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'ref_approved',  'REFUND-Approved',  FALSE, FALSE, 1, '#8b5cf6'),
      (v_workflow_id, 'ref_rejected',  'REFUND-Rejected',  FALSE, TRUE,  2, '#ef4444'),
      (v_workflow_id, 'ref_processed', 'REFUND-Processed', FALSE, TRUE,  3, '#22c55e')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'refund', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='refund' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='refund' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('ref_requested', 'ref_approved',  'Approve',  0),
    ('ref_requested', 'ref_rejected',  'Reject',   1),
    ('ref_approved',  'ref_processed', 'Process',  2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name',  'Customer Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'refund_amount',  'Refund Amount',  'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'reason',         'Reason',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'refund';
END IF;

-- ===== VENDOR =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor', 'Vendor', 'Vendor and supplier directory.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vendor_active',   'VENDOR-Active',   TRUE,  FALSE, 0, '#22c55e'),
      (v_workflow_id, 'vendor_on_hold',  'VENDOR-On Hold',  FALSE, FALSE, 1, '#f59e0b'),
      (v_workflow_id, 'vendor_inactive', 'VENDOR-Inactive', FALSE, TRUE,  2, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vendor_active',  'vendor_on_hold',  'Put On Hold',  0),
    ('vendor_active',  'vendor_inactive', 'Deactivate',   1),
    ('vendor_on_hold', 'vendor_active',   'Reactivate',   2),
    ('vendor_on_hold', 'vendor_inactive', 'Deactivate',   3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'company_name', 'Company Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email',        'Email',         'email',  FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone',        'Phone',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'contact_name', 'Contact Name',  'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'payment_terms','Payment Terms', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor';
END IF;

-- ===== REQUISITION =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'requisition') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('requisition', 'Requisition', 'Internal purchase requests.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'req_draft',     'REQ-Draft',     TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'req_submitted', 'REQ-Submitted', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'req_approved',  'REQ-Approved',  FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'req_rejected',  'REQ-Rejected',  FALSE, TRUE,  3, '#ef4444'),
      (v_workflow_id, 'req_purchased', 'REQ-Purchased', FALSE, TRUE,  4, '#22c55e'),
      (v_workflow_id, 'req_cancelled', 'REQ-Cancelled', FALSE, TRUE,  5, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'requisition', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='requisition' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='requisition' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('req_draft',     'req_submitted', 'Submit',        0),
    ('req_draft',     'req_cancelled', 'Cancel',        1),
    ('req_submitted', 'req_approved',  'Approve',       2),
    ('req_submitted', 'req_rejected',  'Reject',        3),
    ('req_submitted', 'req_cancelled', 'Cancel',        4),
    ('req_approved',  'req_purchased', 'Mark Purchased',5),
    ('req_approved',  'req_cancelled', 'Cancel',        6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'description',    'Description',    'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'requested_by',   'Requested By',   'string', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'estimated_cost', 'Estimated Cost', 'number', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'needed_by',      'Needed By Date', 'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'requisition';
END IF;

-- ===== PURCHASE ORDER =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'purchase_order') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('purchase_order', 'Purchase Order', 'Purchase orders sent to vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'po_draft',              'PO-Draft',              TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'po_sent',               'PO-Sent',               FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'po_partially_received', 'PO-Partially Received', FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'po_received',           'PO-Received',           FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'po_cancelled',          'PO-Cancelled',          FALSE, TRUE,  4, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'purchase_order', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='purchase_order' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='purchase_order' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('po_draft',              'po_sent',               'Send to Vendor',     0),
    ('po_draft',              'po_cancelled',          'Cancel',             1),
    ('po_sent',               'po_partially_received', 'Partial Receipt',    2),
    ('po_sent',               'po_received',           'Mark Received',      3),
    ('po_sent',               'po_cancelled',          'Cancel',             4),
    ('po_partially_received', 'po_received',           'Mark Fully Received',5),
    ('po_partially_received', 'po_cancelled',          'Cancel',             6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',   'Vendor Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'order_date',    'Order Date',    'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'expected_date', 'Expected Date', 'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'purchase_order';
END IF;

-- ===== ITEM RECEIPT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'item_receipt') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('item_receipt', 'Item Receipt', 'Record goods received against purchase orders.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'ir_pending',     'IR-Pending',     TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'ir_received',    'IR-Received',    FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'ir_reconciled',  'IR-Reconciled',  FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'ir_discrepancy', 'IR-Discrepancy', FALSE, TRUE,  3, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'item_receipt', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='item_receipt' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='item_receipt' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('ir_pending',  'ir_received',    'Mark Received',  0),
    ('ir_received', 'ir_reconciled',  'Reconcile',      1),
    ('ir_received', 'ir_discrepancy', 'Flag Discrepancy',2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',   'Vendor Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'received_date', 'Received Date', 'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'item_receipt';
END IF;

-- ===== VENDOR BILL =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor_bill') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor_bill', 'Vendor Bill', 'Bills received from vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vb_draft',    'VB-Draft',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'vb_received', 'VB-Received', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'vb_approved', 'VB-Approved', FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'vb_disputed', 'VB-Disputed', FALSE, FALSE, 3, '#f59e0b'),
      (v_workflow_id, 'vb_paid',     'VB-Paid',     FALSE, TRUE,  4, '#22c55e'),
      (v_workflow_id, 'vb_void',     'VB-Void',     FALSE, TRUE,  5, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor_bill', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_bill' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_bill' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vb_draft',    'vb_received', 'Mark Received', 0),
    ('vb_draft',    'vb_void',     'Void',          1),
    ('vb_received', 'vb_approved', 'Approve',       2),
    ('vb_received', 'vb_disputed', 'Dispute',       3),
    ('vb_approved', 'vb_paid',     'Mark Paid',     4),
    ('vb_approved', 'vb_void',     'Void',          5),
    ('vb_disputed', 'vb_approved', 'Resolve',       6),
    ('vb_disputed', 'vb_void',     'Void',          7)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',  'Vendor Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'bill_date',    'Bill Date',    'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'due_date',     'Due Date',     'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'total_amount', 'Total Amount', 'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor_bill';
END IF;

-- ===== VENDOR PAYMENT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor_payment') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor_payment', 'Vendor Payment', 'Payments made to vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vp_pending',   'VP-Pending',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'vp_scheduled', 'VP-Scheduled', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'vp_sent',      'VP-Sent',      FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'vp_cleared',   'VP-Cleared',   FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'vp_voided',    'VP-Voided',    FALSE, TRUE,  4, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor_payment', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_payment' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_payment' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vp_pending',   'vp_scheduled', 'Schedule',   0),
    ('vp_pending',   'vp_voided',    'Void',       1),
    ('vp_scheduled', 'vp_sent',      'Mark Sent',  2),
    ('vp_scheduled', 'vp_voided',    'Void',       3),
    ('vp_sent',      'vp_cleared',   'Clear',      4),
    ('vp_sent',      'vp_voided',    'Void',       5)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',    'Vendor Name',    'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'amount',         'Amount',         'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'payment_date',   'Payment Date',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'payment_method', 'Payment Method', 'enum',   FALSE,
      '["check","bank_transfer","credit_card","other"]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor_payment';
END IF;

-- ===== VENDOR CREDIT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor_credit') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor_credit', 'Vendor Credits', 'Credits received from vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vc_draft',   'VC-Draft',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'vc_issued',  'VC-Issued',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'vc_applied', 'VC-Applied', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'vc_void',    'VC-Void',    FALSE, TRUE,  3, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor_credit', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_credit' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_credit' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vc_draft',  'vc_issued',  'Issue Credit',    0),
    ('vc_draft',  'vc_void',    'Void',            1),
    ('vc_issued', 'vc_applied', 'Apply to Bill',   2),
    ('vc_issued', 'vc_void',    'Void',            3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',   'Vendor Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'credit_amount', 'Credit Amount', 'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'reason',        'Reason',        'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor_credit';
END IF;

-- ===== EXPENSE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'expense') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('expense', 'Expenses', 'Employee expense submission and reimbursement.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'exp_draft',       'EXP-Draft',       TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'exp_submitted',   'EXP-Submitted',   FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'exp_approved',    'EXP-Approved',    FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'exp_rejected',    'EXP-Rejected',    FALSE, TRUE,  3, '#ef4444'),
      (v_workflow_id, 'exp_reimbursed',  'EXP-Reimbursed',  FALSE, TRUE,  4, '#22c55e')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'expense', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='expense' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='expense' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('exp_draft',     'exp_submitted',  'Submit',     0),
    ('exp_submitted', 'exp_approved',   'Approve',    1),
    ('exp_submitted', 'exp_rejected',   'Reject',     2),
    ('exp_approved',  'exp_reimbursed', 'Reimburse',  3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'submitted_by',  'Submitted By',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'amount',        'Amount',        'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'expense_date',  'Expense Date',  'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'category',      'Category',      'enum',   FALSE,
      '["travel","meals","office_supplies","equipment","software","other"]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'description',   'Description',   'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'expense';
END IF;

END $$;


-- -- 000022_deactivate_legacy_crm_record_status --------------------------------------------------
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

-- -- 000023_rag_vectors --------------------------------------------------
-- =====================================================================
-- RAG assistant storage. Vectors live in the tenant DB so cross-tenant
-- retrieval is impossible by construction. owner_user_id / team_id are
-- denormalized onto each chunk so the RBAC scope clause can be ANDed onto
-- the similarity search (scope can only narrow, never widen -- same
-- invariant as the Record Filter Engine).
-- =====================================================================

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rag_chunks (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_type   TEXT NOT NULL DEFAULT 'record',
    source_id     UUID NOT NULL,
    -- Nullable: only the v1 dynamic-workflow store has a real workflows.id UUID
    -- per record. The v2 relational CRM store has no per-record workflow UUID
    -- (its record types are a fixed lead/prospect/customer enum, not rows in
    -- `workflows`), and this column is otherwise unused (not part of any scope
    -- filter) -- see crmstore/rag_loader.go for the UUID-format guard.
    workflow_id   UUID,
    owner_user_id UUID,
    team_id       UUID,
    content       TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    embedding     vector(768) NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Idempotent: relaxes the constraint for tenants provisioned before this
-- change; a no-op on fresh databases created from the CREATE TABLE above.
ALTER TABLE rag_chunks ALTER COLUMN workflow_id DROP NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS rag_chunks_source_idx   ON rag_chunks (source_id);
CREATE INDEX        IF NOT EXISTS rag_chunks_scope_idx    ON rag_chunks (owner_user_id, team_id);
CREATE INDEX        IF NOT EXISTS rag_chunks_embedding_idx
    ON rag_chunks USING hnsw (embedding vector_cosine_ops);

-- Hybrid retrieval -- lexical (keyword) arm beside the vector arm. A generated
-- tsvector over content + a GIN index lets exact terms / rare tokens (record
-- numbers, names, codes) that a 768-dim embedding blurs be matched precisely.
-- 'simple' config (no stemming) so identifiers like INC-2023-Q4-011 survive
-- tokenization. Idempotent + append-only: the generated STORED column is
-- auto-populated for existing rows on ADD.
ALTER TABLE rag_chunks ADD COLUMN IF NOT EXISTS content_tsv tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED;
CREATE INDEX IF NOT EXISTS rag_chunks_tsv_idx ON rag_chunks USING gin (content_tsv);

CREATE TABLE IF NOT EXISTS rag_index_queue (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id   UUID NOT NULL,
    op          TEXT NOT NULL,                    -- 'upsert' | 'delete'
    status      TEXT NOT NULL DEFAULT 'pending',  -- 'pending' | 'done' | 'error'
    attempts    INT  NOT NULL DEFAULT 0,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS rag_index_queue_pending_idx
    ON rag_index_queue (status) WHERE status = 'pending';


-- -- 000026_workflow_state_approvals ------------------------------------------
-- Generic per-state approver gating for the workflow engine. A workflow state
-- is "approval-gated" simply by having >= 1 active approver row here (presence-
-- based, mirroring the CRM crm_workflow_approver model). While a record sits in
-- a gated state it is locked: the engine blocks every outbound transition until
-- each active approver has a row in workflow_record_approval for that
-- (record, state). Approval status is DERIVED from these two tables, so no
-- approval_status column is added to workflow_records.

-- workflow_state_approver -- which tenant users may approve a given state.
CREATE TABLE IF NOT EXISTS workflow_state_approver (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    state_id          UUID        NOT NULL REFERENCES workflow_states(id) ON DELETE CASCADE,
    approver_user_id  UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    is_active         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by        UUID        NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_workflow_state_approver UNIQUE (state_id, approver_user_id)
);
CREATE INDEX IF NOT EXISTS idx_wf_state_approver_state
    ON workflow_state_approver (state_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_wf_state_approver_user
    ON workflow_state_approver (approver_user_id) WHERE is_active;

-- workflow_record_approval -- one row per sign-off in the record's current
-- pending cycle. UNIQUE(record_id, state_id, approver_user_id) is the DB guard
-- against the same approver signing off twice; the engine deletes rows for a
-- (record, state) when the record re-enters that state so each cycle is fresh.
CREATE TABLE IF NOT EXISTS workflow_record_approval (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    record_id         UUID        NOT NULL REFERENCES workflow_records(id) ON DELETE CASCADE,
    state_id          UUID        NOT NULL REFERENCES workflow_states(id) ON DELETE CASCADE,
    approver_user_id  UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    approved_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_workflow_record_approval UNIQUE (record_id, state_id, approver_user_id)
);
CREATE INDEX IF NOT EXISTS idx_wf_record_approval_record
    ON workflow_record_approval (record_id, state_id);


-- -- 000027_inventory_domain --------------------------------------------------
-- =====================================================================
-- Tenant migration 027: Inventory domain -- shared item/stock foundation for
-- Sales Order (and future Purchase Order / Invoice / Manufacturing modules).
-- Source: docs/superpowers/specs/2026-07-08-sales-order-module-design.md sec 5.1-5.2.
-- New lkp_* reference tables (unit of measure, warehouse, tax rate) plus the
-- inventory_item catalog and per-warehouse on-hand stock. inventory_allocation
-- is deferred to migration 028 (it FKs sales_order/sales_order_item).
-- =====================================================================

-- lkp_unit --------------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_unit (
    unit_id             SERIAL       PRIMARY KEY,
    unit_name           VARCHAR(50)  NOT NULL,
    unit_code           VARCHAR(10)  NOT NULL,
    unit_category       VARCHAR(20)  NOT NULL DEFAULT 'count', -- count|length|area|volume|weight
    unit_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    unit_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    unit_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    unit_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    unit_deleted_at     TIMESTAMP        NULL,
    unit_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    unit_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_unit_code UNIQUE (unit_code),
    CONSTRAINT chk_unit_category CHECK (unit_category IN ('count','length','area','volume','weight'))
);

INSERT INTO lkp_unit (unit_name, unit_code, unit_category, unit_is_system, unit_created_by) VALUES
    ('Each','EA','count',TRUE,1), ('Box','BOX','count',TRUE,1), ('Set','SET','count',TRUE,1),
    ('Pallet','PLT','count',TRUE,1), ('Slab','SLAB','count',TRUE,1),
    ('Square Foot','SQFT','area',TRUE,1), ('Square Meter','SQM','area',TRUE,1),
    ('Linear Foot','LFT','length',TRUE,1), ('Kilogram','KG','weight',TRUE,1), ('Pound','LB','weight',TRUE,1)
ON CONFLICT (unit_code) DO NOTHING;

-- lkp_warehouse -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_warehouse (
    warehouse_id             SERIAL       PRIMARY KEY,
    warehouse_uuid           UUID         NOT NULL DEFAULT gen_random_uuid(),
    warehouse_name           VARCHAR(100) NOT NULL,
    warehouse_code           VARCHAR(20)  NOT NULL,
    warehouse_addr_line1     VARCHAR(100) NOT NULL DEFAULT '',
    warehouse_addr_line2     VARCHAR(100) NOT NULL DEFAULT '',
    warehouse_addr_city      VARCHAR(100) NOT NULL DEFAULT '',
    warehouse_addr_state     INTEGER          NULL REFERENCES lkp_state(state_id),
    warehouse_addr_zip       VARCHAR(10)  NOT NULL DEFAULT '',
    warehouse_addr_country   INTEGER          NULL REFERENCES lkp_country(country_id),
    warehouse_is_default     BOOLEAN      NOT NULL DEFAULT FALSE,
    warehouse_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    warehouse_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    warehouse_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    warehouse_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    warehouse_deleted_at     TIMESTAMP        NULL,
    warehouse_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    warehouse_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_warehouse_code UNIQUE (warehouse_code),
    CONSTRAINT uq_warehouse_uuid UNIQUE (warehouse_uuid)
);
-- At most one default warehouse.
CREATE UNIQUE INDEX IF NOT EXISTS uq_warehouse_default
    ON lkp_warehouse (warehouse_is_default) WHERE warehouse_is_default = TRUE;

INSERT INTO lkp_warehouse (warehouse_name, warehouse_code, warehouse_is_default, warehouse_is_system, warehouse_created_by) VALUES
    ('Main Warehouse','MAIN',TRUE,TRUE,1)
ON CONFLICT (warehouse_code) DO NOTHING;

-- lkp_tax_rate ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_tax_rate (
    tax_rate_id             SERIAL       PRIMARY KEY,
    tax_rate_name           VARCHAR(50)  NOT NULL,
    tax_rate_code           VARCHAR(20)  NOT NULL,
    tax_rate_percent        DECIMAL(6,4) NOT NULL DEFAULT 0,
    tax_rate_jurisdiction   VARCHAR(100) NOT NULL DEFAULT '',
    tax_rate_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    tax_rate_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    tax_rate_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    tax_rate_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    tax_rate_deleted_at     TIMESTAMP        NULL,
    tax_rate_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    tax_rate_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_tax_rate_code UNIQUE (tax_rate_code),
    CONSTRAINT chk_tax_rate_percent CHECK (tax_rate_percent >= 0 AND tax_rate_percent <= 100)
);

INSERT INTO lkp_tax_rate (tax_rate_name, tax_rate_code, tax_rate_percent, tax_rate_is_system, tax_rate_created_by) VALUES
    ('No Tax','NONE',0,TRUE,1)
ON CONFLICT (tax_rate_code) DO NOTHING;

-- inventory_item -- sellable catalog item (hybrid PK, own custom_fields) ----
CREATE TABLE IF NOT EXISTS inventory_item (
    inventory_item_id             SERIAL        PRIMARY KEY,
    inventory_item_uuid           UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_item_sku            VARCHAR(50)   NOT NULL,
    inventory_item_name           VARCHAR(150)  NOT NULL,
    inventory_item_description    TEXT          NOT NULL DEFAULT '',
    inventory_item_unit_id        INTEGER       NOT NULL REFERENCES lkp_unit(unit_id),
    inventory_item_unit_price     DECIMAL(15,2) NOT NULL DEFAULT 0,
    inventory_item_currency_id    INTEGER           NULL REFERENCES lkp_currency(currency_id),
    inventory_item_tax_rate_id    INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    inventory_item_is_active      BOOLEAN       NOT NULL DEFAULT TRUE,
    inventory_item_custom_fields  JSONB         NOT NULL DEFAULT '{}',
    inventory_item_created_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_item_created_by     INTEGER           NULL REFERENCES employee(employee_id),
    inventory_item_updated_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_item_updated_by     INTEGER           NULL REFERENCES employee(employee_id),
    inventory_item_deleted_at     TIMESTAMP         NULL,
    inventory_item_deleted_by     INTEGER           NULL REFERENCES employee(employee_id),
    inventory_item_record_version INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_item_uuid UNIQUE (inventory_item_uuid),
    CONSTRAINT chk_inventory_item_unit_price CHECK (inventory_item_unit_price >= 0),
    CONSTRAINT chk_inventory_item_soft_delete CHECK (
        (inventory_item_deleted_at IS NULL AND inventory_item_deleted_by IS NULL) OR
        (inventory_item_deleted_at IS NOT NULL AND inventory_item_deleted_by IS NOT NULL)
    )
);
-- SKU unique among live rows only (case-insensitive), so a SKU can be reused after soft delete.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_item_sku_active
    ON inventory_item (LOWER(inventory_item_sku)) WHERE inventory_item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_item_active ON inventory_item (inventory_item_is_active) WHERE inventory_item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_item_gin    ON inventory_item USING GIN (inventory_item_custom_fields);

-- inventory_stock -- on-hand quantity per item x warehouse ------------------
CREATE TABLE IF NOT EXISTS inventory_stock (
    inventory_stock_id      SERIAL        PRIMARY KEY,
    inventory_item_id       INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id) ON DELETE CASCADE,
    warehouse_id             INTEGER      NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    quantity_on_hand         DECIMAL(14,3) NOT NULL DEFAULT 0,
    reorder_point            DECIMAL(14,3) NOT NULL DEFAULT 0,
    stock_created_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stock_updated_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stock_record_version     INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_stock_item_wh UNIQUE (inventory_item_id, warehouse_id),
    CONSTRAINT chk_inventory_stock_on_hand CHECK (quantity_on_hand >= 0)
);
CREATE INDEX IF NOT EXISTS idx_inv_stock_wh ON inventory_stock (warehouse_id);


-- -- 000028_sales_order -------------------------------------------------------
-- =====================================================================
-- Tenant migration 028: Sales Order -- relational header + line items +
-- inventory allocation + status history. Sibling of `customer` (v2 pattern):
-- hybrid SERIAL+UUID PK, employee-based audit columns, reused lkp_* lookups,
-- snapshot billing/shipping + item data (frozen at create time so later master-
-- data edits don't rewrite history). Supersedes the v1 JSONB `sales_order`
-- workflow (seeded migration 000010) for production use -- that workflow is
-- left in place, unused, per the design doc's "genuinely missing" finding.
-- Source: docs/superpowers/specs/2026-07-08-sales-order-module-design.md sec 5.3-5.4, sec 6.
-- Create order (FK dependency): sales_order -> sales_order_item ->
-- inventory_allocation (FKs both) -> sales_order_history.
-- =====================================================================

CREATE TABLE IF NOT EXISTS sales_order (
    sales_order_id                 SERIAL        PRIMARY KEY,
    sales_order_uuid               UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                 INTEGER           NULL,  -- platform owner stamp, no cross-DB FK (matches customer)
    sales_order_number             VARCHAR(20)       NULL,  -- 'SORD-000001', generated post-insert in Go

    -- Classification (reused lookups)
    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = SORD
    sales_order_status             INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven -- AD-10; mirrors customer_approval_status)
    sales_order_approval_status    VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    sales_order_approved_by        INTEGER           NULL REFERENCES employee(employee_id),  -- last approver (full trail in sales_order_approval)

    -- Primary info
    sales_order_customer_id        INTEGER       NOT NULL REFERENCES customer(customer_id),
    sales_order_po_number          VARCHAR(50)   NOT NULL DEFAULT '',
    sales_order_reference_number   VARCHAR(50)   NOT NULL DEFAULT '',
    sales_order_date               DATE          NOT NULL DEFAULT CURRENT_DATE,
    sales_order_expected_delivery  DATE              NULL,
    sales_order_sales_tax_percent  DECIMAL(6,4)  NOT NULL DEFAULT 0,
    sales_order_memo               TEXT          NOT NULL DEFAULT '',
    sales_order_notes              TEXT          NOT NULL DEFAULT '',
    sales_order_internal_notes     TEXT          NOT NULL DEFAULT '',
    sales_order_terms_conditions   TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    sales_order_sales_rep_id       INTEGER           NULL REFERENCES employee(employee_id),
    sales_order_owner_id           INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    sales_order_payment_terms      INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    sales_order_payment_due_date   DATE              NULL,  -- schema.org paymentDueDate; derived order_date + terms.net_days when unset (AD-8)
    sales_order_price_level        INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    sales_order_currency           INTEGER           NULL REFERENCES lkp_currency(currency_id),
    sales_order_exchange_rate      DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored -- snapshots must be immutable once frozen)
    sales_order_subtotal           DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_discount_total     DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_tax_total          DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_shipping_charge    DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_adjustment         DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_grand_total        DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot
    sales_order_bill_customer_name VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_bill_attention     VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_bill_addr_line1    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_bill_addr_line2    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_bill_addr_suitenum VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_bill_addr_city     VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_bill_addr_state    INTEGER          NULL REFERENCES lkp_state(state_id),
    sales_order_bill_addr_zip      VARCHAR(10)  NOT NULL DEFAULT '',
    sales_order_bill_addr_country  INTEGER          NULL REFERENCES lkp_country(country_id),
    sales_order_bill_phone         VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_bill_fax           VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_bill_email         VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    sales_order_ship_same_as_bill  BOOLEAN      NOT NULL DEFAULT FALSE,
    sales_order_ship_customer_name VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_ship_attention     VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_ship_addr_line1    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_ship_addr_line2    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_ship_addr_suitenum VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_ship_addr_city     VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_ship_addr_state    INTEGER          NULL REFERENCES lkp_state(state_id),
    sales_order_ship_addr_zip      VARCHAR(10)  NOT NULL DEFAULT '',
    sales_order_ship_addr_country  INTEGER          NULL REFERENCES lkp_country(country_id),
    sales_order_ship_phone         VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_ship_fax           VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_ship_email         VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + lineage + audit
    sales_order_custom_fields      JSONB        NOT NULL DEFAULT '{}',
    sales_order_parent_id          INTEGER          NULL REFERENCES sales_order(sales_order_id),
    sales_order_created_at         TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sales_order_created_by         INTEGER          NULL REFERENCES employee(employee_id),
    sales_order_updated_at         TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sales_order_updated_by         INTEGER          NULL REFERENCES employee(employee_id),
    sales_order_deleted_at         TIMESTAMP        NULL,
    sales_order_deleted_by         INTEGER          NULL REFERENCES employee(employee_id),
    sales_order_record_version     INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_sales_order_uuid   UNIQUE (sales_order_uuid),
    CONSTRAINT uq_sales_order_number UNIQUE (sales_order_number),
    CONSTRAINT chk_so_approval_status CHECK (sales_order_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_so_tax_percent    CHECK (sales_order_sales_tax_percent >= 0 AND sales_order_sales_tax_percent <= 100),
    CONSTRAINT chk_so_totals_nonneg  CHECK (sales_order_subtotal >= 0 AND sales_order_grand_total >= 0),
    CONSTRAINT chk_so_soft_delete    CHECK (
        (sales_order_deleted_at IS NULL AND sales_order_deleted_by IS NULL) OR
        (sales_order_deleted_at IS NOT NULL AND sales_order_deleted_by IS NOT NULL)
    )
);

-- sales_order_item -- ordered lines (snapshot sku/name/description/unit/price/tax) --
CREATE TABLE IF NOT EXISTS sales_order_item (
    sales_order_item_id     SERIAL        PRIMARY KEY,
    sales_order_item_uuid   UUID          NOT NULL DEFAULT gen_random_uuid(),
    sales_order_id          INTEGER       NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    line_number              INTEGER      NOT NULL,
    inventory_item_id       INTEGER           NULL REFERENCES inventory_item(inventory_item_id), -- NULL = free-text line
    warehouse_id             INTEGER          NULL REFERENCES lkp_warehouse(warehouse_id),

    -- Snapshots (frozen at add time)
    item_name                VARCHAR(150) NOT NULL DEFAULT '',
    sku                       VARCHAR(50)  NOT NULL DEFAULT '',
    description               TEXT         NOT NULL DEFAULT '',
    unit_id                   INTEGER          NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)  NOT NULL DEFAULT '',
    quantity                  DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent          DECIMAL(6,4) NOT NULL DEFAULT 0,
    tax_rate_id               INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent               DECIMAL(6,4) NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Fulfillment (schema.org OrderItem.orderItemStatus): maintained rollup of this
    -- line's allocations' fulfilled_quantity; status label derived open|partial|filled (AD-9)
    line_fulfilled_quantity   DECIMAL(14,3) NOT NULL DEFAULT 0,

    item_created_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by           INTEGER          NULL REFERENCES employee(employee_id),
    item_updated_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at           TIMESTAMP        NULL,
    item_record_version       INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_sales_order_item_uuid UNIQUE (sales_order_item_uuid),
    CONSTRAINT chk_soi_qty              CHECK (quantity >= 0),
    CONSTRAINT chk_soi_unit_price       CHECK (unit_price >= 0),
    CONSTRAINT chk_soi_discount         CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_soi_tax              CHECK (tax_percent >= 0 AND tax_percent <= 100),
    CONSTRAINT chk_soi_fulfilled        CHECK (line_fulfilled_quantity >= 0 AND line_fulfilled_quantity <= quantity)
);

-- inventory_allocation -- reservation per order line (shared inventory domain, not owned by SO) --
CREATE TABLE IF NOT EXISTS inventory_allocation (
    inventory_allocation_id    SERIAL        PRIMARY KEY,
    inventory_allocation_uuid  UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_item_id          INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    warehouse_id               INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    sales_order_id             INTEGER       NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    sales_order_item_id        INTEGER       NOT NULL REFERENCES sales_order_item(sales_order_item_id) ON DELETE CASCADE,
    allocated_quantity         DECIMAL(14,3) NOT NULL DEFAULT 0,
    fulfilled_quantity         DECIMAL(14,3) NOT NULL DEFAULT 0,
    allocation_status          VARCHAR(20)   NOT NULL DEFAULT 'reserved', -- reserved|partially_fulfilled|fulfilled|released
    allocation_created_at      TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    allocation_created_by      INTEGER           NULL REFERENCES employee(employee_id),
    allocation_updated_at      TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    allocation_record_version  INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_allocation_uuid UNIQUE (inventory_allocation_uuid),
    CONSTRAINT chk_alloc_qty        CHECK (allocated_quantity >= 0),
    CONSTRAINT chk_alloc_fulfilled  CHECK (fulfilled_quantity >= 0 AND fulfilled_quantity <= allocated_quantity),
    CONSTRAINT chk_alloc_status     CHECK (allocation_status IN ('reserved','partially_fulfilled','fulfilled','released'))
);

-- sales_order_history -- typed from/to status trail (mirrors customer_history) --
CREATE TABLE IF NOT EXISTS sales_order_history (
    sales_order_history_id  SERIAL       PRIMARY KEY,
    sales_order_id          INTEGER      NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    from_status_id          INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id            INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                  VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | cancel | update | approve
    actor_employee_id       INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                JSONB        NOT NULL DEFAULT '{}',
    at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes -- listing/filtering (all partial on live rows) -------------------
CREATE INDEX IF NOT EXISTS idx_so_customer   ON sales_order (sales_order_customer_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_status     ON sales_order (sales_order_status)      WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_date       ON sales_order (sales_order_date)        WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_sales_rep  ON sales_order (sales_order_sales_rep_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_owner      ON sales_order (sales_order_owner_id)     WHERE sales_order_deleted_at IS NULL;
-- Keyset pagination tiebreaker (created_at, id) -- matches query/ default sort.
CREATE INDEX IF NOT EXISTS idx_so_created    ON sales_order (sales_order_created_at, sales_order_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_custom_gin ON sales_order USING GIN (sales_order_custom_fields);

CREATE INDEX IF NOT EXISTS idx_soi_order ON sales_order_item (sales_order_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_soi_item  ON sales_order_item (inventory_item_id);
-- Line number unique among live rows only (mirrors uq_inventory_item_sku_active):
-- Update() soft-deletes an order's old lines and re-inserts replacements
-- reusing the same line numbers, which a table-wide UNIQUE constraint would reject.
CREATE UNIQUE INDEX IF NOT EXISTS uq_soi_line_active
    ON sales_order_item (sales_order_id, line_number) WHERE item_deleted_at IS NULL;

-- -- 000029_sales_order_schema_org_alignment ----------------------------------
-- =====================================================================
-- Tenant migration 029: align Sales Order with schema.org/Order + optional,
-- configuration-driven approval. Additive & idempotent; no destructive change.
--   AD-8  paymentDueDate    -> lkp_payment_terms.payment_terms_net_days (below)
--                              + sales_order.sales_order_payment_due_date (in 028)
--   AD-9  orderItemStatus   -> sales_order_item.line_fulfilled_quantity (in 028)
--   AD-10 approval gate     -> sales_order_approver / sales_order_approval (below)
-- Source: docs/superpowers/specs/2026-07-08-sales-order-module-design.md sec 2.1, sec 5.0, sec 5.5.
-- =====================================================================

-- AD-8: net-days on the existing payment-terms lookup so a due date can be
-- derived (order_date + net_days). Existing table -> idempotent ALTER + backfill.
ALTER TABLE lkp_payment_terms
    ADD COLUMN IF NOT EXISTS payment_terms_net_days INTEGER NOT NULL DEFAULT 0;

UPDATE lkp_payment_terms SET payment_terms_net_days = 10  WHERE payment_terms_code = 'N10_';
UPDATE lkp_payment_terms SET payment_terms_net_days = 15  WHERE payment_terms_code = 'N15_';
UPDATE lkp_payment_terms SET payment_terms_net_days = 30  WHERE payment_terms_code IN ('N30_','D50N'); -- 50% Deposit Net 30
UPDATE lkp_payment_terms SET payment_terms_net_days = 45  WHERE payment_terms_code = 'N45_';
UPDATE lkp_payment_terms SET payment_terms_net_days = 60  WHERE payment_terms_code = 'N60_';
UPDATE lkp_payment_terms SET payment_terms_net_days = 90  WHERE payment_terms_code = 'N90_';
UPDATE lkp_payment_terms SET payment_terms_net_days = 120 WHERE payment_terms_code = 'N120';
UPDATE lkp_payment_terms SET payment_terms_net_days = 0   WHERE payment_terms_code IN ('COR_','COD_','DOR_'); -- due immediately

-- AD-8: AR aging / overdue-order lookups by due date (partial on live rows).
CREATE INDEX IF NOT EXISTS idx_so_payment_due
    ON sales_order (sales_order_payment_due_date) WHERE sales_order_deleted_at IS NULL;

-- AD-10: approver configuration -- which employee may approve at a given SORD
-- status. Keyed to lkp_record_status (crm_workflow_approver points at the
-- CRM-only lkp_crm_status, so it can't be reused verbatim). Zero rows for a
-- status = no gate there; N rows = N required sign-offs.
CREATE TABLE IF NOT EXISTS sales_order_approver (
    sales_order_approver_id SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = SORD
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active               BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by              INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_sales_order_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_sales_order_approver_lookup
    ON sales_order_approver (record_type_id, record_status_id) WHERE is_active;

-- AD-10: approval tracking -- one row per approver who signed off on an order at
-- a status. sales_order.sales_order_approval_status stays 'pending' until the
-- sign-off count reaches the active configured-approver count. Mirrors customer_approval.
CREATE TABLE IF NOT EXISTS sales_order_approval (
    sales_order_approval_id SERIAL      PRIMARY KEY,
    sales_order_id          INTEGER     NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- status the sign-off was for
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at             TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_sales_order_approval UNIQUE (sales_order_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_sales_order_approval_order ON sales_order_approval (sales_order_id);

CREATE INDEX IF NOT EXISTS idx_so_history_order ON sales_order_history (sales_order_id);

CREATE INDEX IF NOT EXISTS idx_alloc_item      ON inventory_allocation (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_alloc_item_wh   ON inventory_allocation (inventory_item_id, warehouse_id);
CREATE INDEX IF NOT EXISTS idx_alloc_order     ON inventory_allocation (sales_order_id);
CREATE INDEX IF NOT EXISTS idx_alloc_line      ON inventory_allocation (sales_order_item_id);
-- Partial index for the "available/allocated" aggregation (open reservations only).
CREATE INDEX IF NOT EXISTS idx_alloc_open      ON inventory_allocation (inventory_item_id, warehouse_id)
    WHERE allocation_status IN ('reserved','partially_fulfilled');



-- =====================================================================
-- INVOICE MODULE
-- =====================================================================

CREATE TABLE IF NOT EXISTS invoice (
    invoice_id                  SERIAL        PRIMARY KEY,
    invoice_uuid                UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id               INTEGER          NULL,
    invoice_number               VARCHAR(20)      NULL,

    -- Classification
    record_type                  INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    invoice_status                INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Source linkage
    invoice_customer_id          INTEGER       NOT NULL REFERENCES customer(customer_id),
    invoice_sales_order_id       INTEGER           NULL REFERENCES sales_order(sales_order_id) ON DELETE SET NULL,

    -- Primary info
    invoice_po_number            VARCHAR(50)   NOT NULL DEFAULT '',
    invoice_reference_number     VARCHAR(50)   NOT NULL DEFAULT '',
    invoice_date                 DATE          NOT NULL DEFAULT CURRENT_DATE,
    invoice_due_date             DATE              NULL,
    invoice_sales_tax_percent    DECIMAL(6,4)  NOT NULL DEFAULT 0,
    invoice_memo                 TEXT          NOT NULL DEFAULT '',
    invoice_notes                TEXT          NOT NULL DEFAULT '',
    invoice_internal_notes       TEXT          NOT NULL DEFAULT '',
    invoice_terms_conditions     TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    invoice_sales_rep_id         INTEGER           NULL REFERENCES employee(employee_id),
    invoice_owner_id             INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    invoice_payment_terms        INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    invoice_price_level          INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    invoice_currency             INTEGER           NULL REFERENCES lkp_currency(currency_id),
    invoice_exchange_rate        DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    invoice_subtotal             DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_discount_total       DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_tax_total            DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_shipping_charge      DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_adjustment           DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_grand_total          DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- AR balance (stored, updated by payment-recording + transitions)
    invoice_amount_paid          DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_balance_due          DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot (copied from customer, or from sales_order on conversion)
    invoice_bill_customer_name   VARCHAR(150) NOT NULL DEFAULT '',
    invoice_bill_attention       VARCHAR(150) NOT NULL DEFAULT '',
    invoice_bill_addr_line1      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_bill_addr_line2      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_bill_addr_suitenum   VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_bill_addr_city       VARCHAR(100) NOT NULL DEFAULT '',
    invoice_bill_addr_state      INTEGER          NULL REFERENCES lkp_state(state_id),
    invoice_bill_addr_zip        VARCHAR(10)  NOT NULL DEFAULT '',
    invoice_bill_addr_country    INTEGER          NULL REFERENCES lkp_country(country_id),
    invoice_bill_phone           VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_bill_fax             VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_bill_email           VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    invoice_ship_same_as_bill    BOOLEAN      NOT NULL DEFAULT FALSE,
    invoice_ship_customer_name   VARCHAR(150) NOT NULL DEFAULT '',
    invoice_ship_attention       VARCHAR(150) NOT NULL DEFAULT '',
    invoice_ship_addr_line1      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_ship_addr_line2      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_ship_addr_suitenum   VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_ship_addr_city       VARCHAR(100) NOT NULL DEFAULT '',
    invoice_ship_addr_state      INTEGER          NULL REFERENCES lkp_state(state_id),
    invoice_ship_addr_zip        VARCHAR(10)  NOT NULL DEFAULT '',
    invoice_ship_addr_country    INTEGER          NULL REFERENCES lkp_country(country_id),
    invoice_ship_phone           VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_ship_fax             VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_ship_email           VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + lineage + audit
    invoice_custom_fields        JSONB        NOT NULL DEFAULT '{}',
    invoice_parent_id            INTEGER          NULL REFERENCES invoice(invoice_id),
    invoice_created_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    invoice_created_by           INTEGER          NULL REFERENCES employee(employee_id),
    invoice_updated_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    invoice_updated_by            INTEGER          NULL REFERENCES employee(employee_id),
    invoice_deleted_at            TIMESTAMP        NULL,
    invoice_deleted_by            INTEGER          NULL REFERENCES employee(employee_id),
    invoice_record_version        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_invoice_uuid     UNIQUE (invoice_uuid),
    CONSTRAINT uq_invoice_number   UNIQUE (invoice_number),
    CONSTRAINT chk_invoice_tax_percent   CHECK (invoice_sales_tax_percent >= 0 AND invoice_sales_tax_percent <= 100),
    CONSTRAINT chk_invoice_totals_nonneg CHECK (invoice_subtotal >= 0 AND invoice_grand_total >= 0),
    CONSTRAINT chk_invoice_paid_nonneg   CHECK (invoice_amount_paid >= 0 AND invoice_balance_due >= 0),
    CONSTRAINT chk_invoice_soft_delete   CHECK (
        (invoice_deleted_at IS NULL AND invoice_deleted_by IS NULL) OR
        (invoice_deleted_at IS NOT NULL AND invoice_deleted_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS invoice_item (
    invoice_item_id          SERIAL        PRIMARY KEY,
    invoice_item_uuid        UUID          NOT NULL DEFAULT gen_random_uuid(),
    invoice_id                INTEGER       NOT NULL REFERENCES invoice(invoice_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),
    sales_order_item_id       INTEGER           NULL REFERENCES sales_order_item(sales_order_item_id) ON DELETE SET NULL,

    -- Snapshots (frozen at add/conversion time -- never re-read from catalog)
    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description                TEXT          NOT NULL DEFAULT '',
    unit_id                    INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                  VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                   DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent           DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                 INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                 DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal               DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                     DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                   DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by              INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at               TIMESTAMP        NULL,
    item_record_version           INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_invoice_item_uuid UNIQUE (invoice_item_uuid),
    CONSTRAINT chk_ii_qty           CHECK (quantity >= 0),
    CONSTRAINT chk_ii_unit_price    CHECK (unit_price >= 0),
    CONSTRAINT chk_ii_discount      CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_ii_tax           CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

CREATE TABLE IF NOT EXISTS invoice_history (
    invoice_history_id       SERIAL       PRIMARY KEY,
    invoice_id                INTEGER      NOT NULL REFERENCES invoice(invoice_id) ON DELETE CASCADE,
    from_status_id             INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                      VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id            INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                     JSONB        NOT NULL DEFAULT '{}',
    at                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- invoice (listing/filtering)
CREATE INDEX IF NOT EXISTS idx_inv_customer      ON invoice (invoice_customer_id)     WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_sales_order    ON invoice (invoice_sales_order_id)  WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_status          ON invoice (invoice_status)          WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_date            ON invoice (invoice_date)            WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_due_date        ON invoice (invoice_due_date)        WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_sales_rep       ON invoice (invoice_sales_rep_id)    WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_owner           ON invoice (invoice_owner_id)        WHERE invoice_deleted_at IS NULL;
-- Keyset pagination tiebreakers (per sortable column + id)
CREATE INDEX IF NOT EXISTS idx_inv_created_id      ON invoice (invoice_created_at, invoice_id)     WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_updated_id      ON invoice (invoice_updated_at, invoice_id)     WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_duedate_id      ON invoice (invoice_due_date, invoice_id)       WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_grandtotal_id   ON invoice (invoice_grand_total, invoice_id)    WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_balance_id      ON invoice (invoice_balance_due, invoice_id)    WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_status_created  ON invoice (invoice_status, invoice_created_at, invoice_id) WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_custom_gin      ON invoice USING GIN (invoice_custom_fields);

-- invoice_item
-- Line numbers are unique per invoice among LIVE rows only, so Update can
-- soft-delete a line and re-insert the same line_number (mirrors uq_soi_line_active).
CREATE UNIQUE INDEX IF NOT EXISTS uq_ii_line_active
    ON invoice_item (invoice_id, line_number) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ii_invoice     ON invoice_item (invoice_id)          WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ii_item        ON invoice_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_ii_so_item     ON invoice_item (sales_order_item_id);

-- invoice_history
CREATE INDEX IF NOT EXISTS idx_inv_history_invoice ON invoice_history (invoice_id);
CREATE TABLE IF NOT EXISTS estimate (
    estimate_id                  SERIAL        PRIMARY KEY,
    estimate_uuid                UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                INTEGER          NULL,  -- platform owner stamp, no cross-DB FK (matches customer/sales_order/invoice)
    estimate_number               VARCHAR(20)      NULL,  -- 'ESTM-000001', generated post-insert in Go

    -- Classification
    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = ESTM
    estimate_status                INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven -- AD-8, mirrors sales_order_approval_status)
    estimate_approval_status       VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    estimate_approved_by           INTEGER           NULL REFERENCES employee(employee_id),

    -- Primary info
    estimate_customer_id           INTEGER       NOT NULL REFERENCES customer(customer_id),
    estimate_po_number             VARCHAR(50)   NOT NULL DEFAULT '',
    estimate_reference_number      VARCHAR(50)   NOT NULL DEFAULT '',
    estimate_date                  DATE          NOT NULL DEFAULT CURRENT_DATE,
    estimate_valid_until           DATE              NULL,  -- matches v1 workflow field 'valid_until'
    estimate_sales_tax_percent     DECIMAL(6,4)  NOT NULL DEFAULT 0,
    estimate_memo                  TEXT          NOT NULL DEFAULT '',
    estimate_notes                 TEXT          NOT NULL DEFAULT '',
    estimate_internal_notes        TEXT          NOT NULL DEFAULT '',
    estimate_terms_conditions      TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    estimate_sales_rep_id          INTEGER           NULL REFERENCES employee(employee_id),
    estimate_owner_id              INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    estimate_payment_terms         INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    estimate_price_level           INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    estimate_currency              INTEGER           NULL REFERENCES lkp_currency(currency_id),
    estimate_exchange_rate         DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    estimate_subtotal              DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_discount_total        DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_tax_total             DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_shipping_charge       DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_adjustment            DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_grand_total           DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot (copied from customer)
    estimate_bill_customer_name    VARCHAR(150) NOT NULL DEFAULT '',
    estimate_bill_attention        VARCHAR(150) NOT NULL DEFAULT '',
    estimate_bill_addr_line1       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_bill_addr_line2       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_bill_addr_suitenum    VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_bill_addr_city        VARCHAR(100) NOT NULL DEFAULT '',
    estimate_bill_addr_state       INTEGER          NULL REFERENCES lkp_state(state_id),
    estimate_bill_addr_zip         VARCHAR(10)  NOT NULL DEFAULT '',
    estimate_bill_addr_country     INTEGER          NULL REFERENCES lkp_country(country_id),
    estimate_bill_phone            VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_bill_fax              VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_bill_email            VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    estimate_ship_same_as_bill     BOOLEAN      NOT NULL DEFAULT FALSE,
    estimate_ship_customer_name    VARCHAR(150) NOT NULL DEFAULT '',
    estimate_ship_attention        VARCHAR(150) NOT NULL DEFAULT '',
    estimate_ship_addr_line1       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_ship_addr_line2       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_ship_addr_suitenum    VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_ship_addr_city        VARCHAR(100) NOT NULL DEFAULT '',
    estimate_ship_addr_state       INTEGER          NULL REFERENCES lkp_state(state_id),
    estimate_ship_addr_zip         VARCHAR(10)  NOT NULL DEFAULT '',
    estimate_ship_addr_country     INTEGER          NULL REFERENCES lkp_country(country_id),
    estimate_ship_phone            VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_ship_fax              VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_ship_email            VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + audit
    estimate_custom_fields         JSONB        NOT NULL DEFAULT '{}',
    estimate_created_at            TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    estimate_created_by            INTEGER          NULL REFERENCES employee(employee_id),
    estimate_updated_at            TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    estimate_updated_by            INTEGER          NULL REFERENCES employee(employee_id),
    estimate_deleted_at            TIMESTAMP        NULL,
    estimate_deleted_by            INTEGER          NULL REFERENCES employee(employee_id),
    estimate_record_version        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_estimate_uuid       UNIQUE (estimate_uuid),
    CONSTRAINT uq_estimate_number     UNIQUE (estimate_number),
    CONSTRAINT chk_est_approval_status CHECK (estimate_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_est_tax_percent    CHECK (estimate_sales_tax_percent >= 0 AND estimate_sales_tax_percent <= 100),
    CONSTRAINT chk_est_totals_nonneg  CHECK (estimate_subtotal >= 0 AND estimate_grand_total >= 0),
    CONSTRAINT chk_est_soft_delete    CHECK (
        (estimate_deleted_at IS NULL AND estimate_deleted_by IS NULL) OR
        (estimate_deleted_at IS NOT NULL AND estimate_deleted_by IS NOT NULL)
    )
);

-- 5.2 estimate_item (line items)

CREATE TABLE IF NOT EXISTS estimate_item (
    estimate_item_id          SERIAL        PRIMARY KEY,
    estimate_item_uuid        UUID          NOT NULL DEFAULT gen_random_uuid(),
    estimate_id                INTEGER       NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    line_number                 INTEGER      NOT NULL,
    inventory_item_id           INTEGER          NULL REFERENCES inventory_item(inventory_item_id),   -- NULL = free-text line

    -- Snapshots (frozen at add time -- never re-read from catalog)
    item_name                   VARCHAR(150)  NOT NULL DEFAULT '',
    sku                          VARCHAR(50)   NOT NULL DEFAULT '',
    description                  TEXT          NOT NULL DEFAULT '',
    unit_id                      INTEGER          NULL REFERENCES lkp_unit(unit_id),
    unit_code                    VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                     DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                   DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent             DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                   INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                   DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                       DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                      DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at                 TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by                 INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at                 TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at                  TIMESTAMP        NULL,
    item_record_version              INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_estimate_item_uuid UNIQUE (estimate_item_uuid),
    CONSTRAINT chk_esti_qty          CHECK (quantity >= 0),
    CONSTRAINT chk_esti_unit_price   CHECK (unit_price >= 0),
    CONSTRAINT chk_esti_discount     CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_esti_tax          CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- 5.3 estimate_history

CREATE TABLE IF NOT EXISTS estimate_history (
    estimate_history_id       SERIAL       PRIMARY KEY,
    estimate_id                 INTEGER      NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                        VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | update | approve
    actor_employee_id              INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                       JSONB        NOT NULL DEFAULT '{}',
    at                             TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 5.4 estimate_approver / estimate_approval (AD-8, mirrors sales_order_approver/_approval)

CREATE TABLE IF NOT EXISTS estimate_approver (
    estimate_approver_id    SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = ESTM
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_estimate_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS estimate_approval (
    estimate_approval_id    SERIAL      PRIMARY KEY,
    estimate_id              INTEGER     NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    record_status_id         INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- status the sign-off was for
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at               TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_estimate_approval UNIQUE (estimate_id, record_status_id, approver_employee_id)
);

-- 5.5 quote (header)

CREATE TABLE IF NOT EXISTS quote (
    quote_id                     SERIAL        PRIMARY KEY,
    quote_uuid                   UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                 INTEGER          NULL,  -- platform owner stamp, no cross-DB FK
    quote_number                   VARCHAR(20)      NULL,  -- 'QUOT-000001', generated post-insert in Go

    -- Classification
    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = QUOT
    quote_status                    INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven -- AD-8)
    quote_approval_status           VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    quote_approved_by               INTEGER           NULL REFERENCES employee(employee_id),

    -- Lineage (AD-5): source Estimate, if any. Nullable -- a Quote may be created standalone.
    quote_estimate_id                INTEGER          NULL REFERENCES estimate(estimate_id),

    -- Primary info
    quote_customer_id                INTEGER       NOT NULL REFERENCES customer(customer_id),
    quote_po_number                  VARCHAR(50)   NOT NULL DEFAULT '',
    quote_reference_number           VARCHAR(50)   NOT NULL DEFAULT '',
    quote_date                       DATE          NOT NULL DEFAULT CURRENT_DATE,
    quote_valid_until                DATE              NULL,
    quote_sales_tax_percent          DECIMAL(6,4)  NOT NULL DEFAULT 0,
    quote_memo                       TEXT          NOT NULL DEFAULT '',
    quote_notes                      TEXT          NOT NULL DEFAULT '',
    quote_internal_notes             TEXT          NOT NULL DEFAULT '',
    quote_terms_conditions           TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    quote_sales_rep_id               INTEGER           NULL REFERENCES employee(employee_id),
    quote_owner_id                   INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    quote_payment_terms              INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    quote_price_level                INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    quote_currency                   INTEGER           NULL REFERENCES lkp_currency(currency_id),
    quote_exchange_rate              DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    quote_subtotal                   DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_discount_total             DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_tax_total                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_shipping_charge            DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_adjustment                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_grand_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot
    quote_bill_customer_name         VARCHAR(150) NOT NULL DEFAULT '',
    quote_bill_attention             VARCHAR(150) NOT NULL DEFAULT '',
    quote_bill_addr_line1            VARCHAR(100) NOT NULL DEFAULT '',
    quote_bill_addr_line2            VARCHAR(100) NOT NULL DEFAULT '',
    quote_bill_addr_suitenum         VARCHAR(20)  NOT NULL DEFAULT '',
    quote_bill_addr_city             VARCHAR(100) NOT NULL DEFAULT '',
    quote_bill_addr_state            INTEGER          NULL REFERENCES lkp_state(state_id),
    quote_bill_addr_zip              VARCHAR(10)  NOT NULL DEFAULT '',
    quote_bill_addr_country          INTEGER          NULL REFERENCES lkp_country(country_id),
    quote_bill_phone                 VARCHAR(20)  NOT NULL DEFAULT '',
    quote_bill_fax                   VARCHAR(20)  NOT NULL DEFAULT '',
    quote_bill_email                 VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    quote_ship_same_as_bill          BOOLEAN      NOT NULL DEFAULT FALSE,
    quote_ship_customer_name         VARCHAR(150) NOT NULL DEFAULT '',
    quote_ship_attention             VARCHAR(150) NOT NULL DEFAULT '',
    quote_ship_addr_line1            VARCHAR(100) NOT NULL DEFAULT '',
    quote_ship_addr_line2            VARCHAR(100) NOT NULL DEFAULT '',
    quote_ship_addr_suitenum         VARCHAR(20)  NOT NULL DEFAULT '',
    quote_ship_addr_city             VARCHAR(100) NOT NULL DEFAULT '',
    quote_ship_addr_state            INTEGER          NULL REFERENCES lkp_state(state_id),
    quote_ship_addr_zip              VARCHAR(10)  NOT NULL DEFAULT '',
    quote_ship_addr_country          INTEGER          NULL REFERENCES lkp_country(country_id),
    quote_ship_phone                 VARCHAR(20)  NOT NULL DEFAULT '',
    quote_ship_fax                   VARCHAR(20)  NOT NULL DEFAULT '',
    quote_ship_email                 VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + audit
    quote_custom_fields               JSONB        NOT NULL DEFAULT '{}',
    quote_created_at                  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    quote_created_by                  INTEGER          NULL REFERENCES employee(employee_id),
    quote_updated_at                  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    quote_updated_by                  INTEGER          NULL REFERENCES employee(employee_id),
    quote_deleted_at                  TIMESTAMP        NULL,
    quote_deleted_by                  INTEGER          NULL REFERENCES employee(employee_id),
    quote_record_version              INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_quote_uuid       UNIQUE (quote_uuid),
    CONSTRAINT uq_quote_number     UNIQUE (quote_number),
    CONSTRAINT chk_quo_approval_status CHECK (quote_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_quo_tax_percent    CHECK (quote_sales_tax_percent >= 0 AND quote_sales_tax_percent <= 100),
    CONSTRAINT chk_quo_totals_nonneg  CHECK (quote_subtotal >= 0 AND quote_grand_total >= 0),
    CONSTRAINT chk_quo_soft_delete    CHECK (
        (quote_deleted_at IS NULL AND quote_deleted_by IS NULL) OR
        (quote_deleted_at IS NOT NULL AND quote_deleted_by IS NOT NULL)
    )
);

-- 5.6 quote_item (line items)

CREATE TABLE IF NOT EXISTS quote_item (
    quote_item_id              SERIAL        PRIMARY KEY,
    quote_item_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    quote_id                     INTEGER       NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    line_number                   INTEGER      NOT NULL,
    inventory_item_id             INTEGER          NULL REFERENCES inventory_item(inventory_item_id),   -- NULL = free-text line
    estimate_item_id               INTEGER          NULL REFERENCES estimate_item(estimate_item_id),     -- lineage from Estimate conversion

    -- Snapshots (frozen at add/conversion time -- never re-read from catalog)
    item_name                      VARCHAR(150)  NOT NULL DEFAULT '',
    sku                              VARCHAR(50)   NOT NULL DEFAULT '',
    description                      TEXT          NOT NULL DEFAULT '',
    unit_id                           INTEGER          NULL REFERENCES lkp_unit(unit_id),
    unit_code                         VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                          DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                        DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent                  DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                        INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                        DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal                      DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                       DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                            DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at                       TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by                       INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at                       TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at                        TIMESTAMP        NULL,
    item_record_version                    INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_quote_item_uuid UNIQUE (quote_item_uuid),
    CONSTRAINT chk_qi_qty         CHECK (quantity >= 0),
    CONSTRAINT chk_qi_unit_price  CHECK (unit_price >= 0),
    CONSTRAINT chk_qi_discount    CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_qi_tax         CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- 5.7 quote_history

CREATE TABLE IF NOT EXISTS quote_history (
    quote_history_id         SERIAL       PRIMARY KEY,
    quote_id                   INTEGER      NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                         VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | update | approve
    actor_employee_id               INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                         JSONB        NOT NULL DEFAULT '{}',
    at                                TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 5.8 quote_approver / quote_approval (AD-8)

CREATE TABLE IF NOT EXISTS quote_approver (
    quote_approver_id       SERIAL      PRIMARY KEY,
    record_type_id           INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = QUOT
    record_status_id         INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                 BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_quote_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS quote_approval (
    quote_approval_id       SERIAL      PRIMARY KEY,
    quote_id                  INTEGER     NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_quote_approval UNIQUE (quote_id, record_status_id, approver_employee_id)
);

-- 5.9 quote_conversion (AD-6 -- Quote -> Sales Order lineage)

CREATE TABLE IF NOT EXISTS quote_conversion (
    quote_conversion_id      SERIAL       PRIMARY KEY,
    quote_id                   INTEGER      NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    sales_order_id              INTEGER      NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    converted_at                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    converted_by                  INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                       JSONB        NOT NULL DEFAULT '{}',  -- lightweight {quoteItemId: salesOrderItemId} line mapping for audit

    CONSTRAINT uq_quote_conversion_sales_order UNIQUE (sales_order_id)
);

-- 5.10 Indexes

-- estimate (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_est_customer      ON estimate (estimate_customer_id)  WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_status        ON estimate (estimate_status)       WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_date          ON estimate (estimate_date)         WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_sales_rep     ON estimate (estimate_sales_rep_id) WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_owner         ON estimate (estimate_owner_id)     WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_created_id    ON estimate (estimate_created_at, estimate_id)     WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_updated_id    ON estimate (estimate_updated_at, estimate_id)     WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_validuntil_id ON estimate (estimate_valid_until, estimate_id)    WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_grandtotal_id ON estimate (estimate_grand_total, estimate_id)    WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_custom_gin    ON estimate USING GIN (estimate_custom_fields);

CREATE INDEX IF NOT EXISTS idx_esti_estimate ON estimate_item (estimate_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_esti_item     ON estimate_item (inventory_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_esti_line_active
    ON estimate_item (estimate_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_est_history_estimate ON estimate_history (estimate_id);

CREATE INDEX IF NOT EXISTS idx_estimate_approver_lookup
    ON estimate_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_estimate_approval_estimate ON estimate_approval (estimate_id);

-- quote (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_quo_customer      ON quote (quote_customer_id)  WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_estimate       ON quote (quote_estimate_id)  WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_status         ON quote (quote_status)       WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_date           ON quote (quote_date)         WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_sales_rep      ON quote (quote_sales_rep_id) WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_owner          ON quote (quote_owner_id)     WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_created_id     ON quote (quote_created_at, quote_id)     WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_updated_id     ON quote (quote_updated_at, quote_id)     WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_validuntil_id  ON quote (quote_valid_until, quote_id)    WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_grandtotal_id  ON quote (quote_grand_total, quote_id)    WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_custom_gin     ON quote USING GIN (quote_custom_fields);

CREATE INDEX IF NOT EXISTS idx_qi_quote     ON quote_item (quote_id)        WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_qi_item      ON quote_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_qi_est_item  ON quote_item (estimate_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_qi_line_active
    ON quote_item (quote_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_quo_history_quote ON quote_history (quote_id);

CREATE INDEX IF NOT EXISTS idx_quote_approver_lookup
    ON quote_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_quote_approval_quote ON quote_approval (quote_id);

CREATE INDEX IF NOT EXISTS idx_quote_conversion_quote ON quote_conversion (quote_id);
-- -- Payments module ----------------------------------------------------

CREATE TABLE IF NOT EXISTS lkp_payment_method (
    payment_method_id          SERIAL       PRIMARY KEY,
    payment_method_name        VARCHAR(50)  NOT NULL,
    payment_method_code        VARCHAR(10)  NOT NULL,
    payment_method_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    payment_method_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    payment_method_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_method_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    payment_method_deleted_at  TIMESTAMP        NULL,
    payment_method_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    payment_method_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_payment_method_code UNIQUE (payment_method_code)
);

INSERT INTO lkp_payment_method (payment_method_name, payment_method_code, payment_method_is_active, payment_method_is_system, payment_method_created_by) VALUES
    ('Check',       'CHK_', TRUE, TRUE, 1),
    ('Cash',        'CASH', TRUE, TRUE, 1),
    ('Credit Card', 'CC__', TRUE, TRUE, 1),
    ('ACH',         'ACH_', TRUE, TRUE, 1),
    ('Wire',        'WIRE', TRUE, TRUE, 1),
    ('Other',       'OTHR', TRUE, TRUE, 1)
ON CONFLICT (payment_method_code) DO NOTHING;

CREATE TABLE IF NOT EXISTS payment (
    payment_id                  SERIAL        PRIMARY KEY,
    payment_uuid                 UUID          NOT NULL DEFAULT gen_random_uuid(),
    payment_number                VARCHAR(20)      NULL,

    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    payment_status                 INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    payment_customer_id            INTEGER       NOT NULL REFERENCES customer(customer_id),

    payment_method                  INTEGER       NOT NULL REFERENCES lkp_payment_method(payment_method_id),
    payment_reference_number        VARCHAR(50)   NOT NULL DEFAULT '',
    payment_date                     DATE          NOT NULL DEFAULT CURRENT_DATE,
    payment_currency                 INTEGER           NULL REFERENCES lkp_currency(currency_id),
    payment_memo                      TEXT          NOT NULL DEFAULT '',
    payment_internal_notes            TEXT          NOT NULL DEFAULT '',

    payment_amount                     DECIMAL(15,2) NOT NULL,
    payment_applied_total               DECIMAL(15,2) NOT NULL DEFAULT 0,
    payment_unapplied_amount             DECIMAL(15,2) NOT NULL DEFAULT 0,

    payment_owner_id                      INTEGER           NULL REFERENCES employee(employee_id),

    payment_custom_fields                  JSONB        NOT NULL DEFAULT '{}',
    payment_created_at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_created_by                       INTEGER          NULL REFERENCES employee(employee_id),
    payment_updated_at                        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_updated_by                         INTEGER          NULL REFERENCES employee(employee_id),
    payment_deleted_at                          TIMESTAMP        NULL,
    payment_deleted_by                           INTEGER          NULL REFERENCES employee(employee_id),
    payment_record_version                        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_payment_uuid       UNIQUE (payment_uuid),
    CONSTRAINT uq_payment_number     UNIQUE (payment_number),
    CONSTRAINT chk_payment_amount_pos      CHECK (payment_amount > 0),
    CONSTRAINT chk_payment_applied_nonneg  CHECK (payment_applied_total >= 0 AND payment_unapplied_amount >= 0),
    CONSTRAINT chk_payment_applied_le_amt  CHECK (payment_applied_total <= payment_amount),
    CONSTRAINT chk_payment_soft_delete     CHECK (
        (payment_deleted_at IS NULL AND payment_deleted_by IS NULL) OR
        (payment_deleted_at IS NOT NULL AND payment_deleted_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS payment_application (
    application_id             SERIAL        PRIMARY KEY,
    application_uuid            UUID          NOT NULL DEFAULT gen_random_uuid(),
    payment_id                   INTEGER       NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    invoice_id                    INTEGER       NOT NULL REFERENCES invoice(invoice_id),

    application_amount             DECIMAL(15,2) NOT NULL,

    application_created_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by           INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at            TIMESTAMP        NULL,
    application_deleted_by             INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version          INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_payment_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_pay_app_amount_pos      CHECK (application_amount > 0),
    CONSTRAINT chk_pay_app_soft_delete     CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_app_live_pair
    ON payment_application (payment_id, invoice_id) WHERE application_deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS payment_history (
    payment_history_id        SERIAL       PRIMARY KEY,
    payment_id                 INTEGER      NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pay_customer      ON payment (payment_customer_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_status         ON payment (payment_status)      WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_date            ON payment (payment_date)        WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_owner            ON payment (payment_owner_id)    WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_created_id      ON payment (payment_created_at, payment_id)  WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_updated_id      ON payment (payment_updated_at, payment_id)  WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_date_id          ON payment (payment_date, payment_id)         WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_amount_id         ON payment (payment_amount, payment_id)       WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_unapplied_id       ON payment (payment_unapplied_amount, payment_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_status_created       ON payment (payment_status, payment_created_at, payment_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_custom_gin            ON payment USING GIN (payment_custom_fields);

CREATE INDEX IF NOT EXISTS idx_pay_app_payment  ON payment_application (payment_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_app_invoice  ON payment_application (invoice_id) WHERE application_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_pay_history_payment ON payment_history (payment_id);
-- -- 000030_vendor_module -----------------------------------------------
-- =====================================================================
-- Tenant migration 030: Vendor module -- a dedicated relational sibling of
-- `customer`/`sales_order` (not the generic v1 JSONB workflow engine; the
-- pre-existing `workflows` row keyed 'vendor' from migration 010 is an
-- unrelated legacy JSONB placeholder -- see the identical note on
-- salesorder.Create). Modeled on schema.org/Person INTERSECT schema.org/Organization:
-- vendor_type discriminates which field group is authoritative. record_type
-- VNDR and its Active/Inactive lkp_record_status rows already exist (migration
-- 002); this adds an On Hold status alongside them.
-- =====================================================================

INSERT INTO lkp_record_status (record_status_code, record_status_name, record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT 'ONHD', 'On Hold', record_type_id, TRUE, TRUE, 1
FROM lkp_record_type WHERE record_type_code = 'VNDR'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

CREATE TABLE IF NOT EXISTS vendor (
    vendor_id                      SERIAL        PRIMARY KEY,
    vendor_uuid                    UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_number                  VARCHAR(20)       NULL,  -- 'VNDR-000001', generated post-insert in Go

    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VNDR
    vendor_status                  INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),
    vendor_type                    VARCHAR(20)   NOT NULL DEFAULT 'Organization', -- schema.org @type: Person | Organization

    -- Ownership (IDOR scope; team scope collapses to own -- mirrors sales_order,
    -- which has no team column either)
    vendor_owner_id                INTEGER           NULL REFERENCES employee(employee_id),

    -- Shared (schema.org properties common to Person and Organization)
    vendor_email                   VARCHAR(100)  NOT NULL DEFAULT '',
    vendor_physical_address        TEXT          NOT NULL DEFAULT '',
    vendor_fax_number              VARCHAR(20)   NOT NULL DEFAULT '',
    vendor_global_location_number  VARCHAR(20)   NOT NULL DEFAULT '',  -- schema.org globalLocationNumber
    vendor_isic_v4_code            VARCHAR(20)   NOT NULL DEFAULT '',  -- schema.org isicV4
    vendor_associated_brands       JSONB         NOT NULL DEFAULT '[]', -- string[] (schema.org brand)
    vendor_awards_won              VARCHAR(255)  NOT NULL DEFAULT '',  -- schema.org award
    vendor_contact_point           JSONB         NOT NULL DEFAULT '{}', -- schema.org ContactPoint {contactType,telephone,email}
    vendor_funder                  VARCHAR(150)  NOT NULL DEFAULT '',  -- schema.org funder
    vendor_offer_catalog_url       VARCHAR(255)  NOT NULL DEFAULT '',  -- schema.org hasOfferCatalog
    vendor_point_of_sale_locations VARCHAR(255)  NOT NULL DEFAULT '',  -- schema.org hasPOS

    -- schema.org/Person -- authoritative when vendor_type = 'Person'
    vendor_honorific_prefix        VARCHAR(20)   NOT NULL DEFAULT '',
    vendor_given_name              VARCHAR(75)   NOT NULL DEFAULT '',
    vendor_additional_name         VARCHAR(75)   NOT NULL DEFAULT '',
    vendor_family_name             VARCHAR(75)   NOT NULL DEFAULT '',
    vendor_honorific_suffix        VARCHAR(20)   NOT NULL DEFAULT '',
    vendor_job_title               VARCHAR(100)  NOT NULL DEFAULT '',
    vendor_gender                  VARCHAR(30)   NOT NULL DEFAULT '',
    vendor_nationality_country_id  INTEGER           NULL REFERENCES lkp_country(country_id),
    vendor_height                  VARCHAR(30)   NOT NULL DEFAULT '',
    vendor_net_worth               VARCHAR(50)   NOT NULL DEFAULT '',

    -- schema.org/Organization -- authoritative when vendor_type = 'Organization'
    vendor_legal_name              VARCHAR(150)  NOT NULL DEFAULT '',
    vendor_registration_info       TEXT          NOT NULL DEFAULT '',
    vendor_duns_number             VARCHAR(20)   NOT NULL DEFAULT '',
    vendor_founding_date           DATE              NULL,
    vendor_founding_location       VARCHAR(150)  NOT NULL DEFAULT '',
    vendor_dissolution_date        DATE              NULL,
    vendor_department              VARCHAR(100)  NOT NULL DEFAULT '',
    vendor_accepted_payment_methods JSONB        NOT NULL DEFAULT '[]', -- string[]
    vendor_compliance_policies     JSONB         NOT NULL DEFAULT '{}', -- {ethicsPolicyUrl,diversityPolicyUrl,correctionsPolicyUrl,actionableFeedbackPolicyUrl}

    -- Lineage + audit (mirrors sales_order tail)
    vendor_created_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_created_by              INTEGER           NULL REFERENCES employee(employee_id),
    vendor_updated_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_updated_by              INTEGER           NULL REFERENCES employee(employee_id),
    vendor_deleted_at              TIMESTAMP         NULL,
    vendor_deleted_by              INTEGER           NULL REFERENCES employee(employee_id),
    vendor_record_version          INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_uuid          UNIQUE (vendor_uuid),
    CONSTRAINT uq_vendor_number        UNIQUE (vendor_number),
    CONSTRAINT chk_vendor_type         CHECK (vendor_type IN ('Person','Organization')),
    CONSTRAINT chk_vendor_person_names CHECK (
        vendor_type <> 'Person' OR (vendor_given_name <> '' AND vendor_family_name <> '')
    ),
    CONSTRAINT chk_vendor_org_legal_name CHECK (
        vendor_type <> 'Organization' OR vendor_legal_name <> ''
    ),
    CONSTRAINT chk_vendor_soft_delete CHECK (
        (vendor_deleted_at IS NULL AND vendor_deleted_by IS NULL) OR
        (vendor_deleted_at IS NOT NULL AND vendor_deleted_by IS NOT NULL)
    )
);

-- vendor_history -- status trail (mirrors sales_order_history, no approval)
CREATE TABLE IF NOT EXISTS vendor_history (
    vendor_history_id  SERIAL       PRIMARY KEY,
    vendor_id           INTEGER      NOT NULL REFERENCES vendor(vendor_id) ON DELETE CASCADE,
    from_status_id       INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id         INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | update
    actor_employee_id     INTEGER          NULL REFERENCES employee(employee_id),
    snapshot              JSONB        NOT NULL DEFAULT '{}',
    at                    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes -- listing/filtering (all partial on live rows) -------------------
CREATE INDEX IF NOT EXISTS idx_vendor_status      ON vendor (vendor_status)      WHERE vendor_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vendor_owner       ON vendor (vendor_owner_id)    WHERE vendor_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vendor_type        ON vendor (vendor_type)       WHERE vendor_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vendor_created_id  ON vendor (vendor_created_at, vendor_id) WHERE vendor_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vendor_updated_id  ON vendor (vendor_updated_at, vendor_id) WHERE vendor_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vendor_custom_gin  ON vendor USING GIN (vendor_associated_brands);
CREATE INDEX IF NOT EXISTS idx_vendor_history_vendor ON vendor_history (vendor_id);


-- -- 000031_credit_memo_module -----------------------------------------------
-- =====================================================================
-- Tenant migration 031: Credit Memo module -- a dedicated relational sibling
-- of `invoice`/`payment` (not the generic v1 JSONB workflow engine; the
-- pre-existing `workflows` row keyed 'credit_memo' from migration 010 is an
-- unrelated legacy JSONB placeholder, left in place unused -- see the
-- identical note on sales_order above).
--
-- A credit memo is credit issued to a customer (returned goods, overbilling,
-- negotiated adjustment) which is applied against invoices to reduce what they
-- owe. It is invoice-shaped (header + lines, AD-3) with payment's
-- applied/unapplied rollup grafted on, and it moves money only through the
-- credit_memo_application ledger (AD-6).
--
-- record_type CRDT (id 9) and its DRFT/APPV/APPL/VOID statuses already exist
-- (migration 002) -- this block adds NO seed rows. The lkp_record_status seed
-- keys statuses to record types by hardcoded integer, so it is append-only.
--
-- Spec: docs/superpowers/specs/2026-07-15-credit-memo-module-design.md
-- =====================================================================

-- Invoice AR rollup gains a third component (AD-4). `invoice_amount_paid`
-- keeps meaning CASH; credit applied against an invoice accumulates here
-- instead, so AR aging and "how much did we actually collect?" stay
-- answerable from the invoice row.
--   invoice_balance_due = grand_total - amount_paid - credit_total
-- Sole writer: invoice.RecomputeBalance (invoice/balance.go, AD-5).
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS invoice_credit_total DECIMAL(15,2) NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS credit_memo (
    credit_memo_id              SERIAL        PRIMARY KEY,
    credit_memo_uuid            UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id              INTEGER           NULL,  -- platform owner stamp, no cross-DB FK
    credit_memo_number          VARCHAR(20)       NULL,

    -- Classification
    record_type                 INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    credit_memo_status          INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Source linkage. Both are LINEAGE ONLY (AD-2) -- they carry no money
    -- semantics. credit_memo_application is the only thing that moves balance.
    -- A goodwill credit has neither.
    credit_memo_customer_id     INTEGER       NOT NULL REFERENCES customer(customer_id),
    credit_memo_invoice_id      INTEGER           NULL REFERENCES invoice(invoice_id) ON DELETE SET NULL,
    credit_memo_sales_order_id  INTEGER           NULL REFERENCES sales_order(sales_order_id) ON DELETE SET NULL,

    -- Primary info
    credit_memo_reference_number VARCHAR(50)  NOT NULL DEFAULT '',
    credit_memo_date             DATE         NOT NULL DEFAULT CURRENT_DATE,
    credit_memo_reason           VARCHAR(150) NOT NULL DEFAULT '',
    credit_memo_sales_tax_percent DECIMAL(6,4) NOT NULL DEFAULT 0,
    credit_memo_memo             TEXT         NOT NULL DEFAULT '',
    credit_memo_notes            TEXT         NOT NULL DEFAULT '',
    credit_memo_internal_notes   TEXT         NOT NULL DEFAULT '',

    -- Sales assignment
    credit_memo_sales_rep_id     INTEGER          NULL REFERENCES employee(employee_id),
    credit_memo_owner_id         INTEGER          NULL REFERENCES employee(employee_id),

    -- Pricing / currency. Display only -- no conversion is performed (AD-17).
    credit_memo_price_level      INTEGER          NULL REFERENCES lkp_price_level(price_level_id),
    credit_memo_currency         INTEGER          NULL REFERENCES lkp_currency(currency_id),
    credit_memo_exchange_rate    DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored, not recomputed on read)
    credit_memo_subtotal         DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit_memo_discount_total   DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit_memo_tax_total        DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit_memo_adjustment       DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit_memo_grand_total      DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Application rollup (stored; derived from credit_memo_application, AD-6)
    credit_memo_applied_total    DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit_memo_unapplied_amount DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot (copied from customer at create -- never re-read)
    credit_memo_bill_customer_name VARCHAR(150) NOT NULL DEFAULT '',
    credit_memo_bill_attention     VARCHAR(150) NOT NULL DEFAULT '',
    credit_memo_bill_addr_line1    VARCHAR(100) NOT NULL DEFAULT '',
    credit_memo_bill_addr_line2    VARCHAR(100) NOT NULL DEFAULT '',
    credit_memo_bill_addr_suitenum VARCHAR(20)  NOT NULL DEFAULT '',
    credit_memo_bill_addr_city     VARCHAR(100) NOT NULL DEFAULT '',
    credit_memo_bill_addr_state    INTEGER          NULL REFERENCES lkp_state(state_id),
    credit_memo_bill_addr_zip      VARCHAR(10)  NOT NULL DEFAULT '',
    credit_memo_bill_addr_country  INTEGER          NULL REFERENCES lkp_country(country_id),
    credit_memo_bill_phone         VARCHAR(20)  NOT NULL DEFAULT '',
    credit_memo_bill_fax           VARCHAR(20)  NOT NULL DEFAULT '',
    credit_memo_bill_email         VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + lineage + audit
    credit_memo_custom_fields    JSONB        NOT NULL DEFAULT '{}',
    credit_memo_parent_id        INTEGER          NULL REFERENCES credit_memo(credit_memo_id),
    credit_memo_created_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    credit_memo_created_by       INTEGER          NULL REFERENCES employee(employee_id),
    credit_memo_updated_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    credit_memo_updated_by       INTEGER          NULL REFERENCES employee(employee_id),
    credit_memo_deleted_at       TIMESTAMP        NULL,
    credit_memo_deleted_by       INTEGER          NULL REFERENCES employee(employee_id),
    credit_memo_record_version   INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_credit_memo_uuid       UNIQUE (credit_memo_uuid),
    CONSTRAINT uq_credit_memo_number     UNIQUE (credit_memo_number),
    CONSTRAINT chk_cm_tax_percent        CHECK (credit_memo_sales_tax_percent >= 0 AND credit_memo_sales_tax_percent <= 100),
    CONSTRAINT chk_cm_totals_nonneg      CHECK (credit_memo_subtotal >= 0 AND credit_memo_grand_total >= 0),
    CONSTRAINT chk_cm_applied_nonneg     CHECK (credit_memo_applied_total >= 0 AND credit_memo_unapplied_amount >= 0),
    CONSTRAINT chk_cm_applied_le_total   CHECK (credit_memo_applied_total <= credit_memo_grand_total),
    CONSTRAINT chk_cm_soft_delete        CHECK (
        (credit_memo_deleted_at IS NULL AND credit_memo_deleted_by IS NULL) OR
        (credit_memo_deleted_at IS NOT NULL AND credit_memo_deleted_by IS NOT NULL)
    )
);

-- credit_memo_item -- mirrors invoice_item, including its asymmetry with the
-- header: no item_deleted_by and no item_updated_by. inventory_item_id records
-- WHAT was credited; nothing decrements stock (AD-11 -- this repo has no
-- inventory write path at all).
CREATE TABLE IF NOT EXISTS credit_memo_item (
    credit_memo_item_id      SERIAL        PRIMARY KEY,
    credit_memo_item_uuid    UUID          NOT NULL DEFAULT gen_random_uuid(),
    credit_memo_id            INTEGER       NOT NULL REFERENCES credit_memo(credit_memo_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),
    invoice_item_id           INTEGER           NULL REFERENCES invoice_item(invoice_item_id) ON DELETE SET NULL,

    -- Snapshots (frozen at add time -- never re-read from catalog)
    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description               TEXT          NOT NULL DEFAULT '',
    unit_id                   INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                  DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent          DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id               INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent               DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by           INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at           TIMESTAMP         NULL,
    item_record_version       INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_credit_memo_item_uuid UNIQUE (credit_memo_item_uuid),
    CONSTRAINT chk_cmi_qty          CHECK (quantity >= 0),
    CONSTRAINT chk_cmi_unit_price   CHECK (unit_price >= 0),
    CONSTRAINT chk_cmi_discount     CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_cmi_tax          CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- credit_memo_application -- the ledger of record (AD-6). Mirrors
-- payment_application. Cannot reuse that table: its payment_id is NOT NULL
-- REFERENCES payment, so a credit would need a fabricated payment row, which
-- would corrupt invoice_amount_paid's "cash" meaning (AD-4).
-- invoice_id is deliberately NOT ON DELETE CASCADE -- an invoice must not be
-- hard-deletable out from under a live credit application.
CREATE TABLE IF NOT EXISTS credit_memo_application (
    application_id             SERIAL        PRIMARY KEY,
    application_uuid           UUID          NOT NULL DEFAULT gen_random_uuid(),
    credit_memo_id             INTEGER       NOT NULL REFERENCES credit_memo(credit_memo_id) ON DELETE CASCADE,
    invoice_id                 INTEGER       NOT NULL REFERENCES invoice(invoice_id),

    application_amount         DECIMAL(15,2) NOT NULL,

    application_created_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by     INTEGER           NULL REFERENCES employee(employee_id),
    application_deleted_at     TIMESTAMP         NULL,
    application_deleted_by     INTEGER           NULL REFERENCES employee(employee_id),
    application_record_version INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_credit_memo_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_cm_app_amount_pos           CHECK (application_amount > 0),
    CONSTRAINT chk_cm_app_soft_delete          CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- credit_memo_history -- typed status trail, written INSIDE the mutation
-- transaction (unlike audit_logs, written outside it from the controller).
CREATE TABLE IF NOT EXISTS credit_memo_history (
    credit_memo_history_id  SERIAL       PRIMARY KEY,
    credit_memo_id          INTEGER      NOT NULL REFERENCES credit_memo(credit_memo_id) ON DELETE CASCADE,
    from_status_id          INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id            INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                  VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | update | transition | apply | unapply
    actor_employee_id       INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                JSONB        NOT NULL DEFAULT '{}',
    at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes -- listing/filtering (all partial on live rows) -------------------
CREATE INDEX IF NOT EXISTS idx_cm_customer      ON credit_memo (credit_memo_customer_id)    WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_invoice       ON credit_memo (credit_memo_invoice_id)     WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_sales_order   ON credit_memo (credit_memo_sales_order_id) WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_status        ON credit_memo (credit_memo_status)         WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_date          ON credit_memo (credit_memo_date)           WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_sales_rep     ON credit_memo (credit_memo_sales_rep_id)   WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_owner         ON credit_memo (credit_memo_owner_id)       WHERE credit_memo_deleted_at IS NULL;
-- Keyset pagination tiebreakers (one per sortable column + id)
CREATE INDEX IF NOT EXISTS idx_cm_created_id    ON credit_memo (credit_memo_created_at, credit_memo_id)       WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_updated_id    ON credit_memo (credit_memo_updated_at, credit_memo_id)       WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_date_id       ON credit_memo (credit_memo_date, credit_memo_id)             WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_total_id      ON credit_memo (credit_memo_grand_total, credit_memo_id)      WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_unapplied_id  ON credit_memo (credit_memo_unapplied_amount, credit_memo_id) WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_status_created ON credit_memo (credit_memo_status, credit_memo_created_at, credit_memo_id) WHERE credit_memo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_custom_gin    ON credit_memo USING GIN (credit_memo_custom_fields);

-- credit_memo_item
-- Line numbers are unique per memo among LIVE rows only, so Update can
-- soft-delete a line and re-insert the same line_number (mirrors uq_ii_line_active).
CREATE UNIQUE INDEX IF NOT EXISTS uq_cmi_line_active
    ON credit_memo_item (credit_memo_id, line_number) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cmi_memo       ON credit_memo_item (credit_memo_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cmi_item       ON credit_memo_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_cmi_inv_item   ON credit_memo_item (invoice_item_id);

-- credit_memo_application -- one live row per (memo, invoice) pair, so a
-- re-apply increments the existing row rather than inserting a second
-- (mirrors uq_pay_app_live_pair).
CREATE UNIQUE INDEX IF NOT EXISTS uq_cm_app_live_pair
    ON credit_memo_application (credit_memo_id, invoice_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_app_memo    ON credit_memo_application (credit_memo_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_app_invoice ON credit_memo_application (invoice_id)     WHERE application_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_cm_history_memo ON credit_memo_history (credit_memo_id);

-- Invoice's new credit rollup column (sortable via the invoice resolver).
CREATE INDEX IF NOT EXISTS idx_inv_credit_id  ON invoice (invoice_credit_total, invoice_id) WHERE invoice_deleted_at IS NULL;
-- -- 000032_refund_module -----------------------------------------------
-- =====================================================================
-- Tenant migration 032: Refund module -- a dedicated relational sibling of
-- `payment`/`credit_memo` (not the generic v1 JSONB workflow engine).
--
-- A refund is money returned to a customer, drawn from either an overpayment
-- held on a payment (payment_unapplied_amount) or an unapplied credit memo
-- (credit_memo_unapplied_amount). It is payment-shaped (scalar amount, no
-- lines -- AD-1) and moves money only through the refund_application ledger,
-- which targets exactly one of payment or credit_memo per row (AD-2).
--
-- record_type RFND (id 10) and its PEND/APPV/SENT/VOID statuses already
-- exist (migration 002) -- this block adds NO seed rows. The lkp_record_status
-- seed keys statuses to record types by hardcoded integer, so it is
-- append-only.
--
-- This module is record-only: no payment gateway, no inbound webhooks, no
-- gateway-log table. None of that infrastructure exists anywhere in this
-- codebase (AD-10) -- a refund records money that was already returned
-- out-of-band, exactly like payment records money already collected.
--
-- Spec: docs/superpowers/specs/2026-07-16-refund-module-design.md
-- =====================================================================

-- payment/credit_memo each gain one rollup column, sole writer is refund/
-- (AD-2). Neither payment's nor credit_memo's own Go code ever reads or
-- writes these -- there is exactly one writer, so no shared invariant needs
-- extracting the way invoice.RecomputeBalance was for invoice_credit_total.
--   available_from_payment     = payment_unapplied_amount     - payment_refunded_total
--   available_from_credit_memo = credit_memo_unapplied_amount - credit_memo_refunded_total
ALTER TABLE payment     ADD COLUMN IF NOT EXISTS payment_refunded_total     DECIMAL(15,2) NOT NULL DEFAULT 0;
ALTER TABLE credit_memo ADD COLUMN IF NOT EXISTS credit_memo_refunded_total DECIMAL(15,2) NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS refund (
    refund_id                  SERIAL        PRIMARY KEY,
    refund_uuid                UUID          NOT NULL DEFAULT gen_random_uuid(),
    refund_number              VARCHAR(20)       NULL,

    -- Classification
    record_type                INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    refund_status               INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    refund_customer_id          INTEGER       NOT NULL REFERENCES customer(customer_id),

    -- Source linkage. LINEAGE ONLY (AD-12 for invoice; the typical/primary
    -- source for payment/credit_memo) -- refund_application is the only thing
    -- that moves balance.
    refund_payment_id            INTEGER          NULL REFERENCES payment(payment_id) ON DELETE SET NULL,
    refund_credit_memo_id         INTEGER          NULL REFERENCES credit_memo(credit_memo_id) ON DELETE SET NULL,
    refund_invoice_id              INTEGER          NULL REFERENCES invoice(invoice_id) ON DELETE SET NULL,

    -- Primary info
    refund_method                 INTEGER       NOT NULL REFERENCES lkp_payment_method(payment_method_id),
    refund_reference_number        VARCHAR(50)   NOT NULL DEFAULT '',
    refund_date                      DATE          NOT NULL DEFAULT CURRENT_DATE,
    refund_currency                   INTEGER          NULL REFERENCES lkp_currency(currency_id),
    refund_reason                      VARCHAR(150)  NOT NULL DEFAULT '',
    refund_memo                         TEXT          NOT NULL DEFAULT '',
    refund_internal_notes                 TEXT          NOT NULL DEFAULT '',

    -- Money summary (stored, not recomputed on read)
    refund_amount                          DECIMAL(15,2) NOT NULL,
    refund_applied_total                    DECIMAL(15,2) NOT NULL DEFAULT 0,
    refund_unapplied_amount                  DECIMAL(15,2) NOT NULL DEFAULT 0,

    refund_owner_id                            INTEGER          NULL REFERENCES employee(employee_id),

    refund_custom_fields                        JSONB        NOT NULL DEFAULT '{}',
    refund_created_at                            TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    refund_created_by                             INTEGER          NULL REFERENCES employee(employee_id),
    refund_updated_at                              TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    refund_updated_by                               INTEGER          NULL REFERENCES employee(employee_id),
    refund_deleted_at                                TIMESTAMP        NULL,
    refund_deleted_by                                 INTEGER          NULL REFERENCES employee(employee_id),
    refund_record_version                              INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_refund_uuid           UNIQUE (refund_uuid),
    CONSTRAINT uq_refund_number         UNIQUE (refund_number),
    CONSTRAINT chk_refund_amount_pos    CHECK (refund_amount > 0),
    CONSTRAINT chk_refund_applied_nonneg CHECK (refund_applied_total >= 0 AND refund_unapplied_amount >= 0),
    CONSTRAINT chk_refund_applied_le_amt CHECK (refund_applied_total <= refund_amount),
    CONSTRAINT chk_refund_soft_delete    CHECK (
        (refund_deleted_at IS NULL AND refund_deleted_by IS NULL) OR
        (refund_deleted_at IS NOT NULL AND refund_deleted_by IS NOT NULL)
    )
);

-- refund_application -- the ledger of record (AD-2). Targets exactly one of
-- payment or credit_memo per row (chk_refund_app_xor_source). Cannot reuse
-- payment_application / credit_memo_application: both have a NOT NULL FK to
-- their single target type. payment_id/credit_memo_id are deliberately NOT
-- ON DELETE CASCADE -- a source must not be hard-deletable out from under a
-- live refund application.
CREATE TABLE IF NOT EXISTS refund_application (
    application_id             SERIAL        PRIMARY KEY,
    application_uuid           UUID          NOT NULL DEFAULT gen_random_uuid(),
    refund_id                  INTEGER       NOT NULL REFERENCES refund(refund_id) ON DELETE CASCADE,
    payment_id                 INTEGER           NULL REFERENCES payment(payment_id),
    credit_memo_id             INTEGER           NULL REFERENCES credit_memo(credit_memo_id),

    application_amount         DECIMAL(15,2) NOT NULL,

    application_created_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by     INTEGER           NULL REFERENCES employee(employee_id),
    application_deleted_at     TIMESTAMP         NULL,
    application_deleted_by     INTEGER           NULL REFERENCES employee(employee_id),
    application_record_version INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_refund_application_uuid  UNIQUE (application_uuid),
    CONSTRAINT chk_refund_app_amount_pos   CHECK (application_amount > 0),
    CONSTRAINT chk_refund_app_xor_source   CHECK (
        (payment_id IS NOT NULL AND credit_memo_id IS NULL) OR
        (payment_id IS NULL AND credit_memo_id IS NOT NULL)
    ),
    CONSTRAINT chk_refund_app_soft_delete  CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- One live application per (refund, payment) or (refund, credit_memo) pair,
-- so a re-apply increments the existing row rather than inserting a second
-- (mirrors uq_pay_app_live_pair / uq_cm_app_live_pair). COALESCE(...,0) lets a
-- single partial unique index cover both source columns despite the XOR.
CREATE UNIQUE INDEX IF NOT EXISTS uq_refund_app_live_pair
    ON refund_application (refund_id, COALESCE(payment_id, 0), COALESCE(credit_memo_id, 0))
    WHERE application_deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS refund_history (
    refund_history_id       SERIAL       PRIMARY KEY,
    refund_id                INTEGER      NOT NULL REFERENCES refund(refund_id) ON DELETE CASCADE,
    from_status_id             INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                 INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                         VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | update | transition | apply | unapply
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                           JSONB        NOT NULL DEFAULT '{}',
    at                                  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes -- listing/filtering (all partial on live rows) -------------------
CREATE INDEX IF NOT EXISTS idx_rfnd_customer     ON refund (refund_customer_id)     WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_payment      ON refund (refund_payment_id)      WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_credit_memo  ON refund (refund_credit_memo_id)  WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_invoice      ON refund (refund_invoice_id)      WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_status       ON refund (refund_status)          WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_date         ON refund (refund_date)            WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_owner        ON refund (refund_owner_id)        WHERE refund_deleted_at IS NULL;
-- Keyset pagination tiebreakers (one per sortable column + id)
CREATE INDEX IF NOT EXISTS idx_rfnd_created_id   ON refund (refund_created_at, refund_id)      WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_updated_id   ON refund (refund_updated_at, refund_id)      WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_date_id      ON refund (refund_date, refund_id)            WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_amount_id    ON refund (refund_amount, refund_id)          WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_unapplied_id ON refund (refund_unapplied_amount, refund_id) WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_status_created ON refund (refund_status, refund_created_at, refund_id) WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_custom_gin   ON refund USING GIN (refund_custom_fields);

CREATE INDEX IF NOT EXISTS idx_rfnd_app_refund      ON refund_application (refund_id)      WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_app_payment     ON refund_application (payment_id)     WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rfnd_app_credit_memo ON refund_application (credit_memo_id) WHERE application_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_rfnd_history_refund ON refund_history (refund_id);

-- New rollup columns on payment/credit_memo (sortable via each module's own resolver).
CREATE INDEX IF NOT EXISTS idx_pay_refunded_id ON payment     (payment_refunded_total, payment_id)         WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cm_refunded_id  ON credit_memo (credit_memo_refunded_total, credit_memo_id) WHERE credit_memo_deleted_at IS NULL;


-- -- 000033_review_hardening --------------------------------------------------
-- =====================================================================
-- Tenant-template schema -- Phase 33: SQL review hardening.
--
-- Three independent, idempotent fixes surfaced by a schema review:
--  1. Drop single-column status indexes made redundant by a composite
--     (status, created_at, id) index already covering the same lookups.
--  2. Tighten two users(id)/workflow_states(id) FKs to ON DELETE SET NULL,
--     matching the behavior every sibling FK in the file already uses.
--  3. Add CHECK constraints on *_history.action / enum-like columns that
--     were previously enforced only by a comment. Value lists were taken
--     from the actual literals each Go package writes (grepped from
--     source), not from the (in two cases stale) column comments --
--     notably rag_index_queue.status is missing 'inflight' in its
--     comment, and users.status is missing 'suspended'.
-- =====================================================================

-- 1. Redundant single-column indexes (composite index already covers these
--    via leftmost-prefix; DROP INDEX IF EXISTS is naturally idempotent).
DROP INDEX IF EXISTS idx_inv_status;
DROP INDEX IF EXISTS idx_pay_status;
DROP INDEX IF EXISTS idx_cm_status;
DROP INDEX IF EXISTS idx_rfnd_status;

-- 2. FK ON DELETE fixes for tenants provisioned before this migration.
--    (Fresh tenants already get the fixed behavior from the CREATE TABLE
--    definitions above; confdeltype != 'n' guards against re-running this
--    on a DB that's already been fixed.)
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'employee_employee_user_id_fkey' AND confdeltype != 'n'
  ) THEN
    ALTER TABLE employee DROP CONSTRAINT employee_employee_user_id_fkey;
    ALTER TABLE employee ADD CONSTRAINT employee_employee_user_id_fkey
      FOREIGN KEY (employee_user_id) REFERENCES users(id) ON DELETE SET NULL;
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'workflow_records_current_state_id_fkey' AND confdeltype != 'n'
  ) THEN
    ALTER TABLE workflow_records DROP CONSTRAINT workflow_records_current_state_id_fkey;
    ALTER TABLE workflow_records ADD CONSTRAINT workflow_records_current_state_id_fkey
      FOREIGN KEY (current_state_id) REFERENCES workflow_states(id) ON DELETE SET NULL;
  END IF;
END $$;

-- 3. CHECK constraints on previously comment-only enum columns.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_users_status') THEN
    ALTER TABLE users ADD CONSTRAINT chk_users_status
      CHECK (status IN ('active','invited','disabled','suspended'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_wf_transition_actions_type') THEN
    ALTER TABLE workflow_transition_actions ADD CONSTRAINT chk_wf_transition_actions_type
      CHECK (type IN ('send_email','assign_owner','set_field','webhook','create_record'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_rag_index_queue_op') THEN
    ALTER TABLE rag_index_queue ADD CONSTRAINT chk_rag_index_queue_op
      CHECK (op IN ('upsert','delete'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_rag_index_queue_status') THEN
    ALTER TABLE rag_index_queue ADD CONSTRAINT chk_rag_index_queue_status
      CHECK (status IN ('pending','inflight','done','error'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_crm_record_history_action') THEN
    ALTER TABLE crm_record_history ADD CONSTRAINT chk_crm_record_history_action
      CHECK (action IN ('create','transition','convert','approve'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_customer_history_action') THEN
    ALTER TABLE customer_history ADD CONSTRAINT chk_customer_history_action
      CHECK (action IN ('create','transition','convert','approve'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_sales_order_history_action') THEN
    ALTER TABLE sales_order_history ADD CONSTRAINT chk_sales_order_history_action
      CHECK (action IN ('create','transition','cancel','update','approve'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_invoice_history_action') THEN
    ALTER TABLE invoice_history ADD CONSTRAINT chk_invoice_history_action
      CHECK (action IN ('create','transition','update','payment','unapply','credit','uncredit'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_estimate_history_action') THEN
    ALTER TABLE estimate_history ADD CONSTRAINT chk_estimate_history_action
      CHECK (action IN ('create','transition','convert','update','approve'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_quote_history_action') THEN
    ALTER TABLE quote_history ADD CONSTRAINT chk_quote_history_action
      CHECK (action IN ('create','transition','convert','update','approve'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_payment_history_action') THEN
    ALTER TABLE payment_history ADD CONSTRAINT chk_payment_history_action
      CHECK (action IN ('create','apply','unapply','transition'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_credit_memo_history_action') THEN
    ALTER TABLE credit_memo_history ADD CONSTRAINT chk_credit_memo_history_action
      CHECK (action IN ('create','update','transition','apply','unapply'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_refund_history_action') THEN
    ALTER TABLE refund_history ADD CONSTRAINT chk_refund_history_action
      CHECK (action IN ('create','update','transition','apply','unapply'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_vendor_history_action') THEN
    ALTER TABLE vendor_history ADD CONSTRAINT chk_vendor_history_action
      CHECK (action IN ('create','transition','update'));
  END IF;
END $$;

-- -- 000034_sales_document_conversions ------------------------------------
-- =====================================================================
-- Tenant migration 34: document-to-document conversion chain
-- (Estimate -> Quote -> Sales Order -> Invoice), full snapshot copy.
--
-- 1. Idempotency: a source document may only convert once. Unique indexes
--    on the lineage columns/tables let Go detect "already converted" and
--    return the existing target instead of creating a duplicate.
-- 2. Widen sales_order_history / invoice_history action CHECKs to allow
--    'convert' on the source document's own history trail (mirrors
--    quote_history / estimate_history, which already allow it).
-- 3. New crm_activity table -- a thin, append-mostly module (no lifecycle,
--    no calc.go) logging calls/emails/meetings/notes/tasks against a
--    CRM customer/lead/prospect record (they share the `customer` table).
-- =====================================================================

-- 1a. Quote.quote_estimate_id: one estimate converts to at most one live
-- quote. NULLs (standalone quotes) are unaffected -- Postgres unique
-- indexes never compare NULLs as equal.
CREATE UNIQUE INDEX IF NOT EXISTS uq_quote_estimate_once
    ON quote (quote_estimate_id) WHERE quote_deleted_at IS NULL;

-- 1b. quote_conversion.quote_id: one quote converts to at most one sales
-- order (uq_quote_conversion_sales_order already guards the reverse
-- direction -- one sales order traces back to at most one quote).
CREATE UNIQUE INDEX IF NOT EXISTS uq_quote_conversion_quote
    ON quote_conversion (quote_id);

-- 1c. invoice.invoice_sales_order_id: one sales order converts to at most
-- one live invoice.
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_sales_order_once
    ON invoice (invoice_sales_order_id) WHERE invoice_deleted_at IS NULL;

-- 2. Widen history action CHECKs so the source document's history row can
-- record 'convert' (widening-only; existing rows remain valid).
ALTER TABLE sales_order_history DROP CONSTRAINT IF EXISTS chk_sales_order_history_action;
ALTER TABLE sales_order_history ADD CONSTRAINT chk_sales_order_history_action
    CHECK (action IN ('create','transition','cancel','update','approve','convert'));

ALTER TABLE invoice_history DROP CONSTRAINT IF EXISTS chk_invoice_history_action;
ALTER TABLE invoice_history ADD CONSTRAINT chk_invoice_history_action
    CHECK (action IN ('create','transition','update','payment','unapply','credit','uncredit','convert'));

-- 3. CRM activity log (call | email | meeting | note | task).
CREATE TABLE IF NOT EXISTS crm_activity (
    crm_activity_id       SERIAL       PRIMARY KEY,
    crm_activity_uuid     UUID         NOT NULL DEFAULT gen_random_uuid(),
    customer_id           INTEGER      NOT NULL REFERENCES customer(customer_id) ON DELETE CASCADE,
    activity_type         VARCHAR(10)  NOT NULL,
    occurred_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    author_employee_id    INTEGER          NULL REFERENCES employee(employee_id),
    subject                VARCHAR(200) NOT NULL DEFAULT '',
    body                    TEXT        NOT NULL DEFAULT '',
    created_at              TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by              INTEGER          NULL REFERENCES employee(employee_id),
    updated_at              TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_by              INTEGER          NULL REFERENCES employee(employee_id),
    deleted_at              TIMESTAMP        NULL,
    deleted_by               INTEGER         NULL REFERENCES employee(employee_id),
    record_version            INTEGER     NOT NULL DEFAULT 1,

    CONSTRAINT uq_crm_activity_uuid UNIQUE (crm_activity_uuid),
    CONSTRAINT chk_crm_activity_type CHECK (activity_type IN ('call','email','meeting','note','task')),
    CONSTRAINT chk_crm_activity_soft_delete CHECK (
        (deleted_at IS NULL AND deleted_by IS NULL) OR
        (deleted_at IS NOT NULL AND deleted_by IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_crm_activity_customer  ON crm_activity (customer_id)               WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_activity_type       ON crm_activity (customer_id, activity_type) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_activity_occurred   ON crm_activity (customer_id, occurred_at)   WHERE deleted_at IS NULL;

-- -- 000035_fabrication_module ------------------------------------------------
-- =====================================================================
-- Tenant migration 035: Fabrication & Installation module (record type FJOB).
-- A production job spawned from a sales order, tracking a stone order through
-- 16 shop-floor statuses. Adds serialized physical slabs (inventory_slab) so a
-- specific stone can be locked rather than quantity-reserved, an append-only
-- stock ledger (inventory_slab_ledger) that is the single writer of
-- slab-tracked stock, per-job pieces, slab allocations with disposition, the
-- 16-step checklist, a status trail, and dual approval gates.
-- Source: docs/superpowers/specs/2026-07-22-fabrication-installation-module-design.md
-- FK order: lookups -> inventory_slab -> inventory_slab_ledger ->
-- fabrication_job -> fabrication_job_item -> fabrication_job_slab ->
-- fabrication_job_step -> fabrication_job_history -> approver/approval.
-- =====================================================================

-- Lookups: FJOB record type + its 16 statuses. The status id is resolved by
-- subselect on the type code (NOT a hardcoded id) so it is robust to any
-- tenant whose lookup rows were seeded out of order (spec §2.0).
INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name, record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('FJOB', 'fabricationjob', 'Fabrication Job', TRUE, TRUE, 1)
ON CONFLICT (record_type_code) DO NOTHING;

INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES
    ('DRFT','Draft'), ('ORCV','Order Received'), ('MALC','Material Allocated'),
    ('TMPL','Templating In Progress'), ('TAPV','Template Approved'), ('FRDY','Fabrication Ready'),
    ('CUTG','Cutting In Progress'), ('EDGP','Edging and Polishing'), ('QCPD','Quality Control Pending'),
    ('QCPS','Quality Control Passed'), ('RSHP','Ready For Shipping'), ('TRAN','In Transit'),
    ('INST','Installation In Progress'), ('COMP','Completed'), ('HOLD','On Hold'), ('CANC','Cancelled')
) AS v(code, name)
CROSS JOIN lkp_record_type rt
WHERE rt.record_type_code = 'FJOB'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- inventory_slab -- serialized physical slab (sibling of inventory_stock) -----
CREATE TABLE IF NOT EXISTS inventory_slab (
    inventory_slab_id        SERIAL        PRIMARY KEY,
    inventory_slab_uuid      UUID          NOT NULL DEFAULT gen_random_uuid(),
    slab_serial              VARCHAR(50)   NOT NULL,                 -- our printed tag
    slab_vendor_id           INTEGER           NULL REFERENCES vendor(vendor_id),
    slab_supplier_code       VARCHAR(80)   NOT NULL DEFAULT '',      -- supplier's own id, as printed
    slab_received_at         DATE              NULL,
    slab_received_by         INTEGER           NULL REFERENCES employee(employee_id),
    slab_supplier_packing_ref VARCHAR(80)  NOT NULL DEFAULT '',
    inventory_item_id        INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id) ON DELETE CASCADE,
    warehouse_id             INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    slab_bundle_id           VARCHAR(50)   NOT NULL DEFAULT '',
    slab_block_id            VARCHAR(50)   NOT NULL DEFAULT '',
    slab_lot                 VARCHAR(50)   NOT NULL DEFAULT '',
    slab_length_mm           DECIMAL(10,2) NOT NULL,
    slab_width_mm            DECIMAL(10,2) NOT NULL,
    slab_thickness_mm        DECIMAL(10,2) NOT NULL,
    slab_area                DECIMAL(14,3) NOT NULL,                 -- in the item's own unit (§4.11.1)
    slab_area_unit_id        INTEGER       NOT NULL REFERENCES lkp_unit(unit_id),
    slab_form                VARCHAR(10)   NOT NULL DEFAULT 'full',  -- full | cut
    slab_parent_slab_id      INTEGER           NULL REFERENCES inventory_slab(inventory_slab_id),
    slab_status              VARCHAR(20)   NOT NULL DEFAULT 'available', -- available|reserved|consumed|scrapped
    slab_grade               VARCHAR(50)   NOT NULL DEFAULT '',
    slab_finish              VARCHAR(50)   NOT NULL DEFAULT '',
    slab_photo_key           VARCHAR(200)  NOT NULL DEFAULT '',
    slab_custom_fields       JSONB         NOT NULL DEFAULT '{}',
    slab_created_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    slab_created_by          INTEGER           NULL REFERENCES employee(employee_id),
    slab_updated_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    slab_updated_by          INTEGER           NULL REFERENCES employee(employee_id),
    slab_deleted_at          TIMESTAMP         NULL,
    slab_deleted_by          INTEGER           NULL REFERENCES employee(employee_id),
    slab_record_version      INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_slab_uuid UNIQUE (inventory_slab_uuid),
    CONSTRAINT chk_slab_dims      CHECK (slab_length_mm > 0 AND slab_width_mm > 0 AND slab_thickness_mm > 0),
    CONSTRAINT chk_slab_area      CHECK (slab_area > 0),
    CONSTRAINT chk_slab_form      CHECK (slab_form IN ('full','cut')),
    CONSTRAINT chk_slab_status    CHECK (slab_status IN ('available','reserved','consumed','scrapped')),
    -- form and parentage cannot disagree; a slab cannot be its own parent
    CONSTRAINT chk_slab_form_parent CHECK ((slab_form = 'cut') = (slab_parent_slab_id IS NOT NULL)),
    CONSTRAINT chk_slab_not_self    CHECK (slab_parent_slab_id IS DISTINCT FROM inventory_slab_id),
    -- a supplier code is meaningless without a supplier (NOT NULL DEFAULT '' so
    -- the CHECK cannot be bypassed by a NULL that evaluates the whole to NULL)
    CONSTRAINT chk_slab_supplier    CHECK (slab_supplier_code = '' OR slab_vendor_id IS NOT NULL),
    CONSTRAINT chk_slab_soft_delete CHECK (
        (slab_deleted_at IS NULL AND slab_deleted_by IS NULL) OR
        (slab_deleted_at IS NOT NULL AND slab_deleted_by IS NOT NULL)
    )
);
-- Serial unique among live rows only (case-insensitive) -- reusable after soft delete.
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_serial_active
    ON inventory_slab (LOWER(slab_serial)) WHERE slab_deleted_at IS NULL;
-- Supplier code unique per vendor among live FULL slabs with a non-blank code
-- (offcuts inherit the parent's code for recall, so full-only; blanks coexist).
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_supplier_code_active
    ON inventory_slab (slab_vendor_id, LOWER(slab_supplier_code))
    WHERE slab_deleted_at IS NULL AND slab_form = 'full' AND slab_supplier_code <> '';
CREATE INDEX IF NOT EXISTS idx_slab_recall  ON inventory_slab (slab_vendor_id, slab_supplier_code);
CREATE INDEX IF NOT EXISTS idx_slab_item    ON inventory_slab (inventory_item_id, slab_status) WHERE slab_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_slab_parent  ON inventory_slab (slab_parent_slab_id);
CREATE INDEX IF NOT EXISTS idx_slab_bundle  ON inventory_slab (slab_bundle_id);
CREATE INDEX IF NOT EXISTS idx_slab_custom_gin ON inventory_slab USING GIN (slab_custom_fields);

-- inventory_slab_ledger -- append-only, the ONLY writer of slab-tracked stock -
-- Invariant: inventory_stock.quantity_on_hand = SUM(quantity_delta) per item.
CREATE TABLE IF NOT EXISTS inventory_slab_ledger (
    inventory_slab_ledger_id SERIAL        PRIMARY KEY,
    inventory_slab_id        INTEGER       NOT NULL REFERENCES inventory_slab(inventory_slab_id),
    inventory_item_id        INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    warehouse_id             INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    event                    VARCHAR(20)   NOT NULL,
    quantity_delta           DECIMAL(14,3) NOT NULL,   -- signed, in the item's unit
    fabrication_job_slab_id  INTEGER           NULL,   -- FK added after that table exists
    occurred_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor_employee_id        INTEGER           NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_slab_ledger_event CHECK (event IN ('received','consumed','recovered','scrapped','adjusted'))
);
-- Each stock-affecting event is once-only, so a re-run transition cannot
-- double-count -- the double-count bugs are made unrepresentable, not tested.
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_ledger_received  ON inventory_slab_ledger (inventory_slab_id) WHERE event = 'received';
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_ledger_consumed  ON inventory_slab_ledger (inventory_slab_id) WHERE event = 'consumed';
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_ledger_scrapped  ON inventory_slab_ledger (inventory_slab_id) WHERE event = 'scrapped';
CREATE INDEX IF NOT EXISTS idx_slab_ledger_item ON inventory_slab_ledger (inventory_item_id);

-- fabrication_job -- header -------------------------------------------------
CREATE TABLE IF NOT EXISTS fabrication_job (
    fabrication_job_id        SERIAL        PRIMARY KEY,
    fabrication_job_uuid      UUID          NOT NULL DEFAULT gen_random_uuid(),
    fabrication_job_number    VARCHAR(20)       NULL,  -- 'FJOB-000001', set post-insert
    record_type               INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = FJOB
    fabrication_job_status    INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),
    sales_order_id            INTEGER       NOT NULL REFERENCES sales_order(sales_order_id),
    fabrication_job_customer_id INTEGER     NOT NULL REFERENCES customer(customer_id),
    -- hold: the status the job was in when held, restored on resume (§1.2)
    job_held_from_status_id   INTEGER           NULL REFERENCES lkp_record_status(record_status_id),
    -- cancel intent: disposition is only legal while a cancel is in progress (§4.4.3)
    job_cancel_requested_at   TIMESTAMP         NULL,
    -- approval gates at TAPV and QCPS (§2.7), mirrors sales_order
    job_approval_status       VARCHAR(10)   NOT NULL DEFAULT 'none',
    job_approved_by           INTEGER           NULL REFERENCES employee(employee_id),
    -- site snapshot (frozen at create)
    job_site_customer_name    VARCHAR(150)  NOT NULL DEFAULT '',
    job_site_addr_line1       VARCHAR(100)  NOT NULL DEFAULT '',
    job_site_addr_line2       VARCHAR(100)  NOT NULL DEFAULT '',
    job_site_addr_city        VARCHAR(100)  NOT NULL DEFAULT '',
    job_site_addr_state       INTEGER           NULL REFERENCES lkp_state(state_id),
    job_site_addr_zip         VARCHAR(10)   NOT NULL DEFAULT '',
    job_site_phone            VARCHAR(30)   NOT NULL DEFAULT '',
    -- scheduling
    job_template_date         DATE              NULL,
    job_fabrication_start     DATE              NULL,
    job_promised_install_date DATE              NULL,
    job_actual_install_date   DATE              NULL,
    -- assignment
    job_owner_id              INTEGER           NULL REFERENCES employee(employee_id),
    job_templater_id          INTEGER           NULL REFERENCES employee(employee_id),
    job_fabricator_id         INTEGER           NULL REFERENCES employee(employee_id),
    job_install_crew_id       INTEGER           NULL REFERENCES employee(employee_id),
    job_notes                 TEXT          NOT NULL DEFAULT '',
    job_custom_fields         JSONB         NOT NULL DEFAULT '{}',
    fabrication_job_created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fabrication_job_created_by INTEGER          NULL REFERENCES employee(employee_id),
    fabrication_job_updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fabrication_job_updated_by INTEGER          NULL REFERENCES employee(employee_id),
    fabrication_job_deleted_at TIMESTAMP        NULL,
    fabrication_job_deleted_by INTEGER          NULL REFERENCES employee(employee_id),
    fabrication_job_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_fabrication_job_uuid UNIQUE (fabrication_job_uuid),
    CONSTRAINT chk_fj_approval CHECK (job_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_fj_soft_delete CHECK (
        (fabrication_job_deleted_at IS NULL AND fabrication_job_deleted_by IS NULL) OR
        (fabrication_job_deleted_at IS NOT NULL AND fabrication_job_deleted_by IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_fj_so       ON fabrication_job (sales_order_id)          WHERE fabrication_job_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_fj_status   ON fabrication_job (fabrication_job_status)  WHERE fabrication_job_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_fj_owner    ON fabrication_job (job_owner_id)            WHERE fabrication_job_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_fj_customer ON fabrication_job (fabrication_job_customer_id) WHERE fabrication_job_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_fj_created  ON fabrication_job (fabrication_job_created_at, fabrication_job_id) WHERE fabrication_job_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_fj_custom_gin ON fabrication_job USING GIN (job_custom_fields);

-- fabrication_job_item -- one row per fabricated piece ----------------------
CREATE TABLE IF NOT EXISTS fabrication_job_item (
    fabrication_job_item_id   SERIAL        PRIMARY KEY,
    fabrication_job_item_uuid UUID          NOT NULL DEFAULT gen_random_uuid(),
    fabrication_job_id        INTEGER       NOT NULL REFERENCES fabrication_job(fabrication_job_id) ON DELETE CASCADE,
    sales_order_item_id       INTEGER           NULL REFERENCES sales_order_item(sales_order_item_id),
    piece_number              INTEGER       NOT NULL,
    piece_name                VARCHAR(150)  NOT NULL DEFAULT '',
    piece_type                VARCHAR(50)   NOT NULL DEFAULT '',
    piece_length_mm           DECIMAL(10,2) NOT NULL DEFAULT 0,
    piece_width_mm            DECIMAL(10,2) NOT NULL DEFAULT 0,
    piece_thickness_mm        DECIMAL(10,2) NOT NULL DEFAULT 0,
    edge_profile_id           INTEGER           NULL,
    sink_cutout_count         INTEGER       NOT NULL DEFAULT 0,
    cooktop_cutout_count      INTEGER       NOT NULL DEFAULT 0,
    seam_count                INTEGER       NOT NULL DEFAULT 0,
    piece_status              VARCHAR(20)   NOT NULL DEFAULT 'pending',
    item_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by           INTEGER           NULL REFERENCES employee(employee_id),
    item_deleted_at           TIMESTAMP         NULL,
    CONSTRAINT uq_fab_item_uuid UNIQUE (fabrication_job_item_uuid),
    CONSTRAINT chk_fab_item_counts CHECK (sink_cutout_count >= 0 AND cooktop_cutout_count >= 0 AND seam_count >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_fab_item_piece_active
    ON fabrication_job_item (fabrication_job_id, piece_number) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_fab_item_job ON fabrication_job_item (fabrication_job_id) WHERE item_deleted_at IS NULL;

-- fabrication_job_slab -- reservation join + disposition --------------------
CREATE TABLE IF NOT EXISTS fabrication_job_slab (
    fabrication_job_slab_id   SERIAL        PRIMARY KEY,
    fabrication_job_id        INTEGER       NOT NULL REFERENCES fabrication_job(fabrication_job_id) ON DELETE CASCADE,
    fabrication_job_item_id   INTEGER           NULL REFERENCES fabrication_job_item(fabrication_job_item_id) ON DELETE SET NULL,
    inventory_slab_id         INTEGER       NOT NULL REFERENCES inventory_slab(inventory_slab_id),
    allocation_status         VARCHAR(20)   NOT NULL DEFAULT 'reserved', -- reserved|consumed|released
    yield_area                DECIMAL(14,3)     NULL,   -- in the item's unit
    reserved_at               TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reserved_by               INTEGER           NULL REFERENCES employee(employee_id),
    consumed_at               TIMESTAMP         NULL,
    consumed_by               INTEGER           NULL REFERENCES employee(employee_id),
    -- disposition (declared when a job is cancelled after cutting, §4.4)
    disposition               VARCHAR(20)       NULL,   -- recovered|scrapped|delivered
    disposition_recorded_at   TIMESTAMP         NULL,
    disposition_recorded_by   INTEGER           NULL REFERENCES employee(employee_id),
    recovered_slab_id         INTEGER           NULL REFERENCES inventory_slab(inventory_slab_id),
    recovered_area            DECIMAL(14,3)     NULL,
    CONSTRAINT chk_fab_slab_status CHECK (allocation_status IN ('reserved','consumed','released')),
    CONSTRAINT chk_fab_slab_disp   CHECK (disposition IS NULL OR disposition IN ('recovered','scrapped','delivered')),
    CONSTRAINT chk_fab_slab_recovered CHECK ((disposition = 'recovered') = (recovered_slab_id IS NOT NULL)),
    CONSTRAINT chk_fab_slab_recovered_area CHECK ((disposition = 'recovered') = (recovered_area IS NOT NULL AND recovered_area > 0))
);
-- The double-selling guard at the DB layer: a slab has at most one live allocation.
CREATE UNIQUE INDEX IF NOT EXISTS uq_fab_slab_live
    ON fabrication_job_slab (inventory_slab_id) WHERE allocation_status IN ('reserved','consumed');
CREATE INDEX IF NOT EXISTS idx_fab_slab_job  ON fabrication_job_slab (fabrication_job_id);
CREATE INDEX IF NOT EXISTS idx_fab_slab_slab ON fabrication_job_slab (inventory_slab_id);

-- Deferred FK: the ledger references the allocation that caused an event.
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints
                   WHERE constraint_name = 'fk_slab_ledger_fab_slab') THEN
        ALTER TABLE inventory_slab_ledger
            ADD CONSTRAINT fk_slab_ledger_fab_slab
            FOREIGN KEY (fabrication_job_slab_id) REFERENCES fabrication_job_slab(fabrication_job_slab_id);
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_ledger_recovered
    ON inventory_slab_ledger (fabrication_job_slab_id) WHERE event = 'recovered';

-- fabrication_job_step -- the 16 checklist rows, seeded per job -------------
CREATE TABLE IF NOT EXISTS fabrication_job_step (
    fabrication_job_step_id   SERIAL        PRIMARY KEY,
    fabrication_job_id        INTEGER       NOT NULL REFERENCES fabrication_job(fabrication_job_id) ON DELETE CASCADE,
    fabrication_job_item_id   INTEGER           NULL REFERENCES fabrication_job_item(fabrication_job_item_id) ON DELETE CASCADE,
    step_code                 VARCHAR(24)   NOT NULL,
    step_sequence             SMALLINT      NOT NULL,
    step_status               VARCHAR(20)   NOT NULL DEFAULT 'pending',
    step_started_at           TIMESTAMP         NULL,
    step_started_by           INTEGER           NULL REFERENCES employee(employee_id),
    step_completed_at         TIMESTAMP         NULL,
    step_completed_by         INTEGER           NULL REFERENCES employee(employee_id),
    step_notes                TEXT          NOT NULL DEFAULT '',
    step_payload              JSONB         NOT NULL DEFAULT '{}',
    CONSTRAINT chk_fab_step_status CHECK (step_status IN ('pending','in_progress','blocked','skipped','completed')),
    CONSTRAINT chk_fab_step_seq    CHECK (step_sequence BETWEEN 1 AND 16)
);
-- Uniqueness needs two partial indexes: NULLs compare distinct, so a single
-- 3-column UNIQUE would leave the seven job-grain (NULL item) steps unconstrained.
CREATE UNIQUE INDEX IF NOT EXISTS uq_fab_step_piece
    ON fabrication_job_step (fabrication_job_id, fabrication_job_item_id, step_code)
    WHERE fabrication_job_item_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_fab_step_job
    ON fabrication_job_step (fabrication_job_id, step_code)
    WHERE fabrication_job_item_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_fab_step_job ON fabrication_job_step (fabrication_job_id);

-- fabrication_job_history -- from/to status trail ---------------------------
CREATE TABLE IF NOT EXISTS fabrication_job_history (
    fabrication_job_history_id SERIAL      PRIMARY KEY,
    fabrication_job_id         INTEGER     NOT NULL REFERENCES fabrication_job(fabrication_job_id) ON DELETE CASCADE,
    from_status_id             INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id               INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    action                     VARCHAR(32) NOT NULL DEFAULT 'transition',
    actor_employee_id          INTEGER         NULL REFERENCES employee(employee_id),
    snapshot                   JSONB       NOT NULL DEFAULT '{}',
    at                         TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_fab_history_action CHECK (action IN ('create','transition','hold','resume','cancel','approve','update','rework','fulfillment_clamped','piece_add','piece_update','piece_remove'))
);
CREATE INDEX IF NOT EXISTS idx_fab_history_job ON fabrication_job_history (fabrication_job_id);

-- Widen chk_fab_history_action for tenant DBs that already ran the CREATE
-- TABLE above before piece add/update/remove started writing history rows
-- (widening-only; existing rows remain valid) -- mirrors the
-- chk_sales_order_history_action / chk_invoice_history_action widenings above.
ALTER TABLE fabrication_job_history DROP CONSTRAINT IF EXISTS chk_fab_history_action;
ALTER TABLE fabrication_job_history ADD CONSTRAINT chk_fab_history_action
    CHECK (action IN ('create','transition','hold','resume','cancel','approve','update','rework','fulfillment_clamped','piece_add','piece_update','piece_remove'));

-- fabrication_job_approver / _approval -- gates at TAPV and QCPS -------------
CREATE TABLE IF NOT EXISTS fabrication_job_approver (
    fabrication_job_approver_id SERIAL    PRIMARY KEY,
    record_type_id             INTEGER    NOT NULL REFERENCES lkp_record_type(record_type_id),
    record_status_id           INTEGER    NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id       INTEGER    NOT NULL REFERENCES employee(employee_id),
    is_active                  BOOLEAN    NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER        NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_fab_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_fab_approver_lookup
    ON fabrication_job_approver (record_type_id, record_status_id) WHERE is_active;

CREATE TABLE IF NOT EXISTS fabrication_job_approval (
    fabrication_job_approval_id SERIAL    PRIMARY KEY,
    fabrication_job_id         INTEGER    NOT NULL REFERENCES fabrication_job(fabrication_job_id) ON DELETE CASCADE,
    record_status_id           INTEGER    NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id       INTEGER    NOT NULL REFERENCES employee(employee_id),
    approved_at                TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_fab_approval UNIQUE (fabrication_job_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_fab_approval_job ON fabrication_job_approval (fabrication_job_id);


-- =====================================================================
-- PURCHASE ORDER MODULE
-- Spec: docs/superpowers/specs/2026-07-22-purchase-order-module-design.md
-- Reuses (already seeded, do not recreate): lkp_record_type PORD (id 13),
-- lkp_record_status rows for record_type=13 (DRFT/PAPV/APPV/SENT/PART/
-- RCVD/CLSD/CANC), authz.ResourcePurchaseOrder, the 'purchase_order' JSONB
-- workflow (custom-field definition host), vendor, inventory_item, lkp_*.
-- Adds zero seed stanzas.
-- =====================================================================

-- purchase_order (header) -- mirrors estimate, with a vendor counterparty
-- instead of a customer and a single ship-to (deliver-to) snapshot block
-- (the bill-to is the tenant itself; POs have no billing/shipping pair).
CREATE TABLE IF NOT EXISTS purchase_order (
    purchase_order_id            SERIAL        PRIMARY KEY,
    purchase_order_uuid          UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id               INTEGER           NULL,  -- platform owner stamp, no cross-DB FK (matches estimate/invoice)
    purchase_order_number        VARCHAR(20)       NULL,  -- 'PORD-000001', generated post-insert in Go

    -- Classification
    record_type                  INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = PORD
    purchase_order_status        INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven -- AD-6, mirrors estimate_approval_status)
    purchase_order_approval_status VARCHAR(10) NOT NULL DEFAULT 'none',  -- none | pending | approved
    purchase_order_approved_by   INTEGER           NULL REFERENCES employee(employee_id),

    -- Counterparty (AD-2: single vendor, name snapshotted at create/update)
    purchase_order_vendor_id     INTEGER       NOT NULL REFERENCES vendor(vendor_id),
    purchase_order_vendor_name   VARCHAR(150)  NOT NULL DEFAULT '',

    -- Primary info
    purchase_order_reference_number VARCHAR(50) NOT NULL DEFAULT '',  -- vendor's quote/reference
    purchase_order_date          DATE          NOT NULL DEFAULT CURRENT_DATE,
    purchase_order_expected_date DATE              NULL,  -- expected delivery
    purchase_order_sales_tax_percent DECIMAL(6,4) NOT NULL DEFAULT 0,
    purchase_order_memo          TEXT          NOT NULL DEFAULT '',
    purchase_order_notes         TEXT          NOT NULL DEFAULT '',
    purchase_order_internal_notes TEXT         NOT NULL DEFAULT '',
    purchase_order_terms_conditions TEXT       NOT NULL DEFAULT '',

    -- Assignment (IDOR scope owner)
    purchase_order_owner_id      INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / currency
    purchase_order_payment_terms INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    purchase_order_currency      INTEGER           NULL REFERENCES lkp_currency(currency_id),
    purchase_order_exchange_rate DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    purchase_order_subtotal      DECIMAL(15,2) NOT NULL DEFAULT 0,
    purchase_order_discount_total DECIMAL(15,2) NOT NULL DEFAULT 0,
    purchase_order_tax_total     DECIMAL(15,2) NOT NULL DEFAULT 0,
    purchase_order_shipping_charge DECIMAL(15,2) NOT NULL DEFAULT 0,
    purchase_order_adjustment    DECIMAL(15,2) NOT NULL DEFAULT 0,
    purchase_order_grand_total   DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Ship-to (deliver-to) snapshot -- the buyer's receiving address
    purchase_order_ship_name     VARCHAR(150)  NOT NULL DEFAULT '',
    purchase_order_ship_attention VARCHAR(150) NOT NULL DEFAULT '',
    purchase_order_ship_addr_line1 VARCHAR(100) NOT NULL DEFAULT '',
    purchase_order_ship_addr_line2 VARCHAR(100) NOT NULL DEFAULT '',
    purchase_order_ship_addr_suitenum VARCHAR(20) NOT NULL DEFAULT '',
    purchase_order_ship_addr_city VARCHAR(100) NOT NULL DEFAULT '',
    purchase_order_ship_addr_state INTEGER         NULL REFERENCES lkp_state(state_id),
    purchase_order_ship_addr_zip VARCHAR(10)   NOT NULL DEFAULT '',
    purchase_order_ship_addr_country INTEGER       NULL REFERENCES lkp_country(country_id),
    purchase_order_ship_phone    VARCHAR(20)   NOT NULL DEFAULT '',
    purchase_order_ship_fax      VARCHAR(20)   NOT NULL DEFAULT '',
    purchase_order_ship_email    VARCHAR(100)  NOT NULL DEFAULT '',

    -- Dynamic + audit
    purchase_order_custom_fields JSONB         NOT NULL DEFAULT '{}',
    purchase_order_created_at    TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    purchase_order_created_by    INTEGER           NULL REFERENCES employee(employee_id),
    purchase_order_updated_at    TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    purchase_order_updated_by    INTEGER           NULL REFERENCES employee(employee_id),
    purchase_order_deleted_at    TIMESTAMP         NULL,
    purchase_order_deleted_by    INTEGER           NULL REFERENCES employee(employee_id),
    purchase_order_record_version INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_purchase_order_uuid   UNIQUE (purchase_order_uuid),
    CONSTRAINT uq_purchase_order_number UNIQUE (purchase_order_number),
    CONSTRAINT chk_po_approval_status   CHECK (purchase_order_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_po_tax_percent       CHECK (purchase_order_sales_tax_percent >= 0 AND purchase_order_sales_tax_percent <= 100),
    CONSTRAINT chk_po_totals_nonneg     CHECK (purchase_order_subtotal >= 0 AND purchase_order_grand_total >= 0),
    CONSTRAINT chk_po_soft_delete       CHECK (
        (purchase_order_deleted_at IS NULL AND purchase_order_deleted_by IS NULL) OR
        (purchase_order_deleted_at IS NOT NULL AND purchase_order_deleted_by IS NOT NULL)
    )
);

-- purchase_order_item (line items) -- mirrors estimate_item + the receiving
-- hook (AD-4): qty_received is written only by the future Item Receipt
-- module; stable ids let receipts reference po lines for 3-way match.
CREATE TABLE IF NOT EXISTS purchase_order_item (
    purchase_order_item_id    SERIAL        PRIMARY KEY,
    purchase_order_item_uuid  UUID          NOT NULL DEFAULT gen_random_uuid(),
    purchase_order_id         INTEGER       NOT NULL REFERENCES purchase_order(purchase_order_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),   -- NULL = free-text line

    -- Snapshots (frozen at add time -- never re-read from catalog)
    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description               TEXT          NOT NULL DEFAULT '',
    unit_id                   INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                  DECIMAL(14,3) NOT NULL DEFAULT 0,
    qty_received              DECIMAL(14,3) NOT NULL DEFAULT 0,  -- AD-4: written by Item Receipt postings
    unit_price                DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent          DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id               INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent               DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by           INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at           TIMESTAMP         NULL,
    item_record_version       INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_po_item_uuid    UNIQUE (purchase_order_item_uuid),
    CONSTRAINT chk_poi_qty        CHECK (quantity >= 0),
    CONSTRAINT chk_poi_qty_received CHECK (qty_received >= 0 AND qty_received <= quantity),
    CONSTRAINT chk_poi_unit_price CHECK (unit_price >= 0),
    CONSTRAINT chk_poi_discount   CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_poi_tax        CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- purchase_order_history -- status/action trail (mirrors estimate_history)
CREATE TABLE IF NOT EXISTS purchase_order_history (
    purchase_order_history_id SERIAL       PRIMARY KEY,
    purchase_order_id         INTEGER      NOT NULL REFERENCES purchase_order(purchase_order_id) ON DELETE CASCADE,
    from_status_id            INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id              INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                    VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | update | approve
    actor_employee_id         INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                  JSONB        NOT NULL DEFAULT '{}',
    at                        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- purchase_order_approver / purchase_order_approval (AD-6, exact structural
-- copies of estimate_approver / estimate_approval)
CREATE TABLE IF NOT EXISTS purchase_order_approver (
    purchase_order_approver_id SERIAL      PRIMARY KEY,
    record_type_id            INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = PORD
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                 BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_purchase_order_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS purchase_order_approval (
    purchase_order_approval_id SERIAL     PRIMARY KEY,
    purchase_order_id         INTEGER     NOT NULL REFERENCES purchase_order(purchase_order_id) ON DELETE CASCADE,
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- status the sign-off was for
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at               TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_purchase_order_approval UNIQUE (purchase_order_id, record_status_id, approver_employee_id)
);

-- purchase_order indexes (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_po_vendor        ON purchase_order (purchase_order_vendor_id) WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_status        ON purchase_order (purchase_order_status)    WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_date          ON purchase_order (purchase_order_date)      WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_expected_date ON purchase_order (purchase_order_expected_date) WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_owner         ON purchase_order (purchase_order_owner_id)  WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_created_id    ON purchase_order (purchase_order_created_at, purchase_order_id) WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_updated_id    ON purchase_order (purchase_order_updated_at, purchase_order_id) WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_grandtotal_id ON purchase_order (purchase_order_grand_total, purchase_order_id) WHERE purchase_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_po_custom_gin    ON purchase_order USING GIN (purchase_order_custom_fields);

CREATE INDEX IF NOT EXISTS idx_poi_po   ON purchase_order_item (purchase_order_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_poi_item ON purchase_order_item (inventory_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_poi_line_active
    ON purchase_order_item (purchase_order_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_po_history_po ON purchase_order_history (purchase_order_id);

CREATE INDEX IF NOT EXISTS idx_purchase_order_approver_lookup
    ON purchase_order_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_purchase_order_approval_po ON purchase_order_approval (purchase_order_id);
