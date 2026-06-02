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
