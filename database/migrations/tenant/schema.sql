-- =====================================================================
-- StoneSuite Tenant Schema — single canonical file.
--
-- Applied to EACH tenant's isolated database at provisioning time and
-- on every startup for existing tenants (idempotent via CREATE IF NOT EXISTS,
-- INSERT ON CONFLICT DO NOTHING, ADD COLUMN IF NOT EXISTS).
--
-- To change the tenant schema, edit this file directly.
-- History lives in git. No numbered migration files exist any more.
-- =====================================================================


-- ── 000001_tenant_base ──────────────────────────────────────────────────
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


-- ── 000002_tenant_rbac ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 2: dynamic RBAC.
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
    scope     VARCHAR(16) NOT NULL DEFAULT 'all',      -- all | team | own
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

-- Teams give meaning to the 'team' permission scope (used by record visibility
-- in the Phase 3 workflow engine). Defined now so scope='team' is enforceable.
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


-- ── 000003_tenant_workflow ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 3: dynamic workflow engine.
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
    current_state_id UUID        REFERENCES workflow_states(id),
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


-- ── 000004_prospects ──────────────────────────────────────────────────
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


-- ── 000005_leads ──────────────────────────────────────────────────
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


-- ── 000006_crm_custom_fields ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 6: custom_fields JSONB on CRM tables.
-- Allows admins to add up to 15 custom fields (via workflow_field_definitions)
-- to Lead and Prospect records without requiring schema migrations.
-- =====================================================================

ALTER TABLE leads     ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}';
ALTER TABLE prospects ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_leads_custom     ON leads     USING gin(custom_fields);
CREATE INDEX IF NOT EXISTS idx_prospects_custom ON prospects USING gin(custom_fields);


-- ── 000007_crm_unified ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 7: Unified CRM workflow fields.
--
-- Adds pipeline_order to workflows so the Lead→Prospect→Customer
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


-- ── 000008_record_numbering ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 8: per-workflow record auto-numbering.
--
-- Lets a super admin configure auto-generated record numbers (prefix +
-- zero-padded sequence + suffix) per workflow. One row per workflow,
-- created lazily via upsert when the config is first set — no seeding
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


-- ── 000012_drop_legacy_crm ──────────────────────────────────────────────────
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


-- ── 000013_employee ──────────────────────────────────────────────────
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
    employee_user_id        UUID             NULL REFERENCES users(id),
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


-- ── 000014_lkp_tables ──────────────────────────────────────────────────
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

-- Indexes — active-record queries -------------------------------------
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


-- ── 000018_lkp_price_level ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant migration 018: lkp_price_level — the 12th CRM lookup table.
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


-- ── 000015_crm_record ──────────────────────────────────────────────────
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


-- ── 000016_crm_record_history ──────────────────────────────────────────────────
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


-- ── 000017_crm_workflow_approver ──────────────────────────────────────────────────
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


-- ── 000019_customer ──────────────────────────────────────────────────
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


-- ── 000021_customer_history ──────────────────────────────────────────────────
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


-- ── 000020_audit_logs_enrich ──────────────────────────────────────────────────
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


-- ── 000024_attachments_recover ──────────────────────────────────────────────────
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


-- ── 000023_attachments_drop_wf_fk ──────────────────────────────────────────────────
-- Migration 023: decouple workflow_record_attachments from workflow_records.
--
-- The attachment table was originally keyed to workflow_records(id), but the
-- v2 relational CRM design stores records in the customer table instead. Drop
-- the FK so attachments can be associated with any record UUID regardless of
-- which table it lives in. Existence is now enforced in application code.
ALTER TABLE IF EXISTS workflow_record_attachments
  DROP CONSTRAINT IF EXISTS workflow_record_attachments_record_id_fkey;


-- ── 000009_seed_crm_workflows ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 8: Seed CRM workflows (Lead/Prospect/Customer).
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


-- ── 000010_seed_sales_purchases_workflows ──────────────────────────────────────────────────
-- =====================================================================
-- Tenant-template schema — Phase 10: Seed Sales & Purchases workflows.
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


-- ── 000022_deactivate_legacy_crm_record_status ──────────────────────────────────────────────────
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

-- ── 000023_rag_vectors ──────────────────────────────────────────────────
-- =====================================================================
-- RAG assistant storage. Vectors live in the tenant DB so cross-tenant
-- retrieval is impossible by construction. owner_user_id / team_id are
-- denormalized onto each chunk so the RBAC scope clause can be ANDed onto
-- the similarity search (scope can only narrow, never widen — same
-- invariant as the Record Filter Engine).
-- =====================================================================

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rag_chunks (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_type   TEXT NOT NULL DEFAULT 'record',
    source_id     UUID NOT NULL,
    workflow_id   UUID NOT NULL,
    owner_user_id UUID,
    team_id       UUID,
    content       TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    embedding     vector(768) NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS rag_chunks_source_idx   ON rag_chunks (source_id);
CREATE INDEX        IF NOT EXISTS rag_chunks_scope_idx    ON rag_chunks (owner_user_id, team_id);
CREATE INDEX        IF NOT EXISTS rag_chunks_embedding_idx
    ON rag_chunks USING hnsw (embedding vector_cosine_ops);

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

