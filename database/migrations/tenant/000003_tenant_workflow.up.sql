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
