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

-- Backfill: link every existing users row to an employee row if it doesn't
-- have one yet. Every employee_id-based FK (Sales Rep, CRM/module approvers,
-- "own"-scope record ownership via workflow.EmployeeIDByIdentity) resolves
-- through employee, not users directly -- new signups get this immediately
-- now (provisioning/provisioner.go, controllers/user.go AcceptUserInvite,
-- controllers/saml_acs.go all call userstore.EnsureEmployeeForUser), this
-- catches everyone who signed up before that existed. Re-runs safely: the
-- NOT EXISTS guard skips users that already have a row, and ON CONFLICT
-- guards the rare case where employee_email already collides with a stale
-- row (skipped for manual reconciliation rather than erroring).
INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email)
SELECT u.id, COALESCE(NULLIF(TRIM(u.full_name), ''), u.email), '', u.email
FROM users u
WHERE NOT EXISTS (SELECT 1 FROM employee e WHERE e.employee_user_id = u.id)
ON CONFLICT (employee_email) DO NOTHING;


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

-- Also widened (migration 038) to allow 'approve'/'approve_override', which
-- approvalchain.Approve (engine.go) writes once Invoice's status transitions
-- moved onto the shared engine -- this is the last unconditional definition
-- of this constraint in the file, so it's the one that must carry the fix
-- (the guarded 'IF NOT EXISTS conname' block above it is a no-op forever on
-- any tenant DB where the constraint already exists, i.e. every already-
-- provisioned tenant).
ALTER TABLE invoice_history DROP CONSTRAINT IF EXISTS chk_invoice_history_action;
ALTER TABLE invoice_history ADD CONSTRAINT chk_invoice_history_action
    CHECK (action IN ('create','transition','update','payment','unapply','credit','uncredit','convert','approve','approve_override'));

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
    -- Only non-negativity is enforced here. The original upper bound
    -- (qty_received <= quantity) was relaxed by migration 032 so the Item
    -- Receipt module can record an over-delivery; the ordered-vs-received
    -- ceiling is a business rule enforced in Go (itemreceipt.WithinTolerance
    -- plus the item_receipt:approve override), not a storage constraint.
    CONSTRAINT chk_poi_qty_received_nonneg CHECK (qty_received >= 0),
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


-- =====================================================================
-- ITEM RECEIPT MODULE
-- Spec: docs/superpowers/specs/2026-07-23-item-receipt-module-design.md
-- Reuses (already seeded, do not recreate): lkp_record_type IRCT (id 14),
-- lkp_record_status rows for record_type=14 (PEND/PART/RCVD/VOID),
-- authz.ResourceItemReceipt, the 'item_receipt' JSONB workflow (custom-field
-- definition host), purchase_order, purchase_order_item, vendor,
-- inventory_item, inventory_stock, lkp_warehouse.
-- Adds zero seed stanzas.
--
-- This block also relaxes one constraint shipped by the Purchase Order
-- module (see the guarded stanza at the end) -- the only non-additive
-- change in the migration.
-- =====================================================================

-- item_receipt (header) -- the document recording goods physically arriving
-- against a finalized purchase order. Vendor and warehouse are snapshotted /
-- fixed at create time; the receipt never re-derives them (AD-2 rule).
CREATE TABLE IF NOT EXISTS item_receipt (
    item_receipt_id              SERIAL        PRIMARY KEY,
    item_receipt_uuid            UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id               INTEGER           NULL,  -- platform owner stamp, no cross-DB FK
    item_receipt_number          VARCHAR(20)       NULL,  -- 'IRCT-000001', generated post-insert in Go

    -- Classification
    record_type                  INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = IRCT
    item_receipt_status          INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Source document (AD-1: a receipt only ever exists against a PO)
    purchase_order_id            INTEGER       NOT NULL REFERENCES purchase_order(purchase_order_id),

    -- Counterparty, inherited from the PO and snapshotted
    item_receipt_vendor_id       INTEGER       NOT NULL REFERENCES vendor(vendor_id),
    item_receipt_vendor_name     VARCHAR(150)  NOT NULL DEFAULT '',

    -- Destination (AD-4: the PO carries no warehouse, so the receipt supplies it)
    warehouse_id                 INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),

    -- Primary info
    item_receipt_date            DATE          NOT NULL DEFAULT CURRENT_DATE,
    item_receipt_packing_slip    VARCHAR(80)   NOT NULL DEFAULT '',
    item_receipt_carrier         VARCHAR(80)   NOT NULL DEFAULT '',
    item_receipt_tracking_number VARCHAR(80)   NOT NULL DEFAULT '',
    item_receipt_bill_of_lading  VARCHAR(80)   NOT NULL DEFAULT '',
    item_receipt_notes           TEXT          NOT NULL DEFAULT '',
    item_receipt_internal_notes  TEXT          NOT NULL DEFAULT '',

    -- Assignment (IDOR scope owner)
    item_receipt_owner_id        INTEGER           NULL REFERENCES employee(employee_id),

    -- Posting / void trail (AD-5: posted receipts are immutable; correction
    -- is void-and-reissue, and voiding reverses stock + qty_received)
    item_receipt_posted_at       TIMESTAMP         NULL,
    item_receipt_posted_by       INTEGER           NULL REFERENCES employee(employee_id),
    item_receipt_voided_at       TIMESTAMP         NULL,
    item_receipt_voided_by       INTEGER           NULL REFERENCES employee(employee_id),
    item_receipt_void_reason     TEXT          NOT NULL DEFAULT '',

    -- AD-3: why an over-delivery beyond tolerance was waved through, captured
    -- at post time alongside the item_receipt:approve grant that allowed it.
    item_receipt_over_receipt_reason TEXT      NOT NULL DEFAULT '',

    -- Dynamic + audit
    item_receipt_custom_fields   JSONB         NOT NULL DEFAULT '{}',
    item_receipt_created_at      TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_receipt_created_by      INTEGER           NULL REFERENCES employee(employee_id),
    item_receipt_updated_at      TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_receipt_updated_by      INTEGER           NULL REFERENCES employee(employee_id),
    item_receipt_deleted_at      TIMESTAMP         NULL,
    item_receipt_deleted_by      INTEGER           NULL REFERENCES employee(employee_id),
    item_receipt_record_version  INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_item_receipt_uuid   UNIQUE (item_receipt_uuid),
    CONSTRAINT uq_item_receipt_number UNIQUE (item_receipt_number),
    CONSTRAINT chk_ir_soft_delete     CHECK (
        (item_receipt_deleted_at IS NULL AND item_receipt_deleted_by IS NULL) OR
        (item_receipt_deleted_at IS NOT NULL AND item_receipt_deleted_by IS NOT NULL)
    ),
    CONSTRAINT chk_ir_posted_pair     CHECK (
        (item_receipt_posted_at IS NULL AND item_receipt_posted_by IS NULL) OR
        (item_receipt_posted_at IS NOT NULL AND item_receipt_posted_by IS NOT NULL)
    ),
    CONSTRAINT chk_ir_void_pair       CHECK (
        (item_receipt_voided_at IS NULL AND item_receipt_voided_by IS NULL) OR
        (item_receipt_voided_at IS NOT NULL AND item_receipt_voided_by IS NOT NULL)
    )
);

-- item_receipt_line -- one PO line's arrival. purchase_order_item_id is NOT
-- NULL: a receipt line always traces to an ordered line (no ad-hoc receiving),
-- which is also what makes the future Vendor Bill 3-way match possible.
CREATE TABLE IF NOT EXISTS item_receipt_line (
    item_receipt_line_id      SERIAL        PRIMARY KEY,
    item_receipt_line_uuid    UUID          NOT NULL DEFAULT gen_random_uuid(),
    item_receipt_id           INTEGER       NOT NULL REFERENCES item_receipt(item_receipt_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    purchase_order_item_id    INTEGER       NOT NULL REFERENCES purchase_order_item(purchase_order_item_id),
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),  -- NULL = free-text PO line

    -- Snapshots (frozen at add time -- never re-read from the PO or catalog)
    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description               TEXT          NOT NULL DEFAULT '',
    unit_id                   INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)   NOT NULL DEFAULT '',

    qty_received              DECIMAL(14,3) NOT NULL DEFAULT 0,
    qty_rejected              DECIMAL(14,3) NOT NULL DEFAULT 0,  -- damaged/refused, never enters stock
    line_notes                TEXT          NOT NULL DEFAULT '',

    item_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by           INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at           TIMESTAMP         NULL,
    item_record_version       INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_item_receipt_line_uuid UNIQUE (item_receipt_line_uuid),
    CONSTRAINT chk_irl_qty CHECK (
        qty_received >= 0 AND qty_rejected >= 0 AND qty_rejected <= qty_received
    )
);

