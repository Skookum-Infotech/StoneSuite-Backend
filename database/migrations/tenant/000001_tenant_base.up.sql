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