-- item_receipt_history -- status/action trail (mirrors purchase_order_history)
CREATE TABLE IF NOT EXISTS item_receipt_history (
    item_receipt_history_id   SERIAL       PRIMARY KEY,
    item_receipt_id           INTEGER      NOT NULL REFERENCES item_receipt(item_receipt_id) ON DELETE CASCADE,
    from_status_id            INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id              INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                    VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | update | post | void
    actor_employee_id         INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                  JSONB        NOT NULL DEFAULT '{}',
    at                        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- inventory_ledger -- append-only stock movement for NON-serialized items,
-- the sibling of inventory_slab_ledger (which covers serialized slabs only).
-- Before this table, plain inventory_stock.quantity_on_hand had no audit trail
-- and no writer outside fabrication's slab path.
--
-- Invariant, identical to the slab ledger's:
--   inventory_stock.quantity_on_hand = SUM(quantity_delta) per (item, warehouse).
--
-- source_record_id / source_line_id are deliberately FK-free: the ledger is
-- polymorphic over source documents (item receipts today, vendor returns and
-- adjustments later) and a real FK cannot point at more than one table.
CREATE TABLE IF NOT EXISTS inventory_ledger (
    inventory_ledger_id SERIAL        PRIMARY KEY,
    inventory_item_id   INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    warehouse_id        INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    event               VARCHAR(20)   NOT NULL,
    quantity_delta      DECIMAL(14,3) NOT NULL,   -- signed, in the item's own unit
    source_record_type  INTEGER           NULL REFERENCES lkp_record_type(record_type_id),
    source_record_id    INTEGER           NULL,
    source_line_id      INTEGER           NULL,
    occurred_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor_employee_id   INTEGER           NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_inventory_ledger_event CHECK (event IN ('received','returned','adjusted','consumed'))
);

-- A source line may be received exactly once. Re-posting the same receipt
-- cannot double-count stock -- the bug is made unrepresentable, not tested
-- (same technique as uq_slab_ledger_received).
--
-- The key is (source_record_type, source_line_id), NOT source_line_id alone:
-- this table is polymorphic over source documents (see the comment above), so
-- a line id is only unique WITHIN a document type. Keying on the line id alone
-- means the first non-item-receipt document to post 'received' collides with an
-- unrelated item_receipt_line that happens to share an id -- and because
-- itemreceipt/inventory_post.go maps unique violations to
-- ErrMovementAlreadyApplied, the user is told a document they never posted was
-- already applied while stock is silently never incremented.
--
-- COALESCE(...,0) rather than a NOT NULL predicate on the record type, because
-- NULLs are DISTINCT in a unique index -- a NULL record type would silently
-- drop the guarantee for exactly those rows.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_ledger_src_line_received
    ON inventory_ledger (COALESCE(source_record_type, 0), source_line_id)
    WHERE event = 'received' AND source_line_id IS NOT NULL;
-- ...and reversed exactly once, for the same reason. Kept as a second index
-- rather than folding 'event' into one key, so a line may be received once AND
-- returned once.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_ledger_src_line_returned
    ON inventory_ledger (COALESCE(source_record_type, 0), source_line_id)
    WHERE event = 'returned' AND source_line_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_inventory_ledger_item_wh
    ON inventory_ledger (inventory_item_id, warehouse_id);
CREATE INDEX IF NOT EXISTS idx_inventory_ledger_source
    ON inventory_ledger (source_record_type, source_record_id);

-- item_receipt indexes (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_ir_po         ON item_receipt (purchase_order_id)        WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_vendor     ON item_receipt (item_receipt_vendor_id)   WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_status     ON item_receipt (item_receipt_status)      WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_warehouse  ON item_receipt (warehouse_id)             WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_owner      ON item_receipt (item_receipt_owner_id)    WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_date       ON item_receipt (item_receipt_date)        WHERE item_receipt_deleted_at IS NULL;
-- Keyset-cursor pairs: the query/ engine orders by (sort key, id).
CREATE INDEX IF NOT EXISTS idx_ir_created_id ON item_receipt (item_receipt_created_at, item_receipt_id) WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_updated_id ON item_receipt (item_receipt_updated_at, item_receipt_id) WHERE item_receipt_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ir_custom_gin ON item_receipt USING GIN (item_receipt_custom_fields);

CREATE INDEX IF NOT EXISTS idx_irl_ir   ON item_receipt_line (item_receipt_id)        WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_irl_poi  ON item_receipt_line (purchase_order_item_id);
CREATE INDEX IF NOT EXISTS idx_irl_item ON item_receipt_line (inventory_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_irl_line_active
    ON item_receipt_line (item_receipt_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_ir_history_ir ON item_receipt_history (item_receipt_id);

-- ---------------------------------------------------------------------
-- Relax purchase_order_item.chk_poi_qty_received (Item Receipt AD-3).
--
-- The Purchase Order module shipped `qty_received <= quantity` as a storage
-- constraint, which makes an over-delivery literally unrecordable. Receiving
-- 102 of 100 ordered is a real warehouse event, so the ceiling moves into Go
-- (itemreceipt.WithinTolerance + the item_receipt:approve override) and the
-- column keeps only its non-negativity guarantee.
--
-- This is a RELAXATION: every row that satisfied the old constraint satisfies
-- the new one, so it cannot fail on existing data and drops no data. The
-- CREATE TABLE above already carries the new form for fresh databases; this
-- guarded stanza converges databases provisioned before this migration.
-- Idempotent and re-runnable (the DO $$ precedent is at line ~1364).
-- ---------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_poi_qty_received'
          AND conrelid = 'purchase_order_item'::regclass
    ) THEN
        ALTER TABLE purchase_order_item DROP CONSTRAINT chk_poi_qty_received;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_poi_qty_received_nonneg'
          AND conrelid = 'purchase_order_item'::regclass
    ) THEN
        ALTER TABLE purchase_order_item
            ADD CONSTRAINT chk_poi_qty_received_nonneg CHECK (qty_received >= 0);
    END IF;
END $$;

-- ===========================================================================
-- CHART OF ACCOUNTS -- Finance section master data.
-- Spec: docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md
-- FK order: lkp_coa_category -> lkp_coa_subcategory -> coa_account
--           -> coa_account_history -> coa_default_mapping
-- ===========================================================================

-- lkp_coa_category -- fixed, seeded, read-only (AD-1). 9 rows.
CREATE TABLE IF NOT EXISTS lkp_coa_category (
    category_id             SERIAL      PRIMARY KEY,
    category_code           INTEGER     NOT NULL,
    category_name           VARCHAR(60) NOT NULL,
    category_range_low      INTEGER     NOT NULL,
    category_range_high     INTEGER     NOT NULL,
    category_normal_balance VARCHAR(6)  NOT NULL,
    category_sort_order     INTEGER     NOT NULL DEFAULT 0,
    CONSTRAINT uq_coa_category_code    UNIQUE (category_code),
    CONSTRAINT chk_coa_category_balance CHECK (category_normal_balance IN ('debit','credit')),
    CONSTRAINT chk_coa_category_range   CHECK (category_range_low < category_range_high)
);

-- lkp_coa_subcategory -- fixed, seeded, read-only (AD-1). 17 rows.
CREATE TABLE IF NOT EXISTS lkp_coa_subcategory (
    subcategory_id         SERIAL      PRIMARY KEY,
    category_id            INTEGER     NOT NULL REFERENCES lkp_coa_category(category_id),
    subcategory_code       INTEGER     NOT NULL,
    subcategory_name       VARCHAR(60) NOT NULL,
    subcategory_range_low  INTEGER     NOT NULL,
    subcategory_range_high INTEGER     NOT NULL,
    subcategory_sort_order INTEGER     NOT NULL DEFAULT 0,
    CONSTRAINT uq_coa_subcategory_code  UNIQUE (subcategory_code),
    CONSTRAINT chk_coa_subcategory_range CHECK (subcategory_range_low < subcategory_range_high)
);
CREATE INDEX IF NOT EXISTS idx_coa_subcat_category ON lkp_coa_subcategory (category_id);

INSERT INTO lkp_coa_category
    (category_code, category_name, category_range_low, category_range_high, category_normal_balance, category_sort_order) VALUES
    (1000,'Assets',                    1000,1999,'debit', 1),
    (2000,'Liabilities',               2000,2999,'credit',2),
    (3000,'Equity',                    3000,3999,'credit',3),
    (4000,'Revenue',                   4000,4999,'credit',4),
    (5000,'Cost of Goods Sold',        5000,5999,'debit', 5),
    (6000,'Operating Expenses',        6000,6999,'debit', 6),
    (7000,'Finance Costs',             7000,7999,'debit', 7),
    (8000,'Other Income',              8000,8999,'credit',8),
    (9000,'System & Control Accounts', 9000,9999,'debit', 9)
ON CONFLICT (category_code) DO NOTHING;

-- Resolves category_id by code so it never depends on serial values.
INSERT INTO lkp_coa_subcategory
    (category_id, subcategory_code, subcategory_name, subcategory_range_low, subcategory_range_high, subcategory_sort_order)
SELECT c.category_id, v.code, v.name, v.lo, v.hi, v.ord
FROM (VALUES
    (1000,1100,'Current Assets',                 1100,1199,1),
    (1000,1200,'Fixed Assets',                   1200,1299,2),
    (1000,1300,'Intangible Assets',              1300,1399,3),
    (2000,2100,'Current Liabilities',            2100,2199,1),
    (2000,2200,'Long-Term Liabilities',          2200,2299,2),
    (3000,3100,'Equity',                         3100,3199,1),
    (4000,4100,'Sales',                          4100,4199,1),
    (4000,4200,'Returns, Discounts & Allowances',4200,4299,2),
    (5000,5100,'Cost of Goods Sold',             5100,5199,1),
    (6000,6100,'Payroll',                        6100,6199,1),
    (6000,6200,'Administrative',                 6200,6299,2),
    (6000,6300,'Sales & Marketing',              6300,6399,3),
    (6000,6400,'Logistics',                      6400,6499,4),
    (6000,6500,'Depreciation',                   6500,6599,5),
    (7000,7100,'Finance Costs',                  7100,7199,1),
    (8000,8100,'Other Income',                   8100,8199,1),
    (9000,9100,'System & Control Accounts',      9100,9199,1)
) AS v(cat_code, code, name, lo, hi, ord)
JOIN lkp_coa_category c ON c.category_code = v.cat_code
ON CONFLICT (subcategory_code) DO NOTHING;

-- coa_account -- 127 seeded rows + everything users add.
CREATE TABLE IF NOT EXISTS coa_account (
    coa_account_id             SERIAL       PRIMARY KEY,
    coa_account_uuid           UUID         NOT NULL DEFAULT gen_random_uuid(),
    coa_account_code           VARCHAR(20)  NOT NULL,
    coa_account_name           VARCHAR(150) NOT NULL,
    coa_account_description    TEXT         NOT NULL DEFAULT '',
    subcategory_id             INTEGER      NOT NULL REFERENCES lkp_coa_subcategory(subcategory_id),
    parent_id                  INTEGER          NULL,
    coa_account_depth          SMALLINT     NOT NULL DEFAULT 0,
    coa_account_bs_pnl         VARCHAR(3)   NOT NULL,
    coa_account_type           VARCHAR(20)  NOT NULL DEFAULT 'general',
    coa_account_attributes     JSONB        NOT NULL DEFAULT '{}',
    coa_account_is_postable    BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_visible     BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    coa_account_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    coa_account_created_by     INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    coa_account_updated_by     INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_deleted_at     TIMESTAMP        NULL,
    coa_account_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_record_version INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_coa_account_uuid       UNIQUE (coa_account_uuid),
    -- AD-5: target of the composite self-FK below.
    CONSTRAINT uq_coa_account_id_subcat  UNIQUE (coa_account_id, subcategory_id),
    CONSTRAINT chk_coa_bs_pnl CHECK (coa_account_bs_pnl IN ('BS','PNL')),
    CONSTRAINT chk_coa_type   CHECK (coa_account_type IN
        ('general','bank','cash','credit_card','ar','ap','tax','inventory','fixed_asset')),
    -- AD-4: two-level cap. CHECK cannot subquery the parent's depth, so depth
    -- is a real column and depth 2 is unrepresentable.
    CONSTRAINT chk_coa_depth        CHECK (coa_account_depth IN (0,1)),
    CONSTRAINT chk_coa_depth_parent CHECK ((parent_id IS NULL) = (coa_account_depth = 0)),
    CONSTRAINT chk_coa_not_self     CHECK (parent_id IS NULL OR parent_id <> coa_account_id),
    -- AD-8: active implies visible.
    CONSTRAINT chk_coa_visibility CHECK (NOT (coa_account_is_active AND NOT coa_account_is_visible)),
    CONSTRAINT chk_coa_system_undeletable
        CHECK (NOT (coa_account_is_system AND coa_account_deleted_at IS NOT NULL)),
    -- Deliberately weaker than the chk_*_soft_delete on every other table,
    -- which requires deleted_at and deleted_by to be set together. The app no
    -- longer relies on that difference: an actor that resolveEmployeeID cannot
    -- map to an employee row (id 0, the common case while
    -- employee.employee_user_id goes unpopulated) now falls back to the seeded
    -- system employee, so this column is written NOT NULL like the others.
    -- The constraint stays relaxed only so already-provisioned tenants are not
    -- forced through an ALTER; the half of the invariant that matters is kept:
    -- a row may never claim a deleter without also being deleted.
    CONSTRAINT chk_coa_soft_delete CHECK (
        coa_account_deleted_by IS NULL OR coa_account_deleted_at IS NOT NULL
    ),
    -- AD-5: a child inherits its parent's sub-category, enforced by the database.
    -- MATCH SIMPLE (the default) satisfies the constraint whenever parent_id IS
    -- NULL, so top-level accounts are unaffected.
    CONSTRAINT fk_coa_parent_subcat FOREIGN KEY (parent_id, subcategory_id)
        REFERENCES coa_account (coa_account_id, subcategory_id)
);

-- AD-3: code unique among LIVE rows only. Name is deliberately NOT unique --
-- 5107 and 9104 are both "Inventory Adjustment".
CREATE UNIQUE INDEX IF NOT EXISTS uq_coa_account_code_live
    ON coa_account (coa_account_code) WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_subcat ON coa_account (subcategory_id)
    WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_parent ON coa_account (parent_id)
    WHERE coa_account_deleted_at IS NULL;
-- Serves the dropdown query: ?postable=true&active=true
CREATE INDEX IF NOT EXISTS idx_coa_account_dropdown ON coa_account (coa_account_code)
    WHERE coa_account_deleted_at IS NULL AND coa_account_is_active AND coa_account_is_postable;

-- coa_account_history -- append-only. coa_account_id is NULLable so a slot
-- repoint (not an account mutation) has somewhere to live.
CREATE TABLE IF NOT EXISTS coa_account_history (
    coa_account_history_id SERIAL      PRIMARY KEY,
    coa_account_id         INTEGER         NULL REFERENCES coa_account(coa_account_id),
    slot_key               VARCHAR(50)     NULL,
    history_action         VARCHAR(20) NOT NULL,
    history_field          VARCHAR(60) NOT NULL DEFAULT '',
    history_old_value      TEXT        NOT NULL DEFAULT '',
    history_new_value      TEXT        NOT NULL DEFAULT '',
    history_at             TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by             INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_coa_history_action CHECK (history_action IN
        ('create','update','delete','activate','deactivate','show','hide','repoint_slot')),
    CONSTRAINT chk_coa_history_target CHECK (coa_account_id IS NOT NULL OR slot_key IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_coa_history_account ON coa_account_history (coa_account_id, history_at DESC);
CREATE INDEX IF NOT EXISTS idx_coa_history_slot    ON coa_account_history (slot_key, history_at DESC);

-- coa_default_mapping -- 19 named slots. The "points at a postable+active
-- account" rule is enforced in the store, not here: a FK cannot express a
-- predicate on the referenced row (AD-7).
CREATE TABLE IF NOT EXISTS coa_default_mapping (
    slot_key         VARCHAR(50)  PRIMARY KEY,
    slot_label       VARCHAR(100) NOT NULL,
    slot_description TEXT         NOT NULL DEFAULT '',
    coa_account_id   INTEGER          NULL REFERENCES coa_account(coa_account_id),
    slot_is_system   BOOLEAN      NOT NULL DEFAULT TRUE,
    slot_sort_order  INTEGER      NOT NULL DEFAULT 0,
    slot_updated_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    slot_updated_by  INTEGER          NULL REFERENCES employee(employee_id)
);
CREATE INDEX IF NOT EXISTS idx_coa_slot_account ON coa_default_mapping (coa_account_id);

-- ── Cash Transfer module + GL foundation (journal/) ────────────────────

-- accounting_settings -- singleton; the entire "closed period" concept --------
CREATE TABLE IF NOT EXISTS accounting_settings (
    accounting_settings_id      SMALLINT     PRIMARY KEY DEFAULT 1,
    books_closed_through        DATE             NULL,
    accounting_settings_updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accounting_settings_updated_by INTEGER       NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_accounting_settings_singleton CHECK (accounting_settings_id = 1)
);
INSERT INTO accounting_settings (accounting_settings_id) VALUES (1) ON CONFLICT DO NOTHING;

-- journal_entry -- the GL posting header --------------------------------------
CREATE TABLE IF NOT EXISTS journal_entry (
    journal_entry_id          SERIAL       PRIMARY KEY,
    journal_entry_uuid        UUID         NOT NULL DEFAULT gen_random_uuid(),
    journal_entry_number      VARCHAR(20)      NULL,
    entry_date                 DATE         NOT NULL,
    memo                       TEXT         NOT NULL DEFAULT '',
    source_type                 VARCHAR(30)  NOT NULL,
    source_id                    UUID         NOT NULL,
    is_reversal                   BOOLEAN      NOT NULL DEFAULT FALSE,
    reverses_journal_entry_id      INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    journal_entry_created_at        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    journal_entry_created_by         INTEGER          NULL REFERENCES employee(employee_id),

    CONSTRAINT uq_je_uuid   UNIQUE (journal_entry_uuid),
    CONSTRAINT uq_je_number UNIQUE (journal_entry_number)
);
CREATE INDEX IF NOT EXISTS idx_je_source ON journal_entry (source_type, source_id);

-- journal_entry_line -- one debit or credit leg -------------------------------
CREATE TABLE IF NOT EXISTS journal_entry_line (
    journal_entry_line_id SERIAL        PRIMARY KEY,
    journal_entry_id       INTEGER       NOT NULL REFERENCES journal_entry(journal_entry_id),
    line_number              INTEGER       NOT NULL,
    coa_account_id             INTEGER       NOT NULL REFERENCES coa_account(coa_account_id),
    debit                        DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit                        DECIMAL(15,2) NOT NULL DEFAULT 0,

    CONSTRAINT uq_jel_line      UNIQUE (journal_entry_id, line_number),
    CONSTRAINT chk_jel_nonneg   CHECK (debit >= 0 AND credit >= 0),
    CONSTRAINT chk_jel_one_side CHECK (NOT (debit > 0 AND credit > 0)),
    CONSTRAINT chk_jel_nonzero  CHECK (debit > 0 OR credit > 0)
);
CREATE INDEX IF NOT EXISTS idx_jel_account ON journal_entry_line (coa_account_id);
CREATE INDEX IF NOT EXISTS idx_jel_entry   ON journal_entry_line (journal_entry_id);

-- coa_account running balance -------------------------------------------------
ALTER TABLE coa_account ADD COLUMN IF NOT EXISTS coa_account_balance DECIMAL(15,2) NOT NULL DEFAULT 0;

-- New record type for Cash Transfer, appended as its own statement -----------
INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name, record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('CTRF', 'cashtransfer', 'Cash Transfer', TRUE, TRUE, 1)
ON CONFLICT (record_type_code) DO NOTHING;

INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES
    ('DRFT','Draft'), ('APPR','Approved'), ('POST','Posted'),
    ('CANC','Cancelled'), ('RVSD','Reversed')
) AS v(code, name)
CROSS JOIN lkp_record_type rt
WHERE rt.record_type_code = 'CTRF'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- cash_transfer -- header ------------------------------------------------------
CREATE TABLE IF NOT EXISTS cash_transfer (
    cash_transfer_id            SERIAL       PRIMARY KEY,
    cash_transfer_uuid           UUID         NOT NULL DEFAULT gen_random_uuid(),
    cash_transfer_number          VARCHAR(20)      NULL,
    record_type                    INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    cash_transfer_status             INTEGER      NOT NULL REFERENCES lkp_record_status(record_status_id),
    cash_transfer_date                 DATE         NOT NULL DEFAULT CURRENT_DATE,
    from_account_id                      INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    to_account_id                          INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    cash_transfer_amount                    DECIMAL(15,2) NOT NULL,
    cash_transfer_reference                   VARCHAR(100) NOT NULL DEFAULT '',
    cash_transfer_notes                         TEXT         NOT NULL DEFAULT '',
    cash_transfer_internal_notes                  TEXT         NOT NULL DEFAULT '',
    cash_transfer_custom_fields                     JSONB        NOT NULL DEFAULT '{}',
    cash_transfer_owner_id                            INTEGER          NULL REFERENCES employee(employee_id),
    journal_entry_id                                    INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    reversal_journal_entry_id                             INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    cash_transfer_posted_at                                 TIMESTAMP        NULL,
    cash_transfer_posted_by                                   INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_reversed_at                                   TIMESTAMP        NULL,
    cash_transfer_reversed_by                                     INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_created_at                                       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cash_transfer_created_by                                         INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_updated_at                                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cash_transfer_updated_by                                             INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_deleted_at                                               TIMESTAMP        NULL,
    cash_transfer_deleted_by                                                 INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_record_version                                               INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_cash_transfer_uuid   UNIQUE (cash_transfer_uuid),
    CONSTRAINT uq_cash_transfer_number UNIQUE (cash_transfer_number),
    CONSTRAINT chk_ct_diff_accounts CHECK (from_account_id <> to_account_id),
    CONSTRAINT chk_ct_amount_positive CHECK (cash_transfer_amount > 0),
    CONSTRAINT chk_ct_posted_pair   CHECK ((cash_transfer_posted_at IS NULL) = (journal_entry_id IS NULL)),
    CONSTRAINT chk_ct_reversed_pair CHECK ((cash_transfer_reversed_at IS NULL) = (reversal_journal_entry_id IS NULL)),
    CONSTRAINT chk_ct_soft_delete CHECK (
        (cash_transfer_deleted_at IS NULL AND cash_transfer_deleted_by IS NULL) OR
        (cash_transfer_deleted_at IS NOT NULL AND cash_transfer_deleted_by IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_ct_status  ON cash_transfer (cash_transfer_status) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_from    ON cash_transfer (from_account_id)      WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_to      ON cash_transfer (to_account_id)        WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_owner   ON cash_transfer (cash_transfer_owner_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_custom_gin ON cash_transfer USING GIN (cash_transfer_custom_fields);
CREATE INDEX IF NOT EXISTS idx_ct_created_keyset ON cash_transfer (cash_transfer_created_at, cash_transfer_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_updated_keyset ON cash_transfer (cash_transfer_updated_at, cash_transfer_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_number_keyset  ON cash_transfer (cash_transfer_number, cash_transfer_id)     WHERE cash_transfer_deleted_at IS NULL;

-- cash_transfer_history -- status trail ---------------------------------------
CREATE TABLE IF NOT EXISTS cash_transfer_history (
    cash_transfer_history_id SERIAL      PRIMARY KEY,
    cash_transfer_id           INTEGER     NOT NULL REFERENCES cash_transfer(cash_transfer_id),
    from_status_id               INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    history_action                   VARCHAR(20) NOT NULL,
    history_at                         TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by                           INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_ct_history_action CHECK (history_action IN
        ('create','update','transition','post','reverse','delete'))
);
CREATE INDEX IF NOT EXISTS idx_ct_history_record ON cash_transfer_history (cash_transfer_id, history_at DESC);

-- All seeded rows are top-level (parent_id NULL, depth 0), so there is no
-- insert-ordering problem. created_by is employee 1, matching lkp_unit/lkp_warehouse.
-- Note 'Partner''s Capital' doubles the apostrophe (correct SQL escaping), and
-- 9106 seeds inactive (AD-11): meaningless under the single-subsidiary policy.
INSERT INTO coa_account
    (coa_account_code, coa_account_name, subcategory_id, coa_account_bs_pnl,
     coa_account_type, coa_account_is_active, coa_account_is_system, coa_account_created_by)
SELECT v.code, v.name, s.subcategory_id, v.bs_pnl, v.acct_type, v.active, TRUE, 1
FROM (VALUES
    -- Current Assets (1100) -- 19
    ('1101','Cash on Hand',                1100,'BS','cash',       TRUE),
    ('1102','Petty Cash',                  1100,'BS','cash',       TRUE),
    ('1103','Bank Account - Operating',    1100,'BS','bank',       TRUE),
    ('1104','Bank Account - Payroll',      1100,'BS','bank',       TRUE),
    ('1105','Bank Account - Tax',          1100,'BS','bank',       TRUE),
    ('1110','Undeposited Funds',           1100,'BS','general',    TRUE),
    ('1120','Accounts Receivable',         1100,'BS','ar',         TRUE),
    ('1121','Allowance for Doubtful Debts',1100,'BS','general',    TRUE),
    ('1130','Employee Advances',           1100,'BS','general',    TRUE),
    ('1135','Vendor Advances',             1100,'BS','general',    TRUE),
    ('1140','Sales Tax Receivable',        1100,'BS','tax',        TRUE),
    ('1141','Sales Tax Refund Receivable', 1100,'BS','tax',        TRUE),
    ('1150','Prepaid Expenses',            1100,'BS','general',    TRUE),
    ('1160','Accrued Income',              1100,'BS','general',    TRUE),
    ('1170','Inventory - Raw Materials',   1100,'BS','inventory',  TRUE),
    ('1171','Inventory - WIP',             1100,'BS','inventory',  TRUE),
    ('1172','Inventory - Finished Goods',  1100,'BS','inventory',  TRUE),
    ('1173','Inventory - Trading Goods',   1100,'BS','inventory',  TRUE),
    ('1180','Short-term Investments',      1100,'BS','general',    TRUE),
    -- Fixed Assets (1200) -- 9
    ('1201','Land',                        1200,'BS','fixed_asset',TRUE),
    ('1202','Building',                    1200,'BS','fixed_asset',TRUE),
    ('1203','Office Equipment',            1200,'BS','fixed_asset',TRUE),
    ('1204','Computers',                   1200,'BS','fixed_asset',TRUE),
    ('1205','Furniture & Fixtures',        1200,'BS','fixed_asset',TRUE),
    ('1206','Vehicles',                    1200,'BS','fixed_asset',TRUE),
    ('1207','Plant & Machinery',           1200,'BS','fixed_asset',TRUE),
    ('1208','Leasehold Improvements',      1200,'BS','fixed_asset',TRUE),
    ('1210','Accumulated Depreciation',    1200,'BS','general',    TRUE),
    -- Intangible Assets (1300) -- 6
    ('1301','Software',                    1300,'BS','general',    TRUE),
    ('1302','ERP Development Cost',        1300,'BS','general',    TRUE),
    ('1303','Patents',                     1300,'BS','general',    TRUE),
    ('1304','Trademark',                   1300,'BS','general',    TRUE),
    ('1305','Goodwill',                    1300,'BS','general',    TRUE),
    ('1310','Accumulated Amortization',    1300,'BS','general',    TRUE),
    -- Current Liabilities (2100) -- 13
    ('2101','Accounts Payable',            2100,'BS','ap',         TRUE),
    ('2102','Credit Card Payable',         2100,'BS','credit_card',TRUE),
    ('2110','Accrued Expenses',            2100,'BS','general',    TRUE),
    ('2120','Salary Payable',              2100,'BS','general',    TRUE),
    ('2121','Bonus Payable',               2100,'BS','general',    TRUE),
    ('2122','Leave Encashment Payable',    2100,'BS','general',    TRUE),
    ('2130','Payroll Taxes Payable',       2100,'BS','general',    TRUE),
    ('2140','Sales Tax Payable',           2100,'BS','tax',        TRUE),
    ('2141','Withholding Tax Payable',     2100,'BS','tax',        TRUE),
    ('2150','Customer Advances',           2100,'BS','general',    TRUE),
    ('2160','Deferred Revenue',            2100,'BS','general',    TRUE),
    ('2170','Short-term Loan',             2100,'BS','general',    TRUE),
    ('2180','Interest Payable',            2100,'BS','general',    TRUE),
    -- Long-Term Liabilities (2200) -- 5
    ('2201','Bank Loan',                   2200,'BS','general',    TRUE),
    ('2202','Mortgage Loan',               2200,'BS','general',    TRUE),
    ('2203','Lease Liability',             2200,'BS','general',    TRUE),
    ('2204','Shareholder Loan',            2200,'BS','general',    TRUE),
    ('2205','Deferred Tax Liability',      2200,'BS','general',    TRUE),
    -- Equity (3100) -- 7
    ('3101','Capital',                     3100,'BS','general',    TRUE),
    ('3102','Partner''s Capital',          3100,'BS','general',    TRUE),
    ('3103','Share Capital',               3100,'BS','general',    TRUE),
    ('3110','Retained Earnings',           3100,'BS','general',    TRUE),
    ('3120','Current Year Earnings',       3100,'BS','general',    TRUE),
    ('3130','Additional Paid-in Capital',  3100,'BS','general',    TRUE),
    ('3140','Dividend Distribution',       3100,'BS','general',    TRUE),
    -- Sales (4100) -- 8
    ('4101','Product Sales',               4100,'PNL','general',   TRUE),
    ('4102','Service Revenue',             4100,'PNL','general',   TRUE),
    ('4103','Consulting Revenue',          4100,'PNL','general',   TRUE),
    ('4104','Subscription Revenue',        4100,'PNL','general',   TRUE),
    ('4105','Maintenance Revenue',         4100,'PNL','general',   TRUE),
    ('4106','Installation Revenue',        4100,'PNL','general',   TRUE),
    ('4107','Export Sales',                4100,'PNL','general',   TRUE),
    ('4108','Domestic Sales',              4100,'PNL','general',   TRUE),
    -- Returns, Discounts & Allowances (4200) -- 3
    ('4201','Sales Returns',               4200,'PNL','general',   TRUE),
    ('4202','Sales Discount',              4200,'PNL','general',   TRUE),
    ('4203','Sales Allowance',             4200,'PNL','general',   TRUE),
    -- Cost of Goods Sold (5100) -- 8
    ('5101','Opening Inventory',           5100,'PNL','general',   TRUE),
    ('5102','Purchases',                   5100,'PNL','general',   TRUE),
    ('5103','Direct Labor',                5100,'PNL','general',   TRUE),
    ('5104','Direct Material',             5100,'PNL','general',   TRUE),
    ('5105','Freight Inward',              5100,'PNL','general',   TRUE),
    ('5106','Manufacturing Overheads',     5100,'PNL','general',   TRUE),
    ('5107','Inventory Adjustment',        5100,'PNL','general',   TRUE),
    ('5108','Closing Inventory',           5100,'PNL','general',   TRUE),
    -- Payroll (6100) -- 5
    ('6101','Salaries',                    6100,'PNL','general',   TRUE),
    ('6102','Wages',                       6100,'PNL','general',   TRUE),
    ('6103','Payroll Taxes',               6100,'PNL','general',   TRUE),
    ('6104','Employee Benefits',           6100,'PNL','general',   TRUE),
    ('6105','Recruitment',                 6100,'PNL','general',   TRUE),
    -- Administrative (6200) -- 18
    ('6201','Rent',                        6200,'PNL','general',   TRUE),
    ('6202','Electricity',                 6200,'PNL','general',   TRUE),
    ('6203','Internet',                    6200,'PNL','general',   TRUE),
    ('6204','Telephone',                   6200,'PNL','general',   TRUE),
    ('6205','Office Supplies',             6200,'PNL','general',   TRUE),
    ('6206','Printing',                    6200,'PNL','general',   TRUE),
    ('6207','Repairs & Maintenance',       6200,'PNL','general',   TRUE),
    ('6208','Insurance',                   6200,'PNL','general',   TRUE),
    ('6209','Professional Fees',           6200,'PNL','general',   TRUE),
    ('6210','Audit Fees',                  6200,'PNL','general',   TRUE),
    ('6211','Legal Fees',                  6200,'PNL','general',   TRUE),
    ('6212','Bank Charges',                6200,'PNL','general',   TRUE),
    ('6213','Software Subscription',       6200,'PNL','general',   TRUE),
    ('6214','Travel',                      6200,'PNL','general',   TRUE),
    ('6215','Meals & Entertainment',       6200,'PNL','general',   TRUE),
    ('6216','Training',                    6200,'PNL','general',   TRUE),
    ('6217','Licenses',                    6200,'PNL','general',   TRUE),
    ('6218','Security',                    6200,'PNL','general',   TRUE),
    -- Sales & Marketing (6300) -- 5
    ('6301','Advertising',                 6300,'PNL','general',   TRUE),
    ('6302','Digital Marketing',           6300,'PNL','general',   TRUE),
    ('6303','Sales Commission',            6300,'PNL','general',   TRUE),
    ('6304','Promotional Expenses',        6300,'PNL','general',   TRUE),
    ('6305','Customer Gifts',              6300,'PNL','general',   TRUE),
    -- Logistics (6400) -- 3
    ('6401','Freight Outward',             6400,'PNL','general',   TRUE),
    ('6402','Courier Charges',             6400,'PNL','general',   TRUE),
    ('6403','Delivery Expenses',           6400,'PNL','general',   TRUE),
    -- Depreciation (6500) -- 2
    ('6501','Depreciation Expense',        6500,'PNL','general',   TRUE),
    ('6502','Amortization Expense',        6500,'PNL','general',   TRUE),
    -- Finance Costs (7100) -- 4
    ('7101','Interest Expense',            7100,'PNL','general',   TRUE),
    ('7102','Loan Processing Charges',     7100,'PNL','general',   TRUE),
    ('7103','Foreign Exchange Loss',       7100,'PNL','general',   TRUE),
    ('7104','Credit Card Charges',         7100,'PNL','general',   TRUE),
    -- Other Income (8100) -- 5
    ('8101','Interest Income',             8100,'PNL','general',   TRUE),
    ('8102','Dividend Income',             8100,'PNL','general',   TRUE),
    ('8103','Foreign Exchange Gain',       8100,'PNL','general',   TRUE),
    ('8104','Gain on Asset Sale',          8100,'PNL','general',   TRUE),
    ('8105','Miscellaneous Income',        8100,'PNL','general',   TRUE),
    -- System & Control (9100) -- 7. The ONLY sub-category mixing BS and PNL (AD-2).
    -- 9106 seeds INACTIVE: meaningless under the single-subsidiary policy.
    ('9101','Opening Balance Equity',      9100,'BS','general',    TRUE),
    ('9102','Suspense Account',            9100,'BS','general',    TRUE),
    ('9103','Rounding Adjustment',         9100,'PNL','general',   TRUE),
    ('9104','Inventory Adjustment',        9100,'PNL','general',   TRUE),
    ('9105','Exchange Rate Adjustment',    9100,'PNL','general',   TRUE),
    ('9106','Intercompany Clearing',       9100,'PNL','general',   FALSE),
    ('9107','Cash Difference',             9100,'PNL','general',   TRUE)
) AS v(code, name, subcat_code, bs_pnl, acct_type, active)
JOIN lkp_coa_subcategory s ON s.subcategory_code = v.subcat_code
ON CONFLICT DO NOTHING;

-- Resolves the target account by code, so it stays independent of serial values.
INSERT INTO coa_default_mapping (slot_key, slot_label, slot_description, coa_account_id, slot_is_system, slot_sort_order)
SELECT v.key, v.label, v.descr, a.coa_account_id, TRUE, v.ord
FROM (VALUES
    ('default_ar',                  'Accounts Receivable',   'Customer balances owed to the company.',    '1120', 1),
    ('default_ap',                  'Accounts Payable',      'Balances owed to vendors.',                 '2101', 2),
    ('default_sales_revenue',       'Sales Revenue',         'Default revenue account for sales.',        '4101', 3),
    ('default_sales_discount',      'Sales Discount',        'Discounts granted on sales.',               '4202', 4),
    ('default_sales_returns',       'Sales Returns',         'Value of goods returned by customers.',     '4201', 5),
    ('default_cogs',                'Cost of Goods Sold',    'Default COGS account.',                     '5104', 6),
    ('default_inventory',           'Inventory',             'Default inventory asset account.',          '1172', 7),
    ('default_bank',                'Bank',                  'Default bank account for receipts.',        '1103', 8),
    ('default_undeposited_funds',   'Undeposited Funds',     'Holding account for uncleared receipts.',   '1110', 9),
    ('default_sales_tax_payable',   'Sales Tax Payable',     'Sales tax collected and owed.',             '2140',10),
    ('default_sales_tax_receivable','Sales Tax Receivable',  'Sales tax paid and recoverable.',           '1140',11),
    ('default_deferred_revenue',    'Deferred Revenue',      'Revenue billed but not yet earned.',        '2160',12),
    ('default_customer_advances',   'Customer Advances',     'Payments received before delivery.',        '2150',13),
    ('default_freight_out',         'Freight Outward',       'Outbound shipping cost.',                   '6401',14),
    ('default_bank_charges',        'Bank Charges',          'Bank fees.',                                '6212',15),
    ('default_fx_gain',             'Foreign Exchange Gain', 'Gain on currency conversion.',              '8103',16),
    ('default_fx_loss',             'Foreign Exchange Loss', 'Loss on currency conversion.',              '7103',17),
    ('default_rounding',            'Rounding Adjustment',   'Absorbs sub-cent rounding differences.',    '9103',18),
    ('default_suspense',            'Suspense',              'Holds entries pending correct classification.','9102',19)
) AS v(key, label, descr, acct_code, ord)
JOIN coa_account a ON a.coa_account_code = v.acct_code AND a.coa_account_deleted_at IS NULL
ON CONFLICT (slot_key) DO NOTHING;


-- ===========================================================================
-- INVENTORY MANAGEMENT -- warehouse/bin locations, stone attributes on the
-- item catalogue, and the generalisation of inventory_slab into a general
-- serialized inventory unit.
--
-- Spec: docs/superpowers/specs/2026-07-26-inventory-module-design.md
--
-- This section is Phase 1 (schema) only. Phase 2 adds CRUD for the item stone
-- attributes, warehouses, bins, units, bundles and the lkp_* vocabularies.
-- Phase 3 (warehouse transfer, stock adjustment, cycle count) is a later
-- branch; only its reason-code lookup and record types are seeded here (AD-6).
--
-- FK order below is load-bearing:
--   lkp_* vocab -> inventory_bin -> inventory_bundle -> ALTERs on
--   inventory_item / inventory_slab -> history -> ledger index repair ->
--   record types -> RBAC backfill
--
-- Columns are added by ALTER ... ADD COLUMN IF NOT EXISTS, never by editing
-- the existing CREATE TABLE bodies (lines 2376 and 4196): CREATE TABLE IF NOT
-- EXISTS is a no-op on every existing tenant, so a column added there would
-- reach fresh databases only and diverge permanently. See spec AD-7.
-- ===========================================================================

-- ---------------------------------------------------------------------
-- 1. Controlled-vocabulary lookups. All follow the lkp_unit shape (line 2299).
-- ---------------------------------------------------------------------

-- lkp_material ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_material (
    material_id             SERIAL       PRIMARY KEY,
    material_name           VARCHAR(60)  NOT NULL,
    material_code           VARCHAR(10)  NOT NULL,
    material_is_porous      BOOLEAN      NOT NULL DEFAULT TRUE,   -- drives the sealing step
    material_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    material_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    material_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    material_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    material_updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    material_updated_by     INTEGER          NULL REFERENCES employee(employee_id),
    material_deleted_at     TIMESTAMP        NULL,
    material_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    material_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_material_code UNIQUE (material_code),
    CONSTRAINT chk_material_soft_delete CHECK (
        (material_deleted_at IS NULL AND material_deleted_by IS NULL) OR
        (material_deleted_at IS NOT NULL AND material_deleted_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_material_active ON lkp_material (material_is_active)
    WHERE material_deleted_at IS NULL;

INSERT INTO lkp_material (material_name, material_code, material_is_porous, material_is_system, material_created_by) VALUES
    ('Granite','GRAN',TRUE,TRUE,1),              ('Marble','MARB',TRUE,TRUE,1),
    ('Quartz (Engineered)','QRTZ',FALSE,TRUE,1), ('Quartzite','QTZT',TRUE,TRUE,1),
    ('Soapstone','SOAP',FALSE,TRUE,1),           ('Porcelain','PORC',FALSE,TRUE,1),
    ('Sintered Stone','SINT',FALSE,TRUE,1),      ('Dolomite','DOLO',TRUE,TRUE,1),
    ('Onyx','ONYX',TRUE,TRUE,1),                 ('Travertine','TRAV',TRUE,TRUE,1),
    ('Limestone','LIME',TRUE,TRUE,1),            ('Slate','SLAT',TRUE,TRUE,1)
ON CONFLICT (material_code) DO NOTHING;

-- lkp_color ---------------------------------------------------------------
-- Deliberately seeded EMPTY. Colour names are vendor catalogue names; a guessed
-- seed set collides with the tenant's real import and leaves dead rows that no
-- partial-unique index can distinguish from live ones.
CREATE TABLE IF NOT EXISTS lkp_color (
    color_id             SERIAL       PRIMARY KEY,
    color_name           VARCHAR(80)  NOT NULL,
    color_code           VARCHAR(20)  NOT NULL,
    color_hex            VARCHAR(7)   NOT NULL DEFAULT '',   -- '#RRGGBB' swatch, '' = none
    color_material_id    INTEGER          NULL REFERENCES lkp_material(material_id),
    color_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    color_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    color_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    color_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    color_updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    color_updated_by     INTEGER          NULL REFERENCES employee(employee_id),
    color_deleted_at     TIMESTAMP        NULL,
    color_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    color_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_color_code UNIQUE (color_code),
    CONSTRAINT chk_color_hex CHECK (color_hex = '' OR color_hex ~ '^#[0-9A-Fa-f]{6}$'),
    CONSTRAINT chk_color_soft_delete CHECK (
        (color_deleted_at IS NULL AND color_deleted_by IS NULL) OR
        (color_deleted_at IS NOT NULL AND color_deleted_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_color_material ON lkp_color (color_material_id) WHERE color_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_color_name     ON lkp_color (LOWER(color_name));

-- lkp_finish --------------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_finish (
    finish_id             SERIAL       PRIMARY KEY,
    finish_name           VARCHAR(60)  NOT NULL,
    finish_code           VARCHAR(10)  NOT NULL,
    finish_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    finish_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    finish_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finish_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    finish_updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finish_updated_by     INTEGER          NULL REFERENCES employee(employee_id),
    finish_deleted_at     TIMESTAMP        NULL,
    finish_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    finish_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_finish_code UNIQUE (finish_code),
    CONSTRAINT chk_finish_soft_delete CHECK (
        (finish_deleted_at IS NULL AND finish_deleted_by IS NULL) OR
        (finish_deleted_at IS NOT NULL AND finish_deleted_by IS NOT NULL))
);
INSERT INTO lkp_finish (finish_name, finish_code, finish_is_system, finish_created_by) VALUES
    ('Polished','POL',TRUE,1), ('Honed','HON',TRUE,1),   ('Leathered','LEA',TRUE,1),
    ('Brushed','BRU',TRUE,1),  ('Flamed','FLA',TRUE,1),  ('Sandblasted','SAND',TRUE,1),
    ('Antiqued','ANT',TRUE,1), ('Sawn / Raw','SAW',TRUE,1)
ON CONFLICT (finish_code) DO NOTHING;

-- lkp_inventory_reason ----------------------------------------------------
-- Phase 3 (adjustment/transfer/count) is the main writer; created now because
-- it is a pure lookup with no workflow, and Phase 2's scrap and cut paths
-- already need a reason code (AD-6).
CREATE TABLE IF NOT EXISTS lkp_inventory_reason (
    inventory_reason_id             SERIAL       PRIMARY KEY,
    inventory_reason_name           VARCHAR(60)  NOT NULL,
    inventory_reason_code           VARCHAR(10)  NOT NULL,
    -- which document may cite it: adjustment | transfer | count | scrap | any
    inventory_reason_applies_to     VARCHAR(12)  NOT NULL DEFAULT 'any',
    -- which direction it may move stock: increase | decrease | both
    inventory_reason_direction      VARCHAR(10)  NOT NULL DEFAULT 'both',
    -- GL account when posting; NULL = fall back to the COA slot default_inventory
    inventory_reason_coa_account_id INTEGER          NULL REFERENCES coa_account(coa_account_id),
    inventory_reason_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    inventory_reason_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    inventory_reason_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_reason_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    inventory_reason_updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_reason_updated_by     INTEGER          NULL REFERENCES employee(employee_id),
    inventory_reason_deleted_at     TIMESTAMP        NULL,
    inventory_reason_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    inventory_reason_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_reason_code UNIQUE (inventory_reason_code),
    CONSTRAINT chk_inv_reason_applies   CHECK (inventory_reason_applies_to IN
        ('adjustment','transfer','count','scrap','any')),
    CONSTRAINT chk_inv_reason_direction CHECK (inventory_reason_direction IN
        ('increase','decrease','both')),
    CONSTRAINT chk_inv_reason_soft_delete CHECK (
        (inventory_reason_deleted_at IS NULL AND inventory_reason_deleted_by IS NULL) OR
        (inventory_reason_deleted_at IS NOT NULL AND inventory_reason_deleted_by IS NOT NULL))
);
INSERT INTO lkp_inventory_reason (inventory_reason_name, inventory_reason_code,
    inventory_reason_applies_to, inventory_reason_direction, inventory_reason_is_system, inventory_reason_created_by) VALUES
    ('Damage',               'DMG',  'adjustment','decrease',TRUE,1),
    ('Breakage',             'BRKG', 'scrap',     'decrease',TRUE,1),
    ('Theft',                'THFT', 'adjustment','decrease',TRUE,1),
    ('Shrinkage',            'SHRK', 'adjustment','decrease',TRUE,1),
    ('Scrap',                'SCRP', 'scrap',     'decrease',TRUE,1),
    ('Found',                'FOUND','adjustment','increase',TRUE,1),
    ('Recount',              'RCNT', 'count',     'both',    TRUE,1),
    ('Cycle Count Variance', 'CCV',  'count',     'both',    TRUE,1),
    ('Warehouse Transfer',   'WHTR', 'transfer',  'both',    TRUE,1),
    ('Data Entry Correction','CORR', 'adjustment','both',    TRUE,1)
ON CONFLICT (inventory_reason_code) DO NOTHING;

-- ---------------------------------------------------------------------
-- 2. inventory_bin -- a physical location inside a warehouse (AD-1).
--
-- Flat + typed + optionally self-nesting rather than a fixed zone/aisle/rack/
-- shelf hierarchy, because a stone yard's depth is not uniform: an A-frame slot
-- is 3 levels deep, a quartz shelf 2, receiving staging 1. A fixed hierarchy
-- forces synthetic filler rows for every missing level.
--
-- Bins locate SERIALIZED units only (AD-2). inventory_stock is NOT re-keyed and
-- stays UNIQUE(inventory_item_id, warehouse_id) -- so a bin move is stock-neutral
-- by construction and writes NO ledger row, only an inventory_unit_history row.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory_bin (
    inventory_bin_id        SERIAL        PRIMARY KEY,
    inventory_bin_uuid      UUID          NOT NULL DEFAULT gen_random_uuid(),
    warehouse_id            INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    bin_code                VARCHAR(30)   NOT NULL,
    bin_name                VARCHAR(100)  NOT NULL DEFAULT '',
    bin_type                VARCHAR(20)   NOT NULL DEFAULT 'rack',
    bin_parent_id           INTEGER           NULL REFERENCES inventory_bin(inventory_bin_id),
    -- Materialized ancestor path, '/'-joined codes incl. self: 'YARD-A/AF-03/SLOT-7'.
    -- Maintained by inventory/bin_path.go so the common read needs no recursion.
    bin_path                VARCHAR(200)  NOT NULL DEFAULT '',
    bin_depth               SMALLINT      NOT NULL DEFAULT 0,   -- 0 = top level
    -- Capacity hints, ADVISORY only: over-capacity warns, never blocks. A yard
    -- crew that must physically put a slab somewhere cannot be blocked by a row
    -- count, and a hard block guarantees they invent a junk bin to work around
    -- it -- worse data than an accurate over-capacity flag.
    bin_capacity_units      INTEGER       NOT NULL DEFAULT 0,   -- 0 = unlimited
    bin_capacity_area       DECIMAL(14,3) NOT NULL DEFAULT 0,   -- 0 = unlimited
    bin_capacity_unit_id    INTEGER           NULL REFERENCES lkp_unit(unit_id),
    bin_is_active           BOOLEAN       NOT NULL DEFAULT TRUE,
    bin_is_system           BOOLEAN       NOT NULL DEFAULT FALSE,
    bin_notes               TEXT          NOT NULL DEFAULT '',
    bin_created_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    bin_created_by          INTEGER           NULL REFERENCES employee(employee_id),
    bin_updated_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    bin_updated_by          INTEGER           NULL REFERENCES employee(employee_id),
    bin_deleted_at          TIMESTAMP         NULL,
    bin_deleted_by          INTEGER           NULL REFERENCES employee(employee_id),
    bin_record_version      INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_bin_uuid UNIQUE (inventory_bin_uuid),
    CONSTRAINT chk_bin_type CHECK (bin_type IN
        ('yard','rack','aframe','aisle','shelf','floor','staging')),
    CONSTRAINT chk_bin_not_self CHECK (bin_parent_id IS DISTINCT FROM inventory_bin_id),
    CONSTRAINT chk_bin_depth    CHECK (bin_depth >= 0 AND bin_depth <= 4),
    CONSTRAINT chk_bin_capacity CHECK (bin_capacity_units >= 0 AND bin_capacity_area >= 0),
    CONSTRAINT chk_bin_soft_delete CHECK (
        (bin_deleted_at IS NULL AND bin_deleted_by IS NULL) OR
        (bin_deleted_at IS NOT NULL AND bin_deleted_by IS NOT NULL))
);
-- Code unique per warehouse among LIVE rows only, matching
-- uq_inventory_item_sku_active (line 2407): a code frees up on soft delete.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_bin_code_active
    ON inventory_bin (warehouse_id, LOWER(bin_code)) WHERE bin_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bin_warehouse ON inventory_bin (warehouse_id, bin_is_active) WHERE bin_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bin_parent    ON inventory_bin (bin_parent_id)                WHERE bin_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bin_path      ON inventory_bin (bin_path varchar_pattern_ops) WHERE bin_deleted_at IS NULL;
-- Keyset-cursor pairs for query/ (mirrors idx_ir_created_id at line 4861).
CREATE INDEX IF NOT EXISTS idx_bin_created_id ON inventory_bin (bin_created_at, inventory_bin_id) WHERE bin_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bin_updated_id ON inventory_bin (bin_updated_at, inventory_bin_id) WHERE bin_deleted_at IS NULL;

-- One staging bin in MAIN so receiving has a default destination. The warehouse
-- id is resolved by subselect on warehouse_code, never a hardcoded integer.
--
-- WHERE NOT EXISTS rather than ON CONFLICT: the uniqueness above is a PARTIAL
-- index, which cannot be named as a conflict target, so a targeted ON CONFLICT
-- would error and an untargeted one would silently mask unrelated violations.
--
-- The guard is scoped to LIVE rows (bin_deleted_at IS NULL) so that it matches
-- uq_inventory_bin_code_active exactly. Scoping it to all rows instead would
-- mean that soft-deleting this system bin leaves the tenant permanently without
-- a staging destination: the guard would keep finding the dead row and skip the
-- insert on every subsequent boot. Matching the index's scope makes a deleted
-- system row reappear on the next boot, which is the same behaviour the seeded
-- chart of accounts already has (uq_coa_account_code_live, line 5041).
-- Phase 2's bin delete path must additionally refuse to soft-delete a
-- bin_is_system row, so this resurrection stays a backstop rather than the
-- normal path.
INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path, bin_depth, bin_is_system)
SELECT w.warehouse_id, 'STAGING', 'Receiving Staging', 'staging', 'STAGING', 0, TRUE
FROM lkp_warehouse w
WHERE w.warehouse_code = 'MAIN'
  AND NOT EXISTS (SELECT 1 FROM inventory_bin b
                  WHERE b.warehouse_id = w.warehouse_id
                    AND LOWER(b.bin_code) = 'staging'
                    AND b.bin_deleted_at IS NULL);

-- ---------------------------------------------------------------------
-- 3. inventory_bundle -- a shipping/handling group that moves as a set (AD-5).
--
-- A bundle has no area of its own and NEVER appears in inventory_slab_ledger;
-- only its member slabs do. That is exactly why it is not an inventory_slab row
-- with a 'bundle' unit_kind: chk_slab_dims and chk_slab_area (lines 4243-4244)
-- demand length/width/thickness > 0 and area > 0 on a thing with no dimensions,
-- and every stock/area/valuation query would have to remember
-- "AND slab_unit_kind <> 'bundle'". One forgotten predicate silently doubles
-- the on-hand area of the entire yard, and nothing would catch it.
--
-- Supersedes the free-text inventory_slab.slab_bundle_id (line 4216), which is
-- retained for historical rows and back-filled from bundle_code on write.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory_bundle (
    inventory_bundle_id      SERIAL       PRIMARY KEY,
    inventory_bundle_uuid    UUID         NOT NULL DEFAULT gen_random_uuid(),
    bundle_code              VARCHAR(50)  NOT NULL,
    bundle_vendor_id         INTEGER          NULL REFERENCES vendor(vendor_id),
    bundle_supplier_code     VARCHAR(80)  NOT NULL DEFAULT '',
    bundle_block_id          VARCHAR(50)  NOT NULL DEFAULT '',
    bundle_lot               VARCHAR(50)  NOT NULL DEFAULT '',
    inventory_item_id        INTEGER          NULL REFERENCES inventory_item(inventory_item_id),
    warehouse_id             INTEGER      NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    inventory_bin_id         INTEGER          NULL REFERENCES inventory_bin(inventory_bin_id),
    -- open   = members may be added/removed
    -- sealed = members move together; a single-member move is refused
    -- broken = deliberately split; members are independent again
    bundle_status            VARCHAR(12)  NOT NULL DEFAULT 'open',
    bundle_received_at       DATE             NULL,
    bundle_notes             TEXT         NOT NULL DEFAULT '',
    bundle_created_at        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    bundle_created_by        INTEGER          NULL REFERENCES employee(employee_id),
    bundle_updated_at        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    bundle_updated_by        INTEGER          NULL REFERENCES employee(employee_id),
    bundle_deleted_at        TIMESTAMP        NULL,
    bundle_deleted_by        INTEGER          NULL REFERENCES employee(employee_id),
    bundle_record_version    INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_bundle_uuid UNIQUE (inventory_bundle_uuid),
    CONSTRAINT chk_bundle_status   CHECK (bundle_status IN ('open','sealed','broken')),
    CONSTRAINT chk_bundle_supplier CHECK (bundle_supplier_code = '' OR bundle_vendor_id IS NOT NULL),
    CONSTRAINT chk_bundle_soft_delete CHECK (
        (bundle_deleted_at IS NULL AND bundle_deleted_by IS NULL) OR
        (bundle_deleted_at IS NOT NULL AND bundle_deleted_by IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_bundle_code_active
    ON inventory_bundle (LOWER(bundle_code)) WHERE bundle_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bundle_wh  ON inventory_bundle (warehouse_id, bundle_status) WHERE bundle_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bundle_bin ON inventory_bundle (inventory_bin_id)            WHERE bundle_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bundle_created_id ON inventory_bundle (bundle_created_at, inventory_bundle_id) WHERE bundle_deleted_at IS NULL;

-- ---------------------------------------------------------------------
-- 4. Stone attributes on the item catalogue (AD-3).
--
-- Typed columns + lkp_* rather than the existing custom_fields JSONB, because
-- these must be filterable, sortable, joinable and FK-validated -- none of which
-- JSONB gives: its values are untyped text so "thickness_mm < 25" cannot be a
-- numeric comparison, there is no FK so a typo creates a phantom colour, and the
-- item picker cannot JOIN lkp_color for a swatch.
--
-- Added by ALTER, NOT by editing CREATE TABLE inventory_item at line 2376.
-- ---------------------------------------------------------------------
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_tracking             VARCHAR(12)   NOT NULL DEFAULT 'quantity';
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_material_id          INTEGER           NULL REFERENCES lkp_material(material_id);
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_color_id             INTEGER           NULL REFERENCES lkp_color(color_id);
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_finish_id            INTEGER           NULL REFERENCES lkp_finish(finish_id);
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_thickness_mm         DECIMAL(10,2) NOT NULL DEFAULT 0;   -- 0 = not applicable
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_origin_country_id    INTEGER           NULL REFERENCES lkp_country(country_id);
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_barcode              VARCHAR(64)   NOT NULL DEFAULT '';
ALTER TABLE inventory_item ADD COLUMN IF NOT EXISTS
    inventory_item_default_warehouse_id INTEGER           NULL REFERENCES lkp_warehouse(warehouse_id);

-- inventory_item_tracking (AD-8) is the highest-value column here. Today nothing
-- on inventory_item says whether an item is slab-tracked or quantity-tracked,
-- yet inventory_slab_ledger (line 4261) and inventory_ledger (line 4821) BOTH
-- drive the same inventory_stock row -- so nothing stops an item receiving stock
-- through both paths and double-counting. Defaults to 'quantity', which is
-- correct for every existing row.

-- New CHECKs need the DO $$ guard: a bare ADD CONSTRAINT errors on the second
-- boot and breaks every tenant. Precedent: lines 4877-4903.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname='chk_inventory_item_tracking' AND conrelid='inventory_item'::regclass) THEN
        ALTER TABLE inventory_item ADD CONSTRAINT chk_inventory_item_tracking
            CHECK (inventory_item_tracking IN ('quantity','serialized'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname='chk_inventory_item_thickness' AND conrelid='inventory_item'::regclass) THEN
        ALTER TABLE inventory_item ADD CONSTRAINT chk_inventory_item_thickness
            CHECK (inventory_item_thickness_mm >= 0);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_inv_item_material ON inventory_item (inventory_item_material_id) WHERE inventory_item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_item_color    ON inventory_item (inventory_item_color_id)    WHERE inventory_item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_item_tracking ON inventory_item (inventory_item_tracking)    WHERE inventory_item_deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_inv_item_barcode_active
    ON inventory_item (LOWER(inventory_item_barcode))
    WHERE inventory_item_deleted_at IS NULL AND inventory_item_barcode <> '';
-- Keyset pairs the resolver already implies but the schema never got.
CREATE INDEX IF NOT EXISTS idx_inv_item_created_id ON inventory_item (inventory_item_created_at, inventory_item_id) WHERE inventory_item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_item_updated_id ON inventory_item (inventory_item_updated_at, inventory_item_id) WHERE inventory_item_deleted_at IS NULL;

-- ---------------------------------------------------------------------
-- 5. Generalise inventory_slab (line 4196) from "slab" to "serialized
-- inventory unit". Extending the existing table rather than creating a parallel
-- one keeps inventory_slab_ledger, fabrication_job_slab and every existing FK
-- valid, and avoids two competing sources of truth for the same physical piece.
-- ---------------------------------------------------------------------
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_unit_kind          VARCHAR(12)   NOT NULL DEFAULT 'slab';   -- slab | remnant
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    inventory_bin_id        INTEGER           NULL REFERENCES inventory_bin(inventory_bin_id);
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_barcode            VARCHAR(64)   NOT NULL DEFAULT '';
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_finish_id          INTEGER           NULL REFERENCES lkp_finish(finish_id);
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    inventory_bundle_id     INTEGER           NULL REFERENCES inventory_bundle(inventory_bundle_id);
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_sequence_in_bundle SMALLINT      NOT NULL DEFAULT 0;
-- Remnant tracking. A cut piece is only a *usable* remnant if it clears the
-- shop's minimum useful rectangle; below that it is scrap. The flag is set at
-- cut time by inventory/unit_cut.go and never derived on read, so a later change
-- to the threshold cannot silently reclassify last year's inventory.
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_is_usable_remnant  BOOLEAN       NOT NULL DEFAULT FALSE;
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_remnant_reason_id  INTEGER           NULL REFERENCES lkp_inventory_reason(inventory_reason_id);
-- Denormalised root ancestor: the original full slab a remnant descends from.
-- Recall ("every piece from vendor lot X") becomes one indexed equality rather
-- than a WITH RECURSIVE over slab_parent_slab_id.
ALTER TABLE inventory_slab ADD COLUMN IF NOT EXISTS
    slab_root_slab_id       INTEGER           NULL REFERENCES inventory_slab(inventory_slab_id);

-- slab_finish (VARCHAR, line 4222) cannot be dropped, so both it and
-- slab_finish_id exist forever. Store rule (AD-9): slab_finish_id is
-- authoritative on write and the store ALSO writes slab_finish = finish_name,
-- so fabrication/'s existing readers and historical rows keep working. Reads
-- prefer the id and fall back to the string when it is NULL. If any writer sets
-- only one, item search by finish silently misses rows.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname='chk_slab_unit_kind' AND conrelid='inventory_slab'::regclass) THEN
        ALTER TABLE inventory_slab ADD CONSTRAINT chk_slab_unit_kind
            CHECK (slab_unit_kind IN ('slab','remnant'));
    END IF;
    -- A remnant is by definition a cut piece, so it must agree with the existing
    -- chk_slab_form_parent (line 4247). Trivially true for all existing rows,
    -- which all default to unit_kind='slab'.
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname='chk_slab_remnant_is_cut' AND conrelid='inventory_slab'::regclass) THEN
        ALTER TABLE inventory_slab ADD CONSTRAINT chk_slab_remnant_is_cut
            CHECK (slab_unit_kind <> 'remnant' OR slab_form = 'cut');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname='chk_slab_root_not_self' AND conrelid='inventory_slab'::regclass) THEN
        ALTER TABLE inventory_slab ADD CONSTRAINT chk_slab_root_not_self
            CHECK (slab_root_slab_id IS DISTINCT FROM inventory_slab_id OR slab_form = 'full');
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_slab_bin       ON inventory_slab (inventory_bin_id)             WHERE slab_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_slab_kind_stat ON inventory_slab (slab_unit_kind, slab_status)  WHERE slab_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_slab_bundle_fk ON inventory_slab (inventory_bundle_id)          WHERE slab_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_slab_root      ON inventory_slab (slab_root_slab_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_barcode_active
    ON inventory_slab (LOWER(slab_barcode))
    WHERE slab_deleted_at IS NULL AND slab_barcode <> '';
-- Remnant picker: "usable offcuts of this item, biggest first".
CREATE INDEX IF NOT EXISTS idx_slab_remnant_pick
    ON inventory_slab (inventory_item_id, slab_area DESC)
    WHERE slab_deleted_at IS NULL AND slab_unit_kind='remnant'
      AND slab_is_usable_remnant = TRUE AND slab_status='available';

-- ---------------------------------------------------------------------
-- 6. History tables. Sibling parity: customer_history (1225),
-- coa_account_history (5053), item_receipt_history (4799).
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory_item_history (
    inventory_item_history_id SERIAL      PRIMARY KEY,
    inventory_item_id         INTEGER     NOT NULL REFERENCES inventory_item(inventory_item_id),
    history_action            VARCHAR(20) NOT NULL,
    history_field             VARCHAR(60) NOT NULL DEFAULT '',
    history_old_value         TEXT        NOT NULL DEFAULT '',
    history_new_value         TEXT        NOT NULL DEFAULT '',
    history_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_inv_item_history_action CHECK (history_action IN
        ('create','update','delete','activate','deactivate'))
);
CREATE INDEX IF NOT EXISTS idx_inv_item_history ON inventory_item_history (inventory_item_id, history_at DESC);

-- inventory_unit_history -- movement/status trail for a serialized unit.
--
-- Distinct from inventory_slab_ledger on purpose: the ledger is the FINANCIAL
-- record (signed quantity deltas that must sum to inventory_stock) and carries
-- partial unique indexes making each stock event once-only. This is the
-- OPERATIONAL record -- bin moves, re-grades, photo swaps, cut events -- none of
-- which change on-hand quantity and none of which may therefore touch the
-- ledger (AD-2). Writing a bin move to the ledger with delta 0 would collide
-- with the once-only indexes and pollute the audit trail with non-events.
CREATE TABLE IF NOT EXISTS inventory_unit_history (
    inventory_unit_history_id SERIAL      PRIMARY KEY,
    inventory_slab_id         INTEGER     NOT NULL REFERENCES inventory_slab(inventory_slab_id),
    history_action            VARCHAR(24) NOT NULL,
    history_field             VARCHAR(60) NOT NULL DEFAULT '',
    history_old_value         TEXT        NOT NULL DEFAULT '',
    history_new_value         TEXT        NOT NULL DEFAULT '',
    from_bin_id               INTEGER         NULL REFERENCES inventory_bin(inventory_bin_id),
    to_bin_id                 INTEGER         NULL REFERENCES inventory_bin(inventory_bin_id),
    from_warehouse_id         INTEGER         NULL REFERENCES lkp_warehouse(warehouse_id),
    to_warehouse_id           INTEGER         NULL REFERENCES lkp_warehouse(warehouse_id),
    inventory_reason_id       INTEGER         NULL REFERENCES lkp_inventory_reason(inventory_reason_id),
    history_note              TEXT        NOT NULL DEFAULT '',
    history_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_inv_unit_history_action CHECK (history_action IN
        ('create','update','bin_move','warehouse_move','status_change',
         'cut','remnant_created','scrap','photo','regrade','delete'))
);
CREATE INDEX IF NOT EXISTS idx_inv_unit_history     ON inventory_unit_history (inventory_slab_id, history_at DESC);
CREATE INDEX IF NOT EXISTS idx_inv_unit_history_bin ON inventory_unit_history (to_bin_id, history_at DESC);

-- ---------------------------------------------------------------------
-- 7. Repair uq_inventory_ledger_receipt_line / _return_line (line 4835).
--
-- Both indexes key on source_line_id ALONE, but inventory_ledger is explicitly
-- polymorphic over source documents (line 4816) with source_record_type as the
-- discriminator. Once a second document type writes 'received', its line id
-- collides with an unrelated item_receipt_line id -- an independent SERIAL, so
-- collision is near-certain rather than a corner case. The insert trips the
-- unique index, itemreceipt/inventory_post.go:44 maps that to
-- ErrMovementAlreadyApplied, and the user is told a document they never posted
-- was already applied -- while stock is silently never incremented.
--
-- The key becomes (source_record_type, source_line_id). COALESCE(...,0) rather
-- than a NOT NULL predicate, because NULLs are DISTINCT in a unique index, so a
-- NULL record type would silently drop the guarantee for exactly those rows.
--
-- This is a RELAXATION: every pair rejected by the new index was rejected by
-- the old one, so it cannot fail on existing data. Non-destructive -- indexes
-- only; no table, column or row is touched.
--
-- The REPLACEMENT indexes are NOT created here. They are created at their
-- original site (line 4838), whose definition has been corrected in place.
-- That placement is load-bearing and was found the hard way: creating the new
-- index here while leaving the old definition upstream means every boot
-- re-creates the OLD index from line 4838 (IF NOT EXISTS does not skip it,
-- because this section dropped it on the previous boot) and then drops it
-- again down here. That churn is harmless on an empty table and FATAL once a
-- tenant holds two legitimately-colliding rows: the upstream CREATE fails, the
-- single-transaction apply aborts, and the tenant can no longer boot at all.
-- A fresh-database apply cannot surface this -- only one with real data can.
--
-- So this stanza does exactly one thing: retire the legacy index names on
-- tenants provisioned before this change. DROP INDEX IF EXISTS is a no-op on
-- every subsequent boot.
-- DROP INDEX CONCURRENTLY is unavailable: the whole file is one transaction.
-- ---------------------------------------------------------------------
DROP INDEX IF EXISTS uq_inventory_ledger_receipt_line;
DROP INDEX IF EXISTS uq_inventory_ledger_return_line;

-- ---------------------------------------------------------------------
-- 8. Record types for the Phase 3 documents + their statuses.
--
-- The record_type_id is resolved by SUBSELECT on record_type_code, never a
-- hardcoded id (pattern copied from the FJOB block at line 4177), because
-- lkp_record_status keys statuses to types by SERIAL assignment order and a
-- literal id would be wrong on any tenant whose lookups were seeded out of
-- order -- silently mis-assigning every downstream status.
-- ---------------------------------------------------------------------
INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name,
    record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('IADJ','inventoryadjustment','Inventory Adjustment', TRUE,TRUE,1),
    ('ITRF','inventorytransfer',  'Inventory Transfer',   TRUE,TRUE,1),
    ('ICNT','inventorycyclecount','Inventory Cycle Count',TRUE,TRUE,1)
ON CONFLICT (record_type_code) DO NOTHING;

INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES ('DRFT','Draft'), ('PAPV','Pending Approval'), ('APPV','Approved'),
             ('POST','Posted'), ('CANC','Cancelled')) AS v(code, name)
CROSS JOIN lkp_record_type rt WHERE rt.record_type_code = 'IADJ'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- ITRF gets TRNS/RCVD rather than POST because a warehouse transfer is genuinely
-- two-legged: stock leaves the source before it arrives, and in-transit must be
-- representable.
INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES ('DRFT','Draft'), ('PAPV','Pending Approval'), ('APPV','Approved'),
             ('TRNS','In Transit'), ('RCVD','Received'), ('CANC','Cancelled')) AS v(code, name)
CROSS JOIN lkp_record_type rt WHERE rt.record_type_code = 'ITRF'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- RVW_ uses the trailing-underscore padding convention already in the file
-- (ACT_, INA_ at line 730).
INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES ('DRFT','Draft'), ('CNTG','Counting'), ('RVW_','In Review'),
             ('APPV','Approved'), ('POST','Posted'), ('CANC','Cancelled')) AS v(code, name)
CROSS JOIN lkp_record_type rt WHERE rt.record_type_code = 'ICNT'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- ---------------------------------------------------------------------
-- 9. RBAC backfill for the inventory_item -> inventory_unit split (AD-10).
--
-- Phase 2 moves the serialized-unit routes off inventory_item:* onto a new
-- inventory_unit:* resource. Without this backfill every CUSTOM tenant role
-- holding inventory_item:* would silently start returning 403 on those routes
-- the moment this deploys -- invisible until a user complains. super_admin is
-- unaffected: it holds a single wildcard ('*','*','all') that the enforcer
-- treats as match-all (line 47).
--
-- Idempotent via the role_permissions_unique constraint, so it converges on
-- the next boot of every tenant and is a no-op thereafter.
-- ---------------------------------------------------------------------
INSERT INTO role_permissions (role_id, resource, action, scope)
SELECT rp.role_id, 'inventory_unit', rp.action, rp.scope
FROM role_permissions rp
WHERE rp.resource = 'inventory_item'
ON CONFLICT (role_id, resource, action) DO NOTHING;

-- Anyone who could already read the item catalogue needs inventory_lookup:read
-- as well, or their item form loses its unit/warehouse/material dropdowns --
-- inventory_item_unit_id is NOT NULL (line 2382), so the form becomes
-- unsubmittable. Read-only grant; the write actions are deliberately NOT
-- backfilled and must be granted explicitly.
INSERT INTO role_permissions (role_id, resource, action, scope)
SELECT rp.role_id, 'inventory_lookup', 'read', rp.scope
FROM role_permissions rp
WHERE rp.resource = 'inventory_item' AND rp.action = 'read'
ON CONFLICT (role_id, resource, action) DO NOTHING;

-- =====================================================================
-- INVENTORY MANAGEMENT -- PHASE 3: TRANSFER, ADJUSTMENT, CYCLE COUNT
-- =====================================================================
--
-- Spec: docs/superpowers/specs/2026-07-26-inventory-module-design.md
--
-- The three documents AD-6 deferred. Their record types (IADJ/ITRF/ICNT),
-- statuses and lkp_inventory_reason were seeded in the Phase 1 section, so this
-- section adds only tables, three CHECK widenings and the once-only indexes.
--
-- All three handle BOTH stock models. A line carries an optional
-- inventory_slab_id: set means "this specific slab", unset means "this many of
-- a quantity-tracked item". That keeps one document per business event instead
-- of a serialized document and a bulk document that would inevitably drift.
--
-- Bin transfer is NOT here. Bins locate serialized units and inventory_stock is
-- keyed (item, warehouse), so moving a unit between bins is stock-neutral by
-- construction and shipped in Phase 2 as PATCH /units/{uuid}/bin (AD-2).
--
-- FK order below is load-bearing:
--   CHECK widenings -> slab ledger source columns -> adjustment -> transfer ->
--   count -> once-only ledger indexes
--

-- ---------------------------------------------------------------------
-- 1. Widen three CHECK constraints.
--
-- Each is a pure RELAXATION: every value accepted by the old constraint is
-- accepted by the new one, so no existing row can fail revalidation and the
-- rewrite cannot break a tenant holding real data. That is what makes
-- DROP + ADD acceptable here where it would not be for a narrowing change.
--
-- DROP CONSTRAINT IF EXISTS followed by an unconditional ADD is idempotent on
-- its own -- the drop makes the add safe on every boot -- so these need no
-- pg_constraint existence guard, unlike a bare ADD CONSTRAINT.
-- ---------------------------------------------------------------------

-- 'transferred' distinguishes the two legs of a warehouse transfer from an
-- 'adjusted' write-off. Recording a transfer as an adjustment would make every
-- shrinkage report count routine yard-to-yard movement as loss.
DO $$
BEGIN
    ALTER TABLE inventory_ledger DROP CONSTRAINT IF EXISTS chk_inventory_ledger_event;
    ALTER TABLE inventory_ledger ADD CONSTRAINT chk_inventory_ledger_event
        CHECK (event IN ('received','returned','adjusted','consumed','transferred'));
END $$;

DO $$
BEGIN
    ALTER TABLE inventory_slab_ledger DROP CONSTRAINT IF EXISTS chk_slab_ledger_event;
    ALTER TABLE inventory_slab_ledger ADD CONSTRAINT chk_slab_ledger_event
        CHECK (event IN ('received','consumed','recovered','scrapped','adjusted','transferred'));
END $$;

-- 'in_transit' is what makes a two-legged transfer honest. Stock leaves the
-- source before it reaches the destination, and without this state a slab in a
-- truck would have to be recorded as still standing in the yard it left --
-- so a cycle count of that yard would report it missing and write it off.
DO $$
BEGIN
    ALTER TABLE inventory_slab DROP CONSTRAINT IF EXISTS chk_slab_status;
    ALTER TABLE inventory_slab ADD CONSTRAINT chk_slab_status
        CHECK (slab_status IN ('available','reserved','consumed','scrapped','in_transit'));
END $$;

-- ---------------------------------------------------------------------
-- 2. Source-document columns on inventory_slab_ledger.
--
-- The bulk ledger has carried these since it was created; the slab ledger never
-- did, so "which document moved this slab?" was unanswerable. They also give
-- the transfer legs a once-only key, which the existing per-(slab,event) unique
-- indexes cannot: a slab may legitimately be transferred many times over its
-- life, so uniqueness has to be per source LINE, not per slab.
-- ---------------------------------------------------------------------
ALTER TABLE inventory_slab_ledger ADD COLUMN IF NOT EXISTS
    source_record_type  INTEGER NULL REFERENCES lkp_record_type(record_type_id);
ALTER TABLE inventory_slab_ledger ADD COLUMN IF NOT EXISTS
    source_record_id    INTEGER NULL;
ALTER TABLE inventory_slab_ledger ADD COLUMN IF NOT EXISTS
    source_line_id      INTEGER NULL;
CREATE INDEX IF NOT EXISTS idx_slab_ledger_source
    ON inventory_slab_ledger (source_record_type, source_record_id);

-- ---------------------------------------------------------------------
-- 3. inventory_adjustment -- manual reconciliation, damage, shrinkage.
--
-- Statuses (seeded above): DRFT -> PAPV -> APPV -> POST, or CANC.
-- Approval is enforced by RBAC (inventory_adjustment:approve gates PAPV->APPV)
-- and trailed in inventory_adjustment_history, rather than by the named-approver
-- pair fabrication_job_approver/_approval. Those two tables exist to ROUTE an
-- approval to specific people; nothing here asks for routing, and adding them
-- unused would be exactly the speculative drift AD-6 warns about.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory_adjustment (
    inventory_adjustment_id       SERIAL        PRIMARY KEY,
    inventory_adjustment_uuid     UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                INTEGER           NULL,  -- platform owner stamp, no cross-DB FK
    adjustment_number             VARCHAR(20)       NULL,  -- 'IADJ-000001', generated post-insert in Go

    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = IADJ
    adjustment_status             INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    warehouse_id                  INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    adjustment_date               DATE          NOT NULL DEFAULT CURRENT_DATE,
    -- Header reason is the default a line inherits when it names none. A line
    -- reason is still required at post time -- see chk_iadjl_reason.
    inventory_reason_id           INTEGER           NULL REFERENCES lkp_inventory_reason(inventory_reason_id),
    adjustment_notes              TEXT          NOT NULL DEFAULT '',
    adjustment_internal_notes     TEXT          NOT NULL DEFAULT '',

    adjustment_owner_id           INTEGER           NULL REFERENCES employee(employee_id),

    adjustment_posted_at          TIMESTAMP         NULL,
    adjustment_posted_by          INTEGER           NULL REFERENCES employee(employee_id),
    adjustment_cancelled_at       TIMESTAMP         NULL,
    adjustment_cancelled_by       INTEGER           NULL REFERENCES employee(employee_id),
    adjustment_cancel_reason      TEXT          NOT NULL DEFAULT '',

    adjustment_custom_fields      JSONB         NOT NULL DEFAULT '{}',
    adjustment_created_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    adjustment_created_by         INTEGER           NULL REFERENCES employee(employee_id),
    adjustment_updated_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    adjustment_updated_by         INTEGER           NULL REFERENCES employee(employee_id),
    adjustment_deleted_at         TIMESTAMP         NULL,
    adjustment_deleted_by         INTEGER           NULL REFERENCES employee(employee_id),
    adjustment_record_version     INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_inventory_adjustment_uuid   UNIQUE (inventory_adjustment_uuid),
    CONSTRAINT uq_inventory_adjustment_number UNIQUE (adjustment_number),
    CONSTRAINT chk_iadj_soft_delete CHECK (
        (adjustment_deleted_at IS NULL AND adjustment_deleted_by IS NULL) OR
        (adjustment_deleted_at IS NOT NULL AND adjustment_deleted_by IS NOT NULL)),
    CONSTRAINT chk_iadj_posted_pair CHECK (
        (adjustment_posted_at IS NULL AND adjustment_posted_by IS NULL) OR
        (adjustment_posted_at IS NOT NULL AND adjustment_posted_by IS NOT NULL)),
    CONSTRAINT chk_iadj_cancel_pair CHECK (
        (adjustment_cancelled_at IS NULL AND adjustment_cancelled_by IS NULL) OR
        (adjustment_cancelled_at IS NOT NULL AND adjustment_cancelled_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_iadj_status     ON inventory_adjustment (adjustment_status)  WHERE adjustment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_iadj_warehouse  ON inventory_adjustment (warehouse_id)       WHERE adjustment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_iadj_owner      ON inventory_adjustment (adjustment_owner_id) WHERE adjustment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_iadj_created_id ON inventory_adjustment (adjustment_created_at, inventory_adjustment_id) WHERE adjustment_deleted_at IS NULL;

-- inventory_adjustment_line -- one item's correction.
--
-- qty_delta is SIGNED and in the item's own unit: negative writes stock off,
-- positive puts it back. For a serialized line it is derived from the slab's
-- area at post time and never taken from the caller, the same rule that governs
-- receipt and cutting -- nothing forces slab_area_unit_id to equal the item's
-- unit, so a trusted client value is how a SQM measurement lands against a
-- SQFT item, wrong by 10.76x with no constraint to catch it.
CREATE TABLE IF NOT EXISTS inventory_adjustment_line (
    inventory_adjustment_line_id   SERIAL        PRIMARY KEY,
    inventory_adjustment_line_uuid UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_adjustment_id        INTEGER       NOT NULL REFERENCES inventory_adjustment(inventory_adjustment_id) ON DELETE CASCADE,
    line_number                    INTEGER       NOT NULL,

    inventory_item_id              INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    -- NULL = quantity-tracked line; set = this one physical slab.
    inventory_slab_id              INTEGER           NULL REFERENCES inventory_slab(inventory_slab_id),
    inventory_reason_id            INTEGER       NOT NULL REFERENCES lkp_inventory_reason(inventory_reason_id),

    item_name                      VARCHAR(150)  NOT NULL DEFAULT '',
    sku                            VARCHAR(50)   NOT NULL DEFAULT '',
    unit_id                        INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                      VARCHAR(10)   NOT NULL DEFAULT '',
    slab_serial                    VARCHAR(80)   NOT NULL DEFAULT '',

    qty_delta                      DECIMAL(14,3) NOT NULL,
    line_notes                     TEXT          NOT NULL DEFAULT '',

    line_created_at                TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    line_created_by                INTEGER           NULL REFERENCES employee(employee_id),
    line_updated_at                TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    line_deleted_at                TIMESTAMP         NULL,
    line_record_version            INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_iadjl_uuid UNIQUE (inventory_adjustment_line_uuid),
    -- A zero adjustment is a no-op that would still consume a reason code and a
    -- ledger row, so it is refused rather than silently ignored.
    CONSTRAINT chk_iadjl_delta  CHECK (qty_delta <> 0),
    CONSTRAINT chk_iadjl_reason CHECK (inventory_reason_id IS NOT NULL),
    -- A serialized line carries the serial it froze, so the document still reads
    -- correctly after the slab is consumed and its row moves on.
    CONSTRAINT chk_iadjl_serial CHECK (inventory_slab_id IS NULL OR slab_serial <> '')
);
CREATE INDEX IF NOT EXISTS idx_iadjl_parent ON inventory_adjustment_line (inventory_adjustment_id) WHERE line_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_iadjl_item   ON inventory_adjustment_line (inventory_item_id)       WHERE line_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_iadjl_slab   ON inventory_adjustment_line (inventory_slab_id)       WHERE line_deleted_at IS NULL;
-- One live line per slab per document: adjusting the same slab twice on one
-- document would post two write-offs for one physical event.
CREATE UNIQUE INDEX IF NOT EXISTS uq_iadjl_slab_once
    ON inventory_adjustment_line (inventory_adjustment_id, inventory_slab_id)
    WHERE inventory_slab_id IS NOT NULL AND line_deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS inventory_adjustment_history (
    inventory_adjustment_history_id SERIAL      PRIMARY KEY,
    inventory_adjustment_id         INTEGER     NOT NULL REFERENCES inventory_adjustment(inventory_adjustment_id) ON DELETE CASCADE,
    from_status_id                  INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                    INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32) NOT NULL DEFAULT 'transition',
    actor_employee_id               INTEGER         NULL REFERENCES employee(employee_id),
    snapshot                        JSONB       NOT NULL DEFAULT '{}',
    at                              TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iadj_history ON inventory_adjustment_history (inventory_adjustment_id, at DESC);

-- ---------------------------------------------------------------------
-- 4. inventory_transfer -- stock moving between warehouses.
--
-- Statuses (seeded above): DRFT -> PAPV -> APPV -> TRNS -> RCVD, or CANC.
--
-- Genuinely two-legged, which is why it has TRNS/RCVD where the adjustment has
-- a single POST: stock leaves the source at ship and arrives at the destination
-- at receive, and between those two moments it is in neither warehouse.
-- inventory_stock therefore UNDERSTATES total on-hand while a transfer is in
-- transit, by design -- the in-transit quantity is the document, and pretending
-- otherwise would need a phantom warehouse row that every stock query would
-- have to learn to exclude.
--
-- Receive is all-or-nothing. The seeded ITRF status set has no partial state,
-- and a qty_received column that only ever equals qty would be dead weight
-- inviting a half-built partial-receipt path later.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory_transfer (
    inventory_transfer_id      SERIAL        PRIMARY KEY,
    inventory_transfer_uuid    UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id             INTEGER           NULL,
    transfer_number            VARCHAR(20)       NULL,  -- 'ITRF-000001'

    record_type                INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = ITRF
    transfer_status            INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    from_warehouse_id          INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    to_warehouse_id            INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    -- Destination bin is optional and applies to serialized lines only. Bins
    -- belong to a warehouse, so the SOURCE bin is never carried: it is
    -- meaningless at the destination and is cleared on arrival.
    to_bin_id                  INTEGER           NULL REFERENCES inventory_bin(inventory_bin_id),

    transfer_date              DATE          NOT NULL DEFAULT CURRENT_DATE,
    transfer_expected_date     DATE              NULL,
    transfer_carrier           VARCHAR(80)   NOT NULL DEFAULT '',
    transfer_tracking_number   VARCHAR(80)   NOT NULL DEFAULT '',
    transfer_notes             TEXT          NOT NULL DEFAULT '',
    transfer_internal_notes    TEXT          NOT NULL DEFAULT '',

    transfer_owner_id          INTEGER           NULL REFERENCES employee(employee_id),

    transfer_shipped_at        TIMESTAMP         NULL,
    transfer_shipped_by        INTEGER           NULL REFERENCES employee(employee_id),
    transfer_received_at       TIMESTAMP         NULL,
    transfer_received_by       INTEGER           NULL REFERENCES employee(employee_id),
    transfer_cancelled_at      TIMESTAMP         NULL,
    transfer_cancelled_by      INTEGER           NULL REFERENCES employee(employee_id),
    transfer_cancel_reason     TEXT          NOT NULL DEFAULT '',

    transfer_custom_fields     JSONB         NOT NULL DEFAULT '{}',
    transfer_created_at        TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    transfer_created_by        INTEGER           NULL REFERENCES employee(employee_id),
    transfer_updated_at        TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    transfer_updated_by        INTEGER           NULL REFERENCES employee(employee_id),
    transfer_deleted_at        TIMESTAMP         NULL,
    transfer_deleted_by        INTEGER           NULL REFERENCES employee(employee_id),
    transfer_record_version    INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_inventory_transfer_uuid   UNIQUE (inventory_transfer_uuid),
    CONSTRAINT uq_inventory_transfer_number UNIQUE (transfer_number),
    -- A transfer to the warehouse it left is not a transfer. Row-local, so a
    -- CHECK can express it and no code path has to remember.
    CONSTRAINT chk_itrf_distinct_wh CHECK (from_warehouse_id <> to_warehouse_id),
    CONSTRAINT chk_itrf_soft_delete CHECK (
        (transfer_deleted_at IS NULL AND transfer_deleted_by IS NULL) OR
        (transfer_deleted_at IS NOT NULL AND transfer_deleted_by IS NOT NULL)),
    CONSTRAINT chk_itrf_shipped_pair CHECK (
        (transfer_shipped_at IS NULL AND transfer_shipped_by IS NULL) OR
        (transfer_shipped_at IS NOT NULL AND transfer_shipped_by IS NOT NULL)),
    CONSTRAINT chk_itrf_received_pair CHECK (
        (transfer_received_at IS NULL AND transfer_received_by IS NULL) OR
        (transfer_received_at IS NOT NULL AND transfer_received_by IS NOT NULL)),
    -- Arrival cannot precede departure, and cannot happen without one.
    CONSTRAINT chk_itrf_receive_after_ship CHECK (
        transfer_received_at IS NULL OR
        (transfer_shipped_at IS NOT NULL AND transfer_received_at >= transfer_shipped_at)),
    CONSTRAINT chk_itrf_cancel_pair CHECK (
        (transfer_cancelled_at IS NULL AND transfer_cancelled_by IS NULL) OR
        (transfer_cancelled_at IS NOT NULL AND transfer_cancelled_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_itrf_status   ON inventory_transfer (transfer_status)   WHERE transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_itrf_from_wh  ON inventory_transfer (from_warehouse_id) WHERE transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_itrf_to_wh    ON inventory_transfer (to_warehouse_id)   WHERE transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_itrf_owner    ON inventory_transfer (transfer_owner_id) WHERE transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_itrf_created_id ON inventory_transfer (transfer_created_at, inventory_transfer_id) WHERE transfer_deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS inventory_transfer_line (
    inventory_transfer_line_id   SERIAL        PRIMARY KEY,
    inventory_transfer_line_uuid UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_transfer_id        INTEGER       NOT NULL REFERENCES inventory_transfer(inventory_transfer_id) ON DELETE CASCADE,
    line_number                  INTEGER       NOT NULL,

    inventory_item_id            INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    inventory_slab_id            INTEGER           NULL REFERENCES inventory_slab(inventory_slab_id),

    item_name                    VARCHAR(150)  NOT NULL DEFAULT '',
    sku                          VARCHAR(50)   NOT NULL DEFAULT '',
    unit_id                      INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                    VARCHAR(10)   NOT NULL DEFAULT '',
    slab_serial                  VARCHAR(80)   NOT NULL DEFAULT '',

    -- Always POSITIVE: direction is the leg, not the sign. Ship writes -qty at
    -- the source and receive writes +qty at the destination, so a signed
    -- quantity here would let one document both add and remove at each end.
    qty                          DECIMAL(14,3) NOT NULL,
    line_notes                   TEXT          NOT NULL DEFAULT '',

    line_created_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    line_created_by              INTEGER           NULL REFERENCES employee(employee_id),
    line_updated_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    line_deleted_at              TIMESTAMP         NULL,
    line_record_version          INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_itrfl_uuid   UNIQUE (inventory_transfer_line_uuid),
    CONSTRAINT chk_itrfl_qty    CHECK (qty > 0),
    CONSTRAINT chk_itrfl_serial CHECK (inventory_slab_id IS NULL OR slab_serial <> '')
);
CREATE INDEX IF NOT EXISTS idx_itrfl_parent ON inventory_transfer_line (inventory_transfer_id) WHERE line_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_itrfl_item   ON inventory_transfer_line (inventory_item_id)     WHERE line_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_itrfl_slab   ON inventory_transfer_line (inventory_slab_id)     WHERE line_deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_itrfl_slab_once
    ON inventory_transfer_line (inventory_transfer_id, inventory_slab_id)
    WHERE inventory_slab_id IS NOT NULL AND line_deleted_at IS NULL;
-- NOTE: "a slab may be on only one IN-FLIGHT transfer" is deliberately NOT an
-- index. The predicate would have to read inventory_transfer.transfer_status,
-- and a partial index may only reference columns of its own table (no joins,
-- no subqueries) -- so it is unrepresentable here however it is written.
--
-- The guard is the slab's own status instead, which is stronger than an index
-- would have been: ship moves the slab to 'in_transit' under FOR UPDATE, and
-- every ship path refuses a slab that is not 'available'. A second crew
-- shipping the same slab blocks on the lock and is then refused, so the slab
-- cannot depart twice or arrive at two warehouses.

CREATE TABLE IF NOT EXISTS inventory_transfer_history (
    inventory_transfer_history_id SERIAL      PRIMARY KEY,
    inventory_transfer_id         INTEGER     NOT NULL REFERENCES inventory_transfer(inventory_transfer_id) ON DELETE CASCADE,
    from_status_id                INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    action                        VARCHAR(32) NOT NULL DEFAULT 'transition',
    actor_employee_id             INTEGER         NULL REFERENCES employee(employee_id),
    snapshot                      JSONB       NOT NULL DEFAULT '{}',
    at                            TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_itrf_history ON inventory_transfer_history (inventory_transfer_id, at DESC);

-- ---------------------------------------------------------------------
-- 5. inventory_count -- cycle counting and physical stock takes.
--
-- Statuses (seeded above): DRFT -> CNTG -> RVW_ -> APPV -> POST, or CANC.
--
-- Freezing (DRFT -> CNTG) snapshots the system quantity onto every line and
-- records count_frozen_at. The snapshot is the whole point: a variance is only
-- meaningful against the number the system believed AT THE MOMENT COUNTING
-- STARTED. Recomputing it at post time would silently absorb every movement
-- that happened while the crew walked the yard, so a genuine shortage would
-- reconcile itself to zero and the write-off would never be raised.
--
-- Counting is scoped to a warehouse and optionally one bin subtree. While a
-- count is CNTG, inventory/count_freeze.go refuses unit moves inside that
-- scope -- stock cannot move under the counters' feet.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory_count (
    inventory_count_id       SERIAL        PRIMARY KEY,
    inventory_count_uuid     UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id           INTEGER           NULL,
    count_number             VARCHAR(20)       NULL,  -- 'ICNT-000001'

    record_type              INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = ICNT
    count_status             INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    warehouse_id             INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    -- NULL = the whole warehouse. Set = this bin and everything under it,
    -- matched on inventory_bin.bin_path so a subtree is one prefix scan.
    inventory_bin_id         INTEGER           NULL REFERENCES inventory_bin(inventory_bin_id),

    count_date               DATE          NOT NULL DEFAULT CURRENT_DATE,
    count_frozen_at          TIMESTAMP         NULL,
    count_frozen_by          INTEGER           NULL REFERENCES employee(employee_id),
    count_notes              TEXT          NOT NULL DEFAULT '',
    count_internal_notes     TEXT          NOT NULL DEFAULT '',

    count_owner_id           INTEGER           NULL REFERENCES employee(employee_id),

    count_posted_at          TIMESTAMP         NULL,
    count_posted_by          INTEGER           NULL REFERENCES employee(employee_id),
    count_cancelled_at       TIMESTAMP         NULL,
    count_cancelled_by       INTEGER           NULL REFERENCES employee(employee_id),
    count_cancel_reason      TEXT          NOT NULL DEFAULT '',

    count_custom_fields      JSONB         NOT NULL DEFAULT '{}',
    count_created_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    count_created_by         INTEGER           NULL REFERENCES employee(employee_id),
    count_updated_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    count_updated_by         INTEGER           NULL REFERENCES employee(employee_id),
    count_deleted_at         TIMESTAMP         NULL,
    count_deleted_by         INTEGER           NULL REFERENCES employee(employee_id),
    count_record_version     INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_inventory_count_uuid   UNIQUE (inventory_count_uuid),
    CONSTRAINT uq_inventory_count_number UNIQUE (count_number),
    CONSTRAINT chk_icnt_soft_delete CHECK (
        (count_deleted_at IS NULL AND count_deleted_by IS NULL) OR
        (count_deleted_at IS NOT NULL AND count_deleted_by IS NOT NULL)),
    CONSTRAINT chk_icnt_frozen_pair CHECK (
        (count_frozen_at IS NULL AND count_frozen_by IS NULL) OR
        (count_frozen_at IS NOT NULL AND count_frozen_by IS NOT NULL)),
    CONSTRAINT chk_icnt_posted_pair CHECK (
        (count_posted_at IS NULL AND count_posted_by IS NULL) OR
        (count_posted_at IS NOT NULL AND count_posted_by IS NOT NULL)),
    -- Posting without a freeze would mean posting variances against a snapshot
    -- that was never taken.
    CONSTRAINT chk_icnt_post_needs_freeze CHECK (
        count_posted_at IS NULL OR count_frozen_at IS NOT NULL),
    CONSTRAINT chk_icnt_cancel_pair CHECK (
        (count_cancelled_at IS NULL AND count_cancelled_by IS NULL) OR
        (count_cancelled_at IS NOT NULL AND count_cancelled_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_icnt_status    ON inventory_count (count_status)   WHERE count_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_icnt_warehouse ON inventory_count (warehouse_id)   WHERE count_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_icnt_owner     ON inventory_count (count_owner_id) WHERE count_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_icnt_created_id ON inventory_count (count_created_at, inventory_count_id) WHERE count_deleted_at IS NULL;
-- The freeze guard's hot path: "is anything counting this warehouse right now?"
CREATE INDEX IF NOT EXISTS idx_icnt_active_scope
    ON inventory_count (warehouse_id, count_status) WHERE count_deleted_at IS NULL AND count_frozen_at IS NOT NULL;

-- inventory_count_line -- one countable thing and what the crew found.
--
-- count_variance is a GENERATED column, not a value any writer supplies. A
-- variance that can disagree with the two numbers it is derived from is worse
-- than no variance at all, because it is the number the write-off posts from.
-- NULL counted_qty (not yet counted) yields NULL variance, which is correct and
-- is what separates "counted zero" from "not counted" -- collapsing those two
-- would write off every shelf the crew simply had not reached yet.
CREATE TABLE IF NOT EXISTS inventory_count_line (
    inventory_count_line_id   SERIAL        PRIMARY KEY,
    inventory_count_line_uuid UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_count_id        INTEGER       NOT NULL REFERENCES inventory_count(inventory_count_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,

    inventory_item_id         INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    inventory_slab_id         INTEGER           NULL REFERENCES inventory_slab(inventory_slab_id),
    inventory_bin_id          INTEGER           NULL REFERENCES inventory_bin(inventory_bin_id),
    -- Required only once a variance exists; enforced at post time, not here,
    -- because a line is created at freeze with no variance and no reason yet.
    inventory_reason_id       INTEGER           NULL REFERENCES lkp_inventory_reason(inventory_reason_id),

    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    unit_id                   INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)   NOT NULL DEFAULT '',
    slab_serial               VARCHAR(80)   NOT NULL DEFAULT '',

    system_qty                DECIMAL(14,3) NOT NULL DEFAULT 0,
    counted_qty               DECIMAL(14,3)     NULL,
    count_variance            DECIMAL(14,3) GENERATED ALWAYS AS (counted_qty - system_qty) STORED,
    -- TRUE when the crew found something the frozen snapshot did not contain.
    -- It still counts as a variance, but it is worth surfacing separately: an
    -- unexpected slab is usually a misfiled location, not found stone.
    is_unexpected             BOOLEAN       NOT NULL DEFAULT FALSE,
    counted_at                TIMESTAMP         NULL,
    counted_by                INTEGER           NULL REFERENCES employee(employee_id),
    line_notes                TEXT          NOT NULL DEFAULT '',

    line_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    line_updated_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    line_deleted_at           TIMESTAMP         NULL,
    line_record_version       INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_icntl_uuid    UNIQUE (inventory_count_line_uuid),
    CONSTRAINT chk_icntl_counted CHECK (counted_qty IS NULL OR counted_qty >= 0),
    CONSTRAINT chk_icntl_serial  CHECK (inventory_slab_id IS NULL OR slab_serial <> ''),
    CONSTRAINT chk_icntl_counted_pair CHECK (
        (counted_qty IS NULL AND counted_at IS NULL) OR counted_qty IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_icntl_parent ON inventory_count_line (inventory_count_id) WHERE line_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_icntl_item   ON inventory_count_line (inventory_item_id)  WHERE line_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_icntl_slab   ON inventory_count_line (inventory_slab_id)  WHERE line_deleted_at IS NULL;
-- The review screen's query: every line whose count disagrees with the system.
CREATE INDEX IF NOT EXISTS idx_icntl_variance
    ON inventory_count_line (inventory_count_id) WHERE line_deleted_at IS NULL AND count_variance <> 0;
CREATE UNIQUE INDEX IF NOT EXISTS uq_icntl_slab_once
    ON inventory_count_line (inventory_count_id, inventory_slab_id)
    WHERE inventory_slab_id IS NOT NULL AND line_deleted_at IS NULL;
-- A bulk item appears at most once per count: two lines for one (item,
-- warehouse) would post two variances against a single system quantity.
CREATE UNIQUE INDEX IF NOT EXISTS uq_icntl_bulk_once
    ON inventory_count_line (inventory_count_id, inventory_item_id)
    WHERE inventory_slab_id IS NULL AND line_deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS inventory_count_history (
    inventory_count_history_id SERIAL      PRIMARY KEY,
    inventory_count_id         INTEGER     NOT NULL REFERENCES inventory_count(inventory_count_id) ON DELETE CASCADE,
    from_status_id             INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id               INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    action                     VARCHAR(32) NOT NULL DEFAULT 'transition',
    actor_employee_id          INTEGER         NULL REFERENCES employee(employee_id),
    snapshot                   JSONB       NOT NULL DEFAULT '{}',
    at                         TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_icnt_history ON inventory_count_history (inventory_count_id, at DESC);

-- ---------------------------------------------------------------------
-- 6. Once-only indexes for the new ledger events.
--
-- Same technique and the same reasoning as uq_inventory_ledger_src_line_received
-- (line 4851), including AD-11's correction: the key MUST carry
-- source_record_type, because IADJ and ICNT both write 'adjusted' rows and
-- their line ids come from independent SERIALs. Keyed on source_line_id alone,
-- adjustment line 512 and count line 512 would collide -- and the second
-- document's post would be reported as "already applied" while its stock never
-- moved.
--
-- COALESCE(source_record_type, 0) keeps NULL from making every row distinct,
-- which would defeat the index entirely.
-- ---------------------------------------------------------------------
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_ledger_src_line_adjusted
    ON inventory_ledger (COALESCE(source_record_type, 0), source_line_id)
    WHERE event = 'adjusted' AND source_line_id IS NOT NULL;

-- A transfer line writes TWO 'transferred' rows -- one out of the source, one
-- into the destination -- so warehouse_id is part of the key. Without it the
-- arrival leg would collide with the departure leg and be rejected as a
-- duplicate, leaving stock permanently deducted from the source and never
-- added at the destination.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_ledger_src_line_transferred
    ON inventory_ledger (COALESCE(source_record_type, 0), source_line_id, warehouse_id)
    WHERE event = 'transferred' AND source_line_id IS NOT NULL;

-- The slab ledger's existing once-only indexes key on (slab, event), which
-- cannot express a transfer: a slab may legitimately be transferred many times
-- over its life. These key on the source LINE instead, and carry warehouse_id
-- for the same two-legged reason as above.
CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_ledger_src_line_transferred
    ON inventory_slab_ledger (COALESCE(source_record_type, 0), source_line_id, warehouse_id)
    WHERE event = 'transferred' AND source_line_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_slab_ledger_src_line_adjusted
    ON inventory_slab_ledger (COALESCE(source_record_type, 0), source_line_id)
    WHERE event = 'adjusted' AND source_line_id IS NOT NULL;

-- ═══════════════════════════════════════════════════════════════════════
-- Accounting Period Management (accountingperiod/)
--
-- Replaces accounting_settings.books_closed_through as the *authoritative*
-- period concept, without retiring it: the column stays, and is recomputed
-- from the contiguous closed prefix on every close/reopen so anything still
-- reading it -- including journal's own fallback path for tenants who have
-- not configured a calendar -- keeps getting a correct answer.
--
-- Spec: docs/superpowers/specs/2026-07-31-accounting-period-management-design.md
-- ═══════════════════════════════════════════════════════════════════════

-- 1. Fiscal calendar configuration on the existing accounting_settings
--    singleton. Appended as ALTER ... ADD COLUMN IF NOT EXISTS, never by
--    editing the CREATE TABLE body above -- that body is a no-op on every
--    existing tenant, so an inline edit would reach fresh databases only.
ALTER TABLE accounting_settings
    ADD COLUMN IF NOT EXISTS fiscal_year_start_month SMALLINT NULL;
ALTER TABLE accounting_settings
    ADD COLUMN IF NOT EXISTS base_period_start DATE NULL;
ALTER TABLE accounting_settings
    ADD COLUMN IF NOT EXISTS accounting_calendar_configured_at TIMESTAMP NULL;

-- ADD CONSTRAINT is not idempotent -- a bare one errors on the second boot
-- and breaks every tenant -- so each new CHECK is guarded on pg_constraint.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_acct_settings_fy_start_month') THEN
        ALTER TABLE accounting_settings
            ADD CONSTRAINT chk_acct_settings_fy_start_month
            CHECK (fiscal_year_start_month IS NULL
                   OR (fiscal_year_start_month >= 1 AND fiscal_year_start_month <= 12));
    END IF;
END $$;

-- 2. fiscal_year -- 12 accounting_period rows hang off each one.
--
-- fiscal_year_status is DERIVED, never set directly by a caller: the store
-- recomputes it in the same transaction as every period status change
-- ('closed' when all 12 periods are closed, 'open' otherwise). That removes
-- the drift case where a year claims closed while a period under it is open.
CREATE TABLE IF NOT EXISTS fiscal_year (
    fiscal_year_id          SERIAL       PRIMARY KEY,
    fiscal_year_uuid        UUID         NOT NULL DEFAULT gen_random_uuid(),
    fiscal_year_name        VARCHAR(20)  NOT NULL,
    fiscal_year_start       DATE         NOT NULL,
    fiscal_year_end         DATE         NOT NULL,
    fiscal_year_status      VARCHAR(10)  NOT NULL DEFAULT 'open',
    fiscal_year_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fiscal_year_created_by  INTEGER          NULL REFERENCES employee(employee_id),
    fiscal_year_updated_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fiscal_year_updated_by  INTEGER          NULL REFERENCES employee(employee_id),

    CONSTRAINT uq_fiscal_year_uuid  UNIQUE (fiscal_year_uuid),
    CONSTRAINT uq_fiscal_year_name  UNIQUE (fiscal_year_name),
    CONSTRAINT uq_fiscal_year_start UNIQUE (fiscal_year_start),
    CONSTRAINT chk_fy_range  CHECK (fiscal_year_end > fiscal_year_start),
    CONSTRAINT chk_fy_status CHECK (fiscal_year_status IN ('open','closed'))
);

-- 3. accounting_period -- always a calendar month; 12 per fiscal year.
--
-- is_base_period marks the go-live boundary. Periods ending before the base
-- period start are created 'closed' and can never be reopened: they stand for
-- books closed in whatever system the tenant used before StoneSuite.
CREATE TABLE IF NOT EXISTS accounting_period (
    accounting_period_id         SERIAL       PRIMARY KEY,
    accounting_period_uuid       UUID         NOT NULL DEFAULT gen_random_uuid(),
    fiscal_year_id               INTEGER      NOT NULL REFERENCES fiscal_year(fiscal_year_id),
    accounting_period_name       VARCHAR(30)  NOT NULL,
    period_number                SMALLINT     NOT NULL,
    period_start                 DATE         NOT NULL,
    period_end                   DATE         NOT NULL,
    accounting_period_status     VARCHAR(10)  NOT NULL DEFAULT 'open',
    is_base_period               BOOLEAN      NOT NULL DEFAULT FALSE,
    accounting_period_closed_at  TIMESTAMP        NULL,
    accounting_period_closed_by  INTEGER          NULL REFERENCES employee(employee_id),
    accounting_period_created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accounting_period_created_by INTEGER          NULL REFERENCES employee(employee_id),
    accounting_period_updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accounting_period_updated_by INTEGER          NULL REFERENCES employee(employee_id),

    CONSTRAINT uq_ap_uuid      UNIQUE (accounting_period_uuid),
    CONSTRAINT uq_ap_fy_number UNIQUE (fiscal_year_id, period_number),
    CONSTRAINT uq_ap_start     UNIQUE (period_start),
    CONSTRAINT chk_ap_range    CHECK (period_end >= period_start),
    CONSTRAINT chk_ap_number   CHECK (period_number >= 1 AND period_number <= 12),
    CONSTRAINT chk_ap_status   CHECK (accounting_period_status IN ('open','closed')),
    -- closed_at pairs with the STATUS, not with closed_by: the actor may
    -- legitimately be NULL when an employee id cannot be resolved, exactly as
    -- cash_transfer_posted_by may be.
    CONSTRAINT chk_ap_closed_pair CHECK (
        (accounting_period_status = 'closed') = (accounting_period_closed_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_ap_fiscal_year ON accounting_period (fiscal_year_id, period_number);
CREATE INDEX IF NOT EXISTS idx_ap_range       ON accounting_period (period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_ap_status      ON accounting_period (accounting_period_status);

-- Overlapping periods would make "which period covers this date?" ambiguous,
-- and journal.CheckPeriodOpen answers exactly that question on every GL write.
-- UNIQUE(period_start) alone does not prevent an overlap on the END side, so
-- the invariant is expressed structurally with a range exclusion rather than
-- trusted to Go-side validation. gist over daterange is core Postgres: no
-- btree_gist is needed, because no equality column participates.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'excl_ap_no_overlap') THEN
        ALTER TABLE accounting_period
            ADD CONSTRAINT excl_ap_no_overlap
            EXCLUDE USING gist (daterange(period_start, period_end, '[]') WITH &&);
    END IF;
END $$;

-- 4. accounting_period_history -- append-only trail, written inside the same
--    transaction as the status change it documents.
CREATE TABLE IF NOT EXISTS accounting_period_history (
    accounting_period_history_id SERIAL      PRIMARY KEY,
    accounting_period_id         INTEGER     NOT NULL REFERENCES accounting_period(accounting_period_id),
    history_action               VARCHAR(20) NOT NULL,
    history_from_status          VARCHAR(10)     NULL,
    history_to_status            VARCHAR(10)     NULL,
    history_note                 TEXT        NOT NULL DEFAULT '',
    history_by                   INTEGER         NULL REFERENCES employee(employee_id),
    history_at                   TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT chk_ap_history_action
        CHECK (history_action IN ('generate','close','reopen','base_setup'))
);
CREATE INDEX IF NOT EXISTS idx_ap_history
    ON accounting_period_history (accounting_period_id, history_at DESC);

-- ═══════════════════════════════════════════════════════════════════════
-- Accounting Period Management -- sub-ledger locks, quarters, range
-- generation (enhancement)
--
-- Spec: docs/superpowers/specs/2026-07-31-accounting-period-locks-and-ranges-design.md
-- ═══════════════════════════════════════════════════════════════════════

-- 1. fiscal_quarter -- 4 rows per fiscal year, each spanning 3 consecutive
--    accounting_period rows (Q1 = periods 1-3, etc). Generated only going
--    forward, by the same generateYear call that creates a fiscal year's
--    periods -- not backfilled onto fiscal years that already exist.
--
-- fiscal_quarter_status is DERIVED, like fiscal_year_status: closed iff all
-- 3 of its periods are closed, recomputed alongside fiscal year status on
-- every period status change. Quarters have no lock of their own -- only a
-- derived status.
CREATE TABLE IF NOT EXISTS fiscal_quarter (
    fiscal_quarter_id         SERIAL       PRIMARY KEY,
    fiscal_quarter_uuid       UUID         NOT NULL DEFAULT gen_random_uuid(),
    fiscal_year_id            INTEGER      NOT NULL REFERENCES fiscal_year(fiscal_year_id),
    quarter_number            SMALLINT     NOT NULL,
    quarter_name              VARCHAR(20)  NOT NULL,
    quarter_start             DATE         NOT NULL,
    quarter_end               DATE         NOT NULL,
    fiscal_quarter_status     VARCHAR(10)  NOT NULL DEFAULT 'open',
    fiscal_quarter_created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fiscal_quarter_created_by INTEGER          NULL REFERENCES employee(employee_id),
    fiscal_quarter_updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fiscal_quarter_updated_by INTEGER          NULL REFERENCES employee(employee_id),

    CONSTRAINT uq_fq_uuid      UNIQUE (fiscal_quarter_uuid),
    CONSTRAINT uq_fq_fy_number UNIQUE (fiscal_year_id, quarter_number),
    CONSTRAINT uq_fq_start     UNIQUE (quarter_start),
    CONSTRAINT chk_fq_range    CHECK (quarter_end > quarter_start),
    CONSTRAINT chk_fq_number   CHECK (quarter_number BETWEEN 1 AND 4),
    CONSTRAINT chk_fq_status   CHECK (fiscal_quarter_status IN ('open','closed'))
);
CREATE INDEX IF NOT EXISTS idx_fq_fiscal_year ON fiscal_quarter (fiscal_year_id, quarter_number);
CREATE INDEX IF NOT EXISTS idx_fq_status      ON fiscal_quarter (fiscal_quarter_status);

-- 2. accounting_period gains a quarter link and three independent
--    sub-ledger lock columns (AP/AR/GL). Appended as ALTER ... ADD COLUMN
--    IF NOT EXISTS, never by editing the CREATE TABLE body above -- that
--    body is a no-op on every existing tenant, so an inline edit would
--    reach fresh databases only.
--
-- accounting_period_status stops being written directly by a lock change
-- and becomes DERIVED from the three lock columns (closed iff all three are
-- closed) -- the same posture fiscal_year_status already has over its
-- periods. fiscal_quarter_id is nullable and NOT backfilled onto periods
-- that already exist (see fiscal_quarter comment above).
ALTER TABLE accounting_period
    ADD COLUMN IF NOT EXISTS fiscal_quarter_id INTEGER NULL REFERENCES fiscal_quarter(fiscal_quarter_id);
ALTER TABLE accounting_period
    ADD COLUMN IF NOT EXISTS ap_lock_status VARCHAR(10) NOT NULL DEFAULT 'open';
ALTER TABLE accounting_period
    ADD COLUMN IF NOT EXISTS ar_lock_status VARCHAR(10) NOT NULL DEFAULT 'open';
ALTER TABLE accounting_period
    ADD COLUMN IF NOT EXISTS gl_lock_status VARCHAR(10) NOT NULL DEFAULT 'open';

-- ADD CONSTRAINT is not idempotent -- guard each new CHECK on pg_constraint,
-- same pattern as chk_acct_settings_fy_start_month / excl_ap_no_overlap above.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_ap_lock_ap_status') THEN
        ALTER TABLE accounting_period
            ADD CONSTRAINT chk_ap_lock_ap_status
            CHECK (ap_lock_status IN ('open','closed'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_ap_lock_ar_status') THEN
        ALTER TABLE accounting_period
            ADD CONSTRAINT chk_ap_lock_ar_status
            CHECK (ar_lock_status IN ('open','closed'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_ap_lock_gl_status') THEN
        ALTER TABLE accounting_period
            ADD CONSTRAINT chk_ap_lock_gl_status
            CHECK (gl_lock_status IN ('open','closed'));
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_ap_fiscal_quarter ON accounting_period (fiscal_quarter_id);

-- 3. Widen chk_ap_history_action to accept the six new per-lock actions.
--    The original CHECK is INLINE inside accounting_period_history's
--    CREATE TABLE IF NOT EXISTS body -- do NOT edit it there, a second
--    CREATE TABLE IF NOT EXISTS is a no-op on every already-provisioned
--    tenant, so an inline edit would never reach them. DROP + unconditional
--    ADD is safe to re-run every boot: DROP IF EXISTS never errors, and ADD
--    always converges to the same, correct, widened definition.
ALTER TABLE accounting_period_history DROP CONSTRAINT IF EXISTS chk_ap_history_action;
ALTER TABLE accounting_period_history
    ADD CONSTRAINT chk_ap_history_action
    CHECK (history_action IN ('generate','close','reopen','base_setup',
                               'ap_lock','ap_unlock','ar_lock','ar_unlock',
                               'gl_lock','gl_unlock'));

-- 4. Backfill: a period already closed under the old single-status model
--    gets all three locks closed too, exactly once. Safe to re-run on every
--    boot (schema.sql re-runs in full every time): going forward
--    accounting_period_status is only ever set AS A DERIVATION of the three
--    lock columns (never independently), so "status = closed AND all three
--    locks still open" is a state that can only exist as leftover from data
--    created before this migration -- it cannot recur once the derivation
--    logic above is live, so this WHERE clause matches zero rows after the
--    first successful run.
UPDATE accounting_period
SET ap_lock_status = 'closed', ar_lock_status = 'closed', gl_lock_status = 'closed'
WHERE accounting_period_status = 'closed'
  AND ap_lock_status = 'open' AND ar_lock_status = 'open' AND gl_lock_status = 'open';

-- =====================================================================
-- REQUISITION MODULE
-- Spec: docs/superpowers/specs/2026-08-01-requisition-module-design.md
-- Reuses (already seeded, do not recreate): lkp_record_type REQN (id 12),
-- lkp_record_status rows for record_type=12 (DRFT/PAPV/APPV/CANC),
-- authz.ResourceRequisition, vendor, inventory_item, lkp_unit,
-- lkp_payment_terms, employee. Reads (never writes outside the conversion
-- path) purchase_order/purchase_order_item, owned by the Purchase Order
-- module. Adds zero seed stanzas.
-- =====================================================================

-- requisition (header) -- an internal request-to-buy: no ship-to block
-- (AD-4, it never leaves the tenant) and a nullable, suggestion-only vendor
-- (AD-2, unlike purchase_order's mandatory vendor).
CREATE TABLE IF NOT EXISTS requisition (
    requisition_id                SERIAL        PRIMARY KEY,
    requisition_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    requisition_number            VARCHAR(20)       NULL,  -- 'REQN-000001', generated post-insert in Go

    -- Classification
    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = REQN
    requisition_status            INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven -- AD-7, mirrors purchase_order_approval_status)
    requisition_approval_status   VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    requisition_approved_by       INTEGER           NULL REFERENCES employee(employee_id),

    -- Requester (AD-5: also the IDOR scope owner, no separate owner_id column)
    requisition_requested_by_id   INTEGER       NOT NULL REFERENCES employee(employee_id),
    requisition_department        VARCHAR(100)  NOT NULL DEFAULT '',  -- free text; no cost-center master exists yet

    -- Primary info
    requisition_needed_by_date    DATE              NULL,
    requisition_priority          VARCHAR(10)   NOT NULL DEFAULT 'normal',  -- low | normal | high | urgent
    requisition_memo              TEXT          NOT NULL DEFAULT '',

    -- Suggested vendor (AD-2: nullable, a suggestion the PO's real vendor may differ from)
    requisition_vendor_id         INTEGER           NULL REFERENCES vendor(vendor_id),
    requisition_vendor_name       VARCHAR(150)  NOT NULL DEFAULT '',
    requisition_payment_terms     INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),

    -- Money summary (stored; simplified per AD-3/AD-9 -- no discount, no shipping/adjustment)
    requisition_sales_tax_percent DECIMAL(6,4)  NOT NULL DEFAULT 0,
    requisition_subtotal          DECIMAL(15,2) NOT NULL DEFAULT 0,
    requisition_tax_total         DECIMAL(15,2) NOT NULL DEFAULT 0,
    requisition_estimated_total   DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Dynamic + audit
    requisition_custom_fields     JSONB         NOT NULL DEFAULT '{}',
    requisition_created_at        TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    requisition_created_by        INTEGER           NULL REFERENCES employee(employee_id),
    requisition_updated_at        TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    requisition_updated_by        INTEGER           NULL REFERENCES employee(employee_id),
    requisition_deleted_at        TIMESTAMP         NULL,
    requisition_deleted_by        INTEGER           NULL REFERENCES employee(employee_id),
    requisition_record_version    INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_requisition_uuid   UNIQUE (requisition_uuid),
    CONSTRAINT uq_requisition_number UNIQUE (requisition_number),
    CONSTRAINT chk_reqn_approval_status CHECK (requisition_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_reqn_priority        CHECK (requisition_priority IN ('low','normal','high','urgent')),
    CONSTRAINT chk_reqn_tax_percent     CHECK (requisition_sales_tax_percent >= 0 AND requisition_sales_tax_percent <= 100),
    CONSTRAINT chk_reqn_totals_nonneg   CHECK (requisition_subtotal >= 0 AND requisition_estimated_total >= 0),
    CONSTRAINT chk_reqn_soft_delete     CHECK (
        (requisition_deleted_at IS NULL AND requisition_deleted_by IS NULL) OR
        (requisition_deleted_at IS NOT NULL AND requisition_deleted_by IS NOT NULL)
    )
);

-- requisition_item (line items) -- mirrors purchase_order_item, minus the
-- receiving hook (a requisition is never received against) and per-line
-- discount/tax (AD-3: a requisition is a rough ask, not a priced commitment).
CREATE TABLE IF NOT EXISTS requisition_item (
    requisition_item_id       SERIAL        PRIMARY KEY,
    requisition_item_uuid     UUID          NOT NULL DEFAULT gen_random_uuid(),
    requisition_id            INTEGER       NOT NULL REFERENCES requisition(requisition_id) ON DELETE CASCADE,
    line_number                INTEGER       NOT NULL,
    inventory_item_id          INTEGER           NULL REFERENCES inventory_item(inventory_item_id),  -- NULL = free-text line

    -- Snapshots (frozen at add time -- never re-read from catalog)
    item_name                  VARCHAR(150)  NOT NULL DEFAULT '',
    sku                        VARCHAR(50)   NOT NULL DEFAULT '',
    description                 TEXT          NOT NULL DEFAULT '',
    unit_id                     INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                   VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                    DECIMAL(14,3) NOT NULL DEFAULT 0,
    estimated_unit_price        DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Stored line money (AD-9: qty * unit price only, no discount/tax term)
    estimated_amount            DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at             TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by             INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at             TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at             TIMESTAMP         NULL,
    item_record_version         INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_reqn_item_uuid UNIQUE (requisition_item_uuid),
    CONSTRAINT chk_reqni_qty     CHECK (quantity >= 0),
    CONSTRAINT chk_reqni_price   CHECK (estimated_unit_price >= 0)
);

-- requisition_history -- status/action trail (mirrors purchase_order_history)
CREATE TABLE IF NOT EXISTS requisition_history (
    requisition_history_id    SERIAL       PRIMARY KEY,
    requisition_id             INTEGER      NOT NULL REFERENCES requisition(requisition_id) ON DELETE CASCADE,
    from_status_id              INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                 INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                       VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | update | approve | convert
    actor_employee_id            INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                     JSONB        NOT NULL DEFAULT '{}',
    at                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_reqn_history_action CHECK (action IN ('create','transition','update','approve','convert'))
);

-- requisition_approver / requisition_approval (AD-7, exact structural copies
-- of purchase_order_approver / purchase_order_approval)
CREATE TABLE IF NOT EXISTS requisition_approver (
    requisition_approver_id   SERIAL      PRIMARY KEY,
    record_type_id             INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = REQN
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_requisition_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS requisition_approval (
    requisition_approval_id   SERIAL     PRIMARY KEY,
    requisition_id             INTEGER     NOT NULL REFERENCES requisition(requisition_id) ON DELETE CASCADE,
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- status the sign-off was for
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_requisition_approval UNIQUE (requisition_id, record_status_id, approver_employee_id)
);

-- requisition_conversion (AD-8 -- Requisition -> Purchase Order lineage,
-- mirrors quote_conversion). A requisition converts at most once.
CREATE TABLE IF NOT EXISTS requisition_conversion (
    requisition_conversion_id SERIAL       PRIMARY KEY,
    requisition_id             INTEGER      NOT NULL REFERENCES requisition(requisition_id) ON DELETE CASCADE,
    purchase_order_id          INTEGER      NOT NULL REFERENCES purchase_order(purchase_order_id) ON DELETE CASCADE,
    converted_at                TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    converted_by                 INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                     JSONB        NOT NULL DEFAULT '{}',  -- {requisitionItemUuid: purchaseOrderItemUuid} line mapping for audit

    CONSTRAINT uq_requisition_conversion_po UNIQUE (purchase_order_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_requisition_conversion_requisition
    ON requisition_conversion (requisition_id);

-- requisition indexes (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_reqn_requested_by  ON requisition (requisition_requested_by_id) WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_status        ON requisition (requisition_status)          WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_vendor        ON requisition (requisition_vendor_id)        WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_needed_by     ON requisition (requisition_needed_by_date)    WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_created_id    ON requisition (requisition_created_at, requisition_id) WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_updated_id    ON requisition (requisition_updated_at, requisition_id) WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_total_id      ON requisition (requisition_estimated_total, requisition_id) WHERE requisition_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqn_custom_gin    ON requisition USING GIN (requisition_custom_fields);

CREATE INDEX IF NOT EXISTS idx_reqni_requisition ON requisition_item (requisition_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reqni_item        ON requisition_item (inventory_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_reqni_line_active
    ON requisition_item (requisition_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_reqn_history_requisition ON requisition_history (requisition_id);

CREATE INDEX IF NOT EXISTS idx_requisition_approver_lookup
    ON requisition_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_requisition_approval_requisition ON requisition_approval (requisition_id);

CREATE INDEX IF NOT EXISTS idx_requisition_conversion_po ON requisition_conversion (purchase_order_id);

-- =====================================================================
-- VENDOR BILL MODULE
-- Spec: docs/superpowers/specs/2026-08-10-vendor-bill-module-design.md
-- Reuses (already seeded, do not recreate): lkp_record_type VBIL (id 15),
-- lkp_record_status rows for record_type=15 (DRFT/PAPV/APPV/PART/PAID/ODUE/VOID),
-- authz.ResourceVendorBill, the 'vendor_bill' JSONB workflow (custom-field
-- definition host), vendor, purchase_order, purchase_order_item,
-- inventory_item, lkp_unit, lkp_tax_rate, lkp_payment_terms, lkp_currency,
-- lkp_payment_method. Adds zero seed stanzas. Zero changes to any existing
-- table.
-- =====================================================================

-- vendor_bill (header) -- the AP mirror of invoice: what a vendor has billed
-- us, approved (AD-6), and settled via its own payment ledger (AD-7). Vendor
-- is fixed at creation (AD-2); Purchase Order link is optional (AD-4); no
-- address block -- an inbound document is never rendered and mailed (AD-13).
CREATE TABLE IF NOT EXISTS vendor_bill (
    vendor_bill_id                SERIAL        PRIMARY KEY,
    vendor_bill_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                 INTEGER          NULL,  -- platform owner stamp, no cross-DB FK
    vendor_bill_number             VARCHAR(20)      NULL,  -- 'VBIL-000001', generated post-insert in Go

    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VBIL
    vendor_bill_status              INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (AD-6, mirrors purchase_order_approval_status)
    vendor_bill_approval_status     VARCHAR(10)  NOT NULL DEFAULT 'none',
    vendor_bill_approved_by         INTEGER          NULL REFERENCES employee(employee_id),

    -- Counterparty (AD-2: fixed at creation, name snapshotted)
    vendor_bill_vendor_id           INTEGER       NOT NULL REFERENCES vendor(vendor_id),
    vendor_bill_vendor_name         VARCHAR(150)  NOT NULL DEFAULT '',

    -- Optional PO lineage (AD-4, AD-8) -- set only via the convert endpoint,
    -- never by manual Create/Update input.
    vendor_bill_purchase_order_id   INTEGER          NULL REFERENCES purchase_order(purchase_order_id) ON DELETE SET NULL,

    -- Primary info
    vendor_bill_vendor_invoice_number VARCHAR(50) NOT NULL DEFAULT '',  -- the vendor's own bill/invoice # (not globally unique)
    vendor_bill_reference_number    VARCHAR(50)  NOT NULL DEFAULT '',
    vendor_bill_date                DATE         NOT NULL DEFAULT CURRENT_DATE,
    vendor_bill_due_date            DATE             NULL,
    vendor_bill_sales_tax_percent   DECIMAL(6,4) NOT NULL DEFAULT 0,
    vendor_bill_memo                TEXT         NOT NULL DEFAULT '',
    vendor_bill_notes               TEXT         NOT NULL DEFAULT '',
    vendor_bill_internal_notes      TEXT         NOT NULL DEFAULT '',
    vendor_bill_terms_conditions    TEXT         NOT NULL DEFAULT '',

    -- Assignment (IDOR scope owner)
    vendor_bill_owner_id            INTEGER          NULL REFERENCES employee(employee_id),

    -- Terms / currency
    vendor_bill_payment_terms       INTEGER          NULL REFERENCES lkp_payment_terms(payment_terms_id),
    vendor_bill_currency            INTEGER          NULL REFERENCES lkp_currency(currency_id),
    vendor_bill_exchange_rate       DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    vendor_bill_subtotal            DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_discount_total      DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_tax_total           DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_adjustment          DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_grand_total         DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- AP balance (stored, sole writer is vendorbill.RecomputeBalance -- AD-7)
    vendor_bill_amount_paid         DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_balance_due         DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Dynamic + audit
    vendor_bill_custom_fields       JSONB        NOT NULL DEFAULT '{}',
    vendor_bill_created_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_bill_created_by          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_bill_updated_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_bill_updated_by          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_bill_deleted_at          TIMESTAMP        NULL,
    vendor_bill_deleted_by          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_bill_record_version      INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_bill_uuid     UNIQUE (vendor_bill_uuid),
    CONSTRAINT uq_vendor_bill_number   UNIQUE (vendor_bill_number),
    CONSTRAINT chk_vbil_approval_status CHECK (vendor_bill_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_vbil_tax_percent    CHECK (vendor_bill_sales_tax_percent >= 0 AND vendor_bill_sales_tax_percent <= 100),
    CONSTRAINT chk_vbil_totals_nonneg  CHECK (vendor_bill_subtotal >= 0 AND vendor_bill_grand_total >= 0),
    CONSTRAINT chk_vbil_paid_nonneg    CHECK (vendor_bill_amount_paid >= 0 AND vendor_bill_balance_due >= 0),
    CONSTRAINT chk_vbil_soft_delete    CHECK (
        (vendor_bill_deleted_at IS NULL AND vendor_bill_deleted_by IS NULL) OR
        (vendor_bill_deleted_at IS NOT NULL AND vendor_bill_deleted_by IS NOT NULL)
    )
);

-- vendor_bill_item (lines) -- mirrors invoice_item; purchase_order_item_id is
-- set only by the convert path (AD-8), never by manual line input.
CREATE TABLE IF NOT EXISTS vendor_bill_item (
    vendor_bill_item_id       SERIAL        PRIMARY KEY,
    vendor_bill_item_uuid     UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_bill_id            INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),
    purchase_order_item_id    INTEGER           NULL REFERENCES purchase_order_item(purchase_order_item_id) ON DELETE SET NULL,

    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description               TEXT          NOT NULL DEFAULT '',
    unit_id                   INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                  DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent          DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                DECIMAL(6,4)  NOT NULL DEFAULT 0,

    line_subtotal               DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                     DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                   DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by              INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at               TIMESTAMP        NULL,
    item_record_version           INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_vbi_uuid       UNIQUE (vendor_bill_item_uuid),
    CONSTRAINT chk_vbi_qty       CHECK (quantity >= 0),
    CONSTRAINT chk_vbi_unit_price CHECK (unit_price >= 0),
    CONSTRAINT chk_vbi_discount  CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_vbi_tax       CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- vendor_bill_history -- status/action trail (mirrors purchase_order_history)
CREATE TABLE IF NOT EXISTS vendor_bill_history (
    vendor_bill_history_id   SERIAL       PRIMARY KEY,
    vendor_bill_id            INTEGER      NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    from_status_id             INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                      VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id            INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                     JSONB        NOT NULL DEFAULT '{}',
    at                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- vendor_bill_approver / vendor_bill_approval (AD-6, exact structural copies
-- of purchase_order_approver / purchase_order_approval)
CREATE TABLE IF NOT EXISTS vendor_bill_approver (
    vendor_bill_approver_id   SERIAL      PRIMARY KEY,
    record_type_id             INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = VBIL
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_vendor_bill_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS vendor_bill_approval (
    vendor_bill_approval_id   SERIAL     PRIMARY KEY,
    vendor_bill_id             INTEGER    NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    record_status_id           INTEGER    NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id       INTEGER    NOT NULL REFERENCES employee(employee_id),
    approved_at                 TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_vendor_bill_approval UNIQUE (vendor_bill_id, record_status_id, approver_employee_id)
);

-- vendor_bill_payment (AD-7 settlement ledger) -- the sole source Recompute-
-- Balance sums to derive amount_paid/balance_due/status. Soft delete is the
-- "unapply" (mirrors payment_application's application_deleted_at).
CREATE TABLE IF NOT EXISTS vendor_bill_payment (
    vendor_bill_payment_id    SERIAL        PRIMARY KEY,
    vendor_bill_payment_uuid  UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_bill_id             INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    payment_method_id          INTEGER           NULL REFERENCES lkp_payment_method(payment_method_id),
    amount                      DECIMAL(15,2) NOT NULL,
    reference_number             VARCHAR(50)  NOT NULL DEFAULT '',
    memo                         TEXT         NOT NULL DEFAULT '',
    paid_at                      DATE         NOT NULL DEFAULT CURRENT_DATE,
    created_by                   INTEGER          NULL REFERENCES employee(employee_id),
    created_at                   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at                   TIMESTAMP        NULL,
    CONSTRAINT uq_vbp_uuid           UNIQUE (vendor_bill_payment_uuid),
    CONSTRAINT chk_vbp_amount_positive CHECK (amount > 0)
);

-- vendor_bill_conversion (AD-8 lineage) -- UNIQUE on vendor_bill_id ONLY,
-- deliberately NOT on purchase_order_id: a PO may be billed in installments
-- across multiple bills, so every call to ConvertFromPurchaseOrder creates a
-- new bill row rather than short-circuiting on an existing one.
CREATE TABLE IF NOT EXISTS vendor_bill_conversion (
    vendor_bill_conversion_id SERIAL      PRIMARY KEY,
    purchase_order_id          INTEGER     NOT NULL REFERENCES purchase_order(purchase_order_id) ON DELETE CASCADE,
    vendor_bill_id              INTEGER     NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    converted_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    converted_by                 INTEGER         NULL REFERENCES employee(employee_id),
    snapshot                     JSONB       NOT NULL DEFAULT '{}',
    CONSTRAINT uq_vendor_bill_conversion_bill UNIQUE (vendor_bill_id)
);

-- vendor_bill indexes (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_vbil_vendor        ON vendor_bill (vendor_bill_vendor_id)         WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_po             ON vendor_bill (vendor_bill_purchase_order_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_status         ON vendor_bill (vendor_bill_status)             WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_date           ON vendor_bill (vendor_bill_date)               WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_due_date       ON vendor_bill (vendor_bill_due_date)            WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_owner          ON vendor_bill (vendor_bill_owner_id)            WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_created_id     ON vendor_bill (vendor_bill_created_at, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_updated_id     ON vendor_bill (vendor_bill_updated_at, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_duedate_id     ON vendor_bill (vendor_bill_due_date, vendor_bill_id)   WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_grandtotal_id  ON vendor_bill (vendor_bill_grand_total, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_balance_id     ON vendor_bill (vendor_bill_balance_due, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_custom_gin     ON vendor_bill USING GIN (vendor_bill_custom_fields);

CREATE INDEX IF NOT EXISTS idx_vbi_bill   ON vendor_bill_item (vendor_bill_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbi_item   ON vendor_bill_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_vbi_po_item ON vendor_bill_item (purchase_order_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_vbi_line_active
    ON vendor_bill_item (vendor_bill_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_vbil_history_bill ON vendor_bill_history (vendor_bill_id);

CREATE INDEX IF NOT EXISTS idx_vendor_bill_approver_lookup
    ON vendor_bill_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_vendor_bill_approval_bill ON vendor_bill_approval (vendor_bill_id);

CREATE INDEX IF NOT EXISTS idx_vbp_bill ON vendor_bill_payment (vendor_bill_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_vendor_bill_conversion_po ON vendor_bill_conversion (purchase_order_id);

-- =====================================================================
-- Role-based dashboard widget allocation.
--
-- Lets an admin choose which dashboard widgets each role's members may see.
-- One row per configured role; a role with no row here falls back to the
-- widget catalog's default set (see dashboardui.DefaultWidgetIDs) -- row
-- presence, not row content, is what "configured" means, so an admin can
-- deliberately clear a role down to zero widgets without it reverting to
-- the defaults on next read.
-- =====================================================================

CREATE TABLE IF NOT EXISTS role_dashboard_widgets (
    role_id    UUID PRIMARY KEY REFERENCES roles(id) ON DELETE CASCADE,
    widget_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Vendor Bills + Vendor Payments module ───────────────────────────

-- 5.1 Three new lkp_record_status rows for VPAY (AD-7). VPAY reused
-- record_type_id 16's PEND/APPV/SENT/VOID block above, but the Go transition
-- map (vendorpayment/transitions.go) routes DRFT->PAPV->APPV, not PEND-based —
-- PAPV was missing here until this fix, which made a plain Submit-for-Approval
-- transition fail server-side with "Unknown target status: PAPV".
INSERT INTO lkp_record_status (record_status_code, record_status_name, record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT 'DRFT', 'Draft', record_type_id, TRUE, TRUE, 1 FROM lkp_record_type WHERE record_type_code = 'VPAY'
UNION ALL
SELECT 'PAPV', 'Pending Approval', record_type_id, TRUE, TRUE, 1 FROM lkp_record_type WHERE record_type_code = 'VPAY'
UNION ALL
SELECT 'SCHD', 'Scheduled', record_type_id, TRUE, TRUE, 1 FROM lkp_record_type WHERE record_type_code = 'VPAY'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- 5.2 vendor_bill / vendor_bill_history already exist (see the Vendor Bill
-- Module section above, added by the vendor-bills PR) -- vendor_payment only
-- needs to reference them, not redefine them.

-- 5.3 vendor_payment (header)
CREATE TABLE IF NOT EXISTS vendor_payment (
    vendor_payment_id               SERIAL        PRIMARY KEY,
    vendor_payment_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_payment_number           VARCHAR(20)       NULL,  -- 'VPAY-000001', generated post-insert in Go

    record_type                     INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VPAY
    vendor_payment_status            INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    vendor_payment_vendor_id          INTEGER       NOT NULL REFERENCES vendor(vendor_id),  -- fixed at creation
    vendor_payment_vendor_name        VARCHAR(150)  NOT NULL DEFAULT '',                      -- snapshot

    vendor_payment_method               INTEGER       NOT NULL REFERENCES lkp_payment_method(payment_method_id),
    vendor_payment_reference_number     VARCHAR(50)   NOT NULL DEFAULT '',
    vendor_payment_date                 DATE          NOT NULL DEFAULT CURRENT_DATE,
    vendor_payment_scheduled_date       DATE              NULL,  -- only meaningful once status = SCHD
    vendor_payment_currency             INTEGER           NULL REFERENCES lkp_currency(currency_id),
    vendor_payment_memo                 TEXT          NOT NULL DEFAULT '',
    vendor_payment_internal_notes       TEXT          NOT NULL DEFAULT '',

    vendor_payment_amount                DECIMAL(15,2) NOT NULL,                              -- immutable post-create (AD-12)
    vendor_payment_applied_total          DECIMAL(15,2) NOT NULL DEFAULT 0,                    -- rollup
    vendor_payment_unapplied_amount        DECIMAL(15,2) NOT NULL DEFAULT 0,                   -- rollup

    vendor_payment_approval_status          VARCHAR(10)  NOT NULL DEFAULT 'none',              -- AD-6
    vendor_payment_approved_by               INTEGER          NULL REFERENCES employee(employee_id),

    vendor_payment_owner_id                   INTEGER           NULL REFERENCES employee(employee_id),

    vendor_payment_custom_fields               JSONB        NOT NULL DEFAULT '{}',
    vendor_payment_created_at                   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_payment_created_by                    INTEGER          NULL REFERENCES employee(employee_id),
    vendor_payment_updated_at                     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_payment_updated_by                      INTEGER          NULL REFERENCES employee(employee_id),
    vendor_payment_deleted_at                       TIMESTAMP        NULL,
    vendor_payment_deleted_by                        INTEGER          NULL REFERENCES employee(employee_id),
    vendor_payment_record_version                     INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_payment_uuid       UNIQUE (vendor_payment_uuid),
    CONSTRAINT uq_vendor_payment_number     UNIQUE (vendor_payment_number),
    CONSTRAINT chk_vpay_approval_status     CHECK (vendor_payment_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_vpay_amount_pos          CHECK (vendor_payment_amount > 0),
    CONSTRAINT chk_vpay_applied_nonneg      CHECK (vendor_payment_applied_total >= 0 AND vendor_payment_unapplied_amount >= 0),
    CONSTRAINT chk_vpay_applied_le_amt      CHECK (vendor_payment_applied_total <= vendor_payment_amount),
    CONSTRAINT chk_vpay_soft_delete         CHECK (
        (vendor_payment_deleted_at IS NULL AND vendor_payment_deleted_by IS NULL) OR
        (vendor_payment_deleted_at IS NOT NULL AND vendor_payment_deleted_by IS NOT NULL)
    )
);

-- 5.4 vendor_payment_application (bill-application ledger)
CREATE TABLE IF NOT EXISTS vendor_payment_application (
    application_id              SERIAL        PRIMARY KEY,
    application_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_payment_id             INTEGER       NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    vendor_bill_id                 INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id),

    application_amount              DECIMAL(15,2) NOT NULL,

    application_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by            INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at             TIMESTAMP        NULL,  -- set = "unapplied"
    application_deleted_by              INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version           INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_payment_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_vpay_app_amount_pos            CHECK (application_amount > 0),
    CONSTRAINT chk_vpay_app_soft_delete           CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- At most one LIVE application per (vendor_payment, vendor_bill) pair -- re-applying
-- increases the existing row's amount instead of creating a duplicate.
CREATE UNIQUE INDEX IF NOT EXISTS uq_vpay_app_live_pair
    ON vendor_payment_application (vendor_payment_id, vendor_bill_id) WHERE application_deleted_at IS NULL;

-- 5.5 vendor_payment_refund (AD-5)
CREATE TABLE IF NOT EXISTS vendor_payment_refund (
    refund_id                    SERIAL        PRIMARY KEY,
    refund_uuid                  UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_payment_id             INTEGER       NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    vendor_bill_id                 INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id),

    refund_amount                   DECIMAL(15,2) NOT NULL,
    refund_reason                    VARCHAR(150) NOT NULL DEFAULT '',
    refund_reference_number           VARCHAR(50)  NOT NULL DEFAULT '',
    refund_memo                        TEXT         NOT NULL DEFAULT '',
    refund_refunded_at                  DATE         NOT NULL DEFAULT CURRENT_DATE,

    refund_created_at                    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    refund_created_by                     INTEGER          NULL REFERENCES employee(employee_id),
    refund_deleted_at                      TIMESTAMP        NULL,  -- set = "un-refund" (correction)
    refund_deleted_by                       INTEGER          NULL REFERENCES employee(employee_id),
    refund_record_version                    INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_payment_refund_uuid UNIQUE (refund_uuid),
    CONSTRAINT chk_vpay_refund_amount_pos    CHECK (refund_amount > 0),
    CONSTRAINT chk_vpay_refund_soft_delete   CHECK (
        (refund_deleted_at IS NULL AND refund_deleted_by IS NULL) OR
        (refund_deleted_at IS NOT NULL AND refund_deleted_by IS NOT NULL)
    )
);

-- 5.6 vendor_payment_history
CREATE TABLE IF NOT EXISTS vendor_payment_history (
    vendor_payment_history_id  SERIAL       PRIMARY KEY,
    vendor_payment_id           INTEGER      NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    from_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition',  -- create|update|transition|approve|apply|unapply|refund|unrefund
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 5.7 vendor_payment_approver / vendor_payment_approval (AD-6)
CREATE TABLE IF NOT EXISTS vendor_payment_approver (
    vendor_payment_approver_id   SERIAL      PRIMARY KEY,
    record_type_id                 INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = VPAY
    record_status_id               INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- = PAPV
    approver_employee_id           INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                     TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                     INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_vendor_payment_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS vendor_payment_approval (
    vendor_payment_approval_id   SERIAL     PRIMARY KEY,
    vendor_payment_id              INTEGER    NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    record_status_id               INTEGER    NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id           INTEGER    NOT NULL REFERENCES employee(employee_id),
    approved_at                    TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_vendor_payment_approval UNIQUE (vendor_payment_id, record_status_id, approver_employee_id)
);

-- 5.8 Indexes

-- vendor_bill / vendor_bill_history indexes already exist (see the Vendor
-- Bill Module section above).

-- vendor_payment
CREATE INDEX IF NOT EXISTS idx_vpay_vendor         ON vendor_payment (vendor_payment_vendor_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_status         ON vendor_payment (vendor_payment_status)    WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_date           ON vendor_payment (vendor_payment_date)      WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_scheduled      ON vendor_payment (vendor_payment_scheduled_date) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_owner          ON vendor_payment (vendor_payment_owner_id)  WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_created_id     ON vendor_payment (vendor_payment_created_at, vendor_payment_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_amount_id      ON vendor_payment (vendor_payment_amount, vendor_payment_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_unapplied_id   ON vendor_payment (vendor_payment_unapplied_amount, vendor_payment_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_custom_gin     ON vendor_payment USING GIN (vendor_payment_custom_fields);

-- children
CREATE INDEX IF NOT EXISTS idx_vpay_app_payment    ON vendor_payment_application (vendor_payment_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_app_bill        ON vendor_payment_application (vendor_bill_id)     WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_refund_payment  ON vendor_payment_refund (vendor_payment_id) WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_refund_bill      ON vendor_payment_refund (vendor_bill_id)     WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_history_payment   ON vendor_payment_history (vendor_payment_id);
CREATE INDEX IF NOT EXISTS idx_vpay_approver_lookup    ON vendor_payment_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_vpay_approval_payment    ON vendor_payment_approval (vendor_payment_id);

-- ===================================================================
-- Vendor Credit Module
-- Reuses (already seeded, do not recreate): lkp_record_type VCRD (id 17,
-- tenant/schema.sql:710) and its DRFT/APPV/APPL/VOID statuses (record_type=17,
-- tenant/schema.sql:752) -- the same status set as CRDT/credit_memo.
-- Spec: docs/superpowers/specs/2026-08-13-vendor-credit-module-design.md
-- ===================================================================

-- Extend vendor_bill with a credit rollup, mirroring invoice_credit_total's
-- separation of cash (vendor_bill_amount_paid) from credit-memo-style credit.
ALTER TABLE vendor_bill ADD COLUMN IF NOT EXISTS vendor_bill_credit_total DECIMAL(15,2) NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS vendor_credit (
    vendor_credit_id                SERIAL        PRIMARY KEY,
    vendor_credit_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_credit_number            VARCHAR(20)       NULL,  -- 'VCR-000001', generated post-insert in Go

    record_type                     INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VCRD (17)
    vendor_credit_status             INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    vendor_credit_vendor_id           INTEGER       NOT NULL REFERENCES vendor(vendor_id),  -- fixed at creation
    vendor_credit_vendor_name         VARCHAR(150)  NOT NULL DEFAULT '',                      -- snapshot

    vendor_credit_reference_number     VARCHAR(50)   NOT NULL DEFAULT '',
    vendor_credit_date                  DATE          NOT NULL DEFAULT CURRENT_DATE,
    vendor_credit_reason                 VARCHAR(150)  NOT NULL DEFAULT '',
    vendor_credit_memo                    TEXT          NOT NULL DEFAULT '',
    vendor_credit_internal_notes           TEXT          NOT NULL DEFAULT '',

    vendor_credit_owner_id                  INTEGER           NULL REFERENCES employee(employee_id),

    vendor_credit_grand_total                DECIMAL(15,2) NOT NULL,                          -- face amount, entered directly
    vendor_credit_applied_total               DECIMAL(15,2) NOT NULL DEFAULT 0,                -- rollup
    vendor_credit_unapplied_amount             DECIMAL(15,2) NOT NULL DEFAULT 0,               -- rollup

    vendor_credit_custom_fields                 JSONB        NOT NULL DEFAULT '{}',
    vendor_credit_created_at                     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_credit_created_by                      INTEGER          NULL REFERENCES employee(employee_id),
    vendor_credit_updated_at                       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_credit_updated_by                        INTEGER          NULL REFERENCES employee(employee_id),
    vendor_credit_deleted_at                         TIMESTAMP        NULL,
    vendor_credit_deleted_by                          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_credit_record_version                      INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_credit_uuid       UNIQUE (vendor_credit_uuid),
    CONSTRAINT uq_vendor_credit_number     UNIQUE (vendor_credit_number),
    CONSTRAINT chk_vcrd_amount_pos          CHECK (vendor_credit_grand_total > 0),
    CONSTRAINT chk_vcrd_applied_nonneg      CHECK (vendor_credit_applied_total >= 0 AND vendor_credit_unapplied_amount >= 0),
    CONSTRAINT chk_vcrd_applied_le_amt      CHECK (vendor_credit_applied_total <= vendor_credit_grand_total),
    CONSTRAINT chk_vcrd_soft_delete         CHECK (
        (vendor_credit_deleted_at IS NULL AND vendor_credit_deleted_by IS NULL) OR
        (vendor_credit_deleted_at IS NOT NULL AND vendor_credit_deleted_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS vendor_credit_history (
    vendor_credit_history_id   SERIAL       PRIMARY KEY,
    vendor_credit_id            INTEGER      NOT NULL REFERENCES vendor_credit(vendor_credit_id) ON DELETE CASCADE,
    from_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition',  -- create|update|transition|apply|reverse
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS vendor_credit_application (
    application_id              SERIAL        PRIMARY KEY,
    application_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_credit_id              INTEGER       NOT NULL REFERENCES vendor_credit(vendor_credit_id) ON DELETE CASCADE,
    vendor_bill_id                 INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id),

    application_amount              DECIMAL(15,2) NOT NULL,

    application_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by            INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at             TIMESTAMP        NULL,  -- set = "reversed"
    application_deleted_by              INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version           INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_credit_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_vcrd_app_amount_pos            CHECK (application_amount > 0),
    CONSTRAINT chk_vcrd_app_soft_delete           CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- At most one LIVE application per (vendor_credit, vendor_bill) pair -- re-applying
-- increases the existing row's amount instead of creating a duplicate (mirrors
-- uq_cm_app_live_pair / uq_vpay_app_live_pair).
CREATE UNIQUE INDEX IF NOT EXISTS uq_vcrd_app_live_pair
    ON vendor_credit_application (vendor_credit_id, vendor_bill_id) WHERE application_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_vcrd_vendor        ON vendor_credit (vendor_credit_vendor_id) WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_status         ON vendor_credit (vendor_credit_status)    WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_date           ON vendor_credit (vendor_credit_date)      WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_owner          ON vendor_credit (vendor_credit_owner_id)  WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_created_id     ON vendor_credit (vendor_credit_created_at, vendor_credit_id) WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_unapplied_id   ON vendor_credit (vendor_credit_unapplied_amount, vendor_credit_id) WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_custom_gin     ON vendor_credit USING GIN (vendor_credit_custom_fields);
CREATE INDEX IF NOT EXISTS idx_vcrd_history_credit ON vendor_credit_history (vendor_credit_id);
CREATE INDEX IF NOT EXISTS idx_vcrd_app_credit     ON vendor_credit_application (vendor_credit_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_app_bill       ON vendor_credit_application (vendor_bill_id)     WHERE application_deleted_at IS NULL;

-- =====================================================================
-- EXPENSE MODULE (Employee Expense Claims)
-- Reuses (already seeded, do not recreate): authz.ResourceExpense (already
-- has 5 catalog rows), employee, users, workflow_record_attachments
-- (receipts route through the generic attachment mechanism -- see
-- workflow.ResolveRecordAccess's "expense" branch, not a dedicated table).
-- Reuses (read-only) the pre-existing legacy v1 JSONB "expense" workflow
-- row (this file, ~line 2127) purely as the custom-field-definition host,
-- the same pattern requisition/purchase order/invoice already established
-- for their own same-named legacy rows.
-- Spec: docs/superpowers/specs/2026-08-17-expense-module-design.md
-- =====================================================================

-- 1. New lkp_record_type row (append-only). The record_type_id is resolved
-- by SUBSELECT on record_type_code below, never a hardcoded id (pattern
-- copied from the IADJ/ITRF/ICNT block at tenant/schema.sql:5947) --
-- lkp_record_status keys statuses to types by SERIAL assignment order, so a
-- literal id would silently mis-assign every downstream status the moment
-- this file's append order no longer matches assumptions made at write time.
INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name, record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('EXPN', 'expense', 'Expense', TRUE, TRUE, 1)
ON CONFLICT (record_type_code) DO NOTHING;

-- 2. lkp_record_status rows for EXPN, resolved by SUBSELECT (see note above).
INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES
    ('DRFT','Draft'), ('SUBM','Submitted'), ('APPV','Approved'),
    ('RJCT','Rejected'), ('REIM','Reimbursed')
) AS v(code, name)
CROSS JOIN lkp_record_type rt
WHERE rt.record_type_code = 'EXPN'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- 3. lkp_expense_category -- new tenant-editable lookup, mirrors
-- lkp_customer_type's shape. expense_category_coa_account_id is an optional
-- link to a GL expense account for future accounting work; nothing in this
-- module posts journal entries against it.
CREATE TABLE IF NOT EXISTS lkp_expense_category (
    expense_category_id             SERIAL       PRIMARY KEY,
    expense_category_name           VARCHAR(100) NOT NULL,
    expense_category_code           VARCHAR(20)  NOT NULL,
    expense_category_coa_account_id INTEGER          NULL REFERENCES coa_account(coa_account_id),
    expense_category_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    expense_category_is_system      BOOLEAN      NOT NULL DEFAULT TRUE,
    expense_category_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expense_category_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    expense_category_deleted_at     TIMESTAMP        NULL,
    expense_category_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_expense_category_code UNIQUE (expense_category_code)
);

INSERT INTO lkp_expense_category (expense_category_name, expense_category_code, expense_category_created_by) VALUES
    ('Travel',          'TRAVEL', 1),
    ('Meals',           'MEALS',  1),
    ('Office Supplies', 'OFFICE', 1),
    ('Equipment',       'EQUIP',  1),
    ('Software',        'SOFT',   1),
    ('Other',           'OTHER',  1)
ON CONFLICT (expense_category_code) DO NOTHING;

-- 4. expense (header) -- an employee expense claim: no vendor/payment-terms/
-- tax fields (AD: a reimbursement claim isn't a priced commitment against a
-- vendor). Claimant is always the acting employee (self-service, AD-5-style
-- owner-is-requester) and doubles as the IDOR scope owner.
CREATE TABLE IF NOT EXISTS expense (
    expense_id                SERIAL        PRIMARY KEY,
    expense_uuid               UUID          NOT NULL DEFAULT gen_random_uuid(),
    expense_number              VARCHAR(20)      NULL,  -- 'EXPN-000001', generated post-insert in Go

    record_type                 INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = EXPN
    expense_status               INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven, mirrors requisition_approval_status)
    expense_approval_status      VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    expense_approved_by          INTEGER           NULL REFERENCES employee(employee_id),
    expense_rejected_by          INTEGER           NULL REFERENCES employee(employee_id),
    expense_rejection_reason     TEXT          NOT NULL DEFAULT '',

    -- Claimant (also the IDOR scope owner -- no separate owner_id column)
    expense_claimant_id          INTEGER       NOT NULL REFERENCES employee(employee_id),
    expense_department           VARCHAR(100)  NOT NULL DEFAULT '',
    expense_memo                 TEXT          NOT NULL DEFAULT '',

    -- Money (stored; sum of line amounts -- no discount/tax term)
    expense_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Dynamic + audit
    expense_custom_fields        JSONB         NOT NULL DEFAULT '{}',
    expense_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expense_created_by           INTEGER           NULL REFERENCES employee(employee_id),
    expense_updated_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expense_updated_by           INTEGER           NULL REFERENCES employee(employee_id),
    expense_deleted_at           TIMESTAMP         NULL,
    expense_deleted_by           INTEGER           NULL REFERENCES employee(employee_id),
    expense_record_version       INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_expense_uuid   UNIQUE (expense_uuid),
    CONSTRAINT uq_expense_number UNIQUE (expense_number),
    CONSTRAINT chk_exp_approval_status CHECK (expense_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_exp_total_nonneg    CHECK (expense_total >= 0),
    CONSTRAINT chk_exp_soft_delete     CHECK (
        (expense_deleted_at IS NULL AND expense_deleted_by IS NULL) OR
        (expense_deleted_at IS NOT NULL AND expense_deleted_by IS NOT NULL)
    )
);

-- 5. expense_item (line items) -- one row per receipt/expense entry.
CREATE TABLE IF NOT EXISTS expense_item (
    expense_item_id            SERIAL        PRIMARY KEY,
    expense_item_uuid           UUID          NOT NULL DEFAULT gen_random_uuid(),
    expense_id                   INTEGER       NOT NULL REFERENCES expense(expense_id) ON DELETE CASCADE,
    line_number                   INTEGER       NOT NULL,
    category_id                   INTEGER       NOT NULL REFERENCES lkp_expense_category(expense_category_id),

    expense_date                  DATE          NOT NULL,
    amount                        DECIMAL(15,2) NOT NULL DEFAULT 0,
    description                   TEXT          NOT NULL DEFAULT '',

    item_created_at               TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by               INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at               TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at               TIMESTAMP         NULL,
    item_record_version           INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_exp_item_uuid UNIQUE (expense_item_uuid),
    CONSTRAINT chk_expi_amount  CHECK (amount >= 0)
);

-- 6. expense_history -- status/action trail (mirrors requisition_history).
CREATE TABLE IF NOT EXISTS expense_history (
    expense_history_id        SERIAL       PRIMARY KEY,
    expense_id                  INTEGER      NOT NULL REFERENCES expense(expense_id) ON DELETE CASCADE,
    from_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                         VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | update | approve | reject
    actor_employee_id              INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                       JSONB        NOT NULL DEFAULT '{}',
    at                             TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_exp_history_action CHECK (action IN ('create','transition','update','approve','reject'))
);

-- 7. expense_approver / expense_approval -- configuration-driven approval
-- gate, exact structural copies of requisition_approver / requisition_approval.
-- expense_approver.is_active is what keeps an inactive employee from
-- signing off, rejecting, or counting toward quorum.
CREATE TABLE IF NOT EXISTS expense_approver (
    expense_approver_id       SERIAL      PRIMARY KEY,
    record_type_id             INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = EXPN
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. SUBM
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_expense_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS expense_approval (
    expense_approval_id       SERIAL     PRIMARY KEY,
    expense_id                  INTEGER     NOT NULL REFERENCES expense(expense_id) ON DELETE CASCADE,
    record_status_id            INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- status the sign-off was for
    approver_employee_id        INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                  TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_expense_approval UNIQUE (expense_id, record_status_id, approver_employee_id)
);

-- expense indexes (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_exp_claimant        ON expense (expense_claimant_id) WHERE expense_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_exp_status          ON expense (expense_status)      WHERE expense_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_exp_created_id      ON expense (expense_created_at, expense_id) WHERE expense_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_exp_custom_gin      ON expense USING GIN (expense_custom_fields);
CREATE INDEX IF NOT EXISTS idx_exp_item_expense    ON expense_item (expense_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_exp_history_expense ON expense_history (expense_id);

-- -- 000036_approval_chain_generic_phase1 ----------------------------------
-- =====================================================================
-- Tenant migration 036: extends the AD-8 approval gate (proven on
-- Estimate/Quote/Sales Order, and already configured for Purchase Order/
-- Requisition/Vendor Bill/Vendor Payment/Expense/Fabrication Job) to
-- Invoice, Payment, Credit Memo and Refund -- the approvalchain "Sales"
-- rollout group. Every new approver/approval table is an exact structural
-- copy of estimate_approver/estimate_approval (approvalchain/engine.go
-- drives all of them generically; see approvalchain/registry.go for the
-- gate config).
--
-- Invoice already had a PAPV status (Pending Approval) from its original
-- design, so its gate is PAPV -> APPV, identical in shape to Estimate.
-- Payment and Refund already had PEND, so their gate is PEND -> APPV.
-- Credit Memo has no separate pending status at all -- its gate sits on
-- DRFT itself, with Void always exempt (approvalchain.AlwaysAllowedExitCodes)
-- so a draft credit memo can still be voided without approval sign-off.
--
-- Fabrication Job needs no schema change here -- it already has
-- job_approval_status/job_approved_by and its approver/approval tables from
-- migration 035; only its Go code moves onto the shared engine.
-- =====================================================================

ALTER TABLE invoice     ADD COLUMN IF NOT EXISTS invoice_approval_status     VARCHAR(10) NOT NULL DEFAULT 'none';
ALTER TABLE invoice     ADD COLUMN IF NOT EXISTS invoice_approved_by         INTEGER         NULL REFERENCES employee(employee_id);
ALTER TABLE payment     ADD COLUMN IF NOT EXISTS payment_approval_status     VARCHAR(10) NOT NULL DEFAULT 'none';
ALTER TABLE payment     ADD COLUMN IF NOT EXISTS payment_approved_by         INTEGER         NULL REFERENCES employee(employee_id);
ALTER TABLE credit_memo ADD COLUMN IF NOT EXISTS credit_memo_approval_status VARCHAR(10) NOT NULL DEFAULT 'none';
ALTER TABLE credit_memo ADD COLUMN IF NOT EXISTS credit_memo_approved_by     INTEGER         NULL REFERENCES employee(employee_id);
ALTER TABLE refund      ADD COLUMN IF NOT EXISTS refund_approval_status      VARCHAR(10) NOT NULL DEFAULT 'none';
ALTER TABLE refund      ADD COLUMN IF NOT EXISTS refund_approved_by          INTEGER         NULL REFERENCES employee(employee_id);

CREATE TABLE IF NOT EXISTS invoice_approver (
    invoice_approver_id     SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = INVC
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_invoice_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE TABLE IF NOT EXISTS invoice_approval (
    invoice_approval_id     SERIAL      PRIMARY KEY,
    invoice_id                INTEGER     NOT NULL REFERENCES invoice(invoice_id) ON DELETE CASCADE,
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_invoice_approval UNIQUE (invoice_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_invoice_approver_lookup ON invoice_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_invoice_approval_invoice ON invoice_approval (invoice_id);

CREATE TABLE IF NOT EXISTS payment_approver (
    payment_approver_id     SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = PYMT
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PEND
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_payment_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE TABLE IF NOT EXISTS payment_approval (
    payment_approval_id     SERIAL      PRIMARY KEY,
    payment_id                INTEGER     NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_payment_approval UNIQUE (payment_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_payment_approver_lookup ON payment_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_payment_approval_payment ON payment_approval (payment_id);

CREATE TABLE IF NOT EXISTS credit_memo_approver (
    credit_memo_approver_id SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = CRDT
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. DRFT
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_credit_memo_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE TABLE IF NOT EXISTS credit_memo_approval (
    credit_memo_approval_id SERIAL      PRIMARY KEY,
    credit_memo_id             INTEGER     NOT NULL REFERENCES credit_memo(credit_memo_id) ON DELETE CASCADE,
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_credit_memo_approval UNIQUE (credit_memo_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_credit_memo_approver_lookup ON credit_memo_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_credit_memo_approval_memo ON credit_memo_approval (credit_memo_id);

CREATE TABLE IF NOT EXISTS refund_approver (
    refund_approver_id      SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = RFND
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PEND
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_refund_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE TABLE IF NOT EXISTS refund_approval (
    refund_approval_id      SERIAL      PRIMARY KEY,
    refund_id                  INTEGER     NOT NULL REFERENCES refund(refund_id) ON DELETE CASCADE,
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                  TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_refund_approval UNIQUE (refund_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_refund_approver_lookup ON refund_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_refund_approval_refund ON refund_approval (refund_id);

-- -- 000037_approval_chain_generic_phase2 ----------------------------------
-- =====================================================================
-- Tenant migration 037: extends the AD-8 approval gate to Vendor Credit --
-- the approvalchain "Purchases" rollout group's one net-new module (every
-- other Purchases module -- Requisition, Purchase Order, Vendor Bill,
-- Vendor Payment, Expense -- already had its approver/approval tables from
-- an earlier migration; only their Go code moved onto the shared engine).
--
-- Vendor Credit has no separate pending status, mirroring Credit Memo
-- (migration 036) -- its gate sits on DRFT itself, with Void always exempt
-- (approvalchain.AlwaysAllowedExitCodes) so a draft vendor credit can still
-- be voided without approval sign-off.
-- =====================================================================

ALTER TABLE vendor_credit ADD COLUMN IF NOT EXISTS vendor_credit_approval_status VARCHAR(10) NOT NULL DEFAULT 'none';
ALTER TABLE vendor_credit ADD COLUMN IF NOT EXISTS vendor_credit_approved_by     INTEGER         NULL REFERENCES employee(employee_id);

CREATE TABLE IF NOT EXISTS vendor_credit_approver (
    vendor_credit_approver_id SERIAL      PRIMARY KEY,
    record_type_id            INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = VCRD
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. DRFT
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                  TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                  INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_vendor_credit_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);
CREATE TABLE IF NOT EXISTS vendor_credit_approval (
    vendor_credit_approval_id SERIAL      PRIMARY KEY,
    vendor_credit_id             INTEGER     NOT NULL REFERENCES vendor_credit(vendor_credit_id) ON DELETE CASCADE,
    record_status_id             INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id         INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                   TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_vendor_credit_approval UNIQUE (vendor_credit_id, record_status_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_vendor_credit_approver_lookup ON vendor_credit_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_vendor_credit_approval_credit ON vendor_credit_approval (vendor_credit_id);

-- -- 000038_approval_chain_history_action_fix --------------------------------
-- =====================================================================
-- Tenant migration 038: payment_history / credit_memo_history / refund_history's
-- action CHECK constraints (added in earlier migrations before these modules
-- had an approval gate) never got widened to allow 'approve' and
-- 'approve_override' the way estimate_history / quote_history /
-- sales_order_history / fabrication_job_history / requisition_history /
-- expense_history already were (invoice_history's own equivalent constraint
-- is fixed in place above, at its existing unconditional widening from
-- migration 034 -- see the comment there for why it can't be fixed here
-- instead). approvalchain.Approve (engine.go) writes exactly those two action
-- values for every sign-off and every super-admin override -- the INSERT
-- violated the CHECK, aborting the transaction and surfacing as a generic
-- "failed to approve" 500 on every Payment/Credit Memo/Refund approval, while
-- the modules with the wider CHECK (Estimate/Quote/Sales Order) worked fine.
--
-- Each of these three has no prior *unconditional* redefinition of its CHECK
-- (only the original migration's 'IF NOT EXISTS conname' guard, which is a
-- no-op forever on any tenant DB where the constraint already exists -- i.e.
-- every already-provisioned tenant), so adding the first unconditional
-- DROP+ADD here is safe and is what actually reaches already-broken tenants.
-- Widening-only, mirrors the sales_order_history / invoice_history 'convert'
-- widening in migration 034 and chk_fab_history_action's widening above.
-- =====================================================================

ALTER TABLE payment_history DROP CONSTRAINT IF EXISTS chk_payment_history_action;
ALTER TABLE payment_history ADD CONSTRAINT chk_payment_history_action
    CHECK (action IN ('create','apply','unapply','transition','approve','approve_override'));

ALTER TABLE credit_memo_history DROP CONSTRAINT IF EXISTS chk_credit_memo_history_action;
ALTER TABLE credit_memo_history ADD CONSTRAINT chk_credit_memo_history_action
    CHECK (action IN ('create','update','transition','apply','unapply','approve','approve_override'));

ALTER TABLE refund_history DROP CONSTRAINT IF EXISTS chk_refund_history_action;
ALTER TABLE refund_history ADD CONSTRAINT chk_refund_history_action
    CHECK (action IN ('create','update','transition','apply','unapply','approve','approve_override'));
