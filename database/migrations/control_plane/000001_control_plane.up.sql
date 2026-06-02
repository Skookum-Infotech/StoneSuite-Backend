-- =====================================================================
-- Control-Plane schema (shared "front desk" database).
-- Holds the tenant registry, login identities, the tenant->database
-- routing, invites, per-tenant SSO config, platform admins, and a
-- platform-level audit trail. NO tenant business data lives here.
-- =====================================================================

-- ---------------------------------------------------------------------
-- tenants: one row per customer organization (+ the platform owner).
-- Stores routing (db_name / db_connection_ref) and lifecycle state.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tenants (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug               VARCHAR(63)  NOT NULL UNIQUE,   -- used to build db_name (tenant_<slug>)
    display_name       VARCHAR(255) NOT NULL,
    status             VARCHAR(32)  NOT NULL DEFAULT 'invited',
                       -- invited | provisioning | active | suspended | deleted
    is_platform_owner  BOOLEAN      NOT NULL DEFAULT FALSE,

    -- Routing: which database this tenant's data lives in.
    db_name            VARCHAR(63),                    -- e.g. tenant_acme
    db_connection_ref  VARCHAR(255),                   -- secret-manager key / encrypted DSN ref
    region             VARCHAR(64)  NOT NULL DEFAULT 'default',

    -- Migration tracking (the resolver refuses to serve a failed tenant).
    schema_version     INT          NOT NULL DEFAULT 0,
    migration_status   VARCHAR(32)  NOT NULL DEFAULT 'pending', -- pending | ok | failed

    -- Soft-delete lifecycle (platform owner controls hard delete).
    deleted_at         TIMESTAMPTZ,
    hard_delete_after  TIMESTAMPTZ,                    -- grace window end (e.g. +30 days)

    created_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status);
-- Only one platform-owner tenant is expected.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_platform_owner
    ON tenants(is_platform_owner) WHERE is_platform_owner = TRUE;

-- ---------------------------------------------------------------------
-- identities: central login identity. A user belongs to exactly one
-- tenant. Email is globally unique (central auth). Password + SSO both
-- supported. (Generalizes the old single-DB `users` auth columns.)
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS identities (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email                    VARCHAR(255) NOT NULL UNIQUE,
    password_hash            TEXT,
    full_name                VARCHAR(255) NOT NULL DEFAULT '',
    email_verified           BOOLEAN NOT NULL DEFAULT FALSE,
    email_verification_code  TEXT,

    -- SSO linkage (optional, per-tenant IdP).
    sso_provider             VARCHAR(50),   -- entra | cognito | okta
    sso_subject              TEXT,          -- provider-side unique id

    -- Account-lock + password reset (reused from existing auth logic).
    failed_login_attempts    INT NOT NULL DEFAULT 0,
    is_locked                BOOLEAN NOT NULL DEFAULT FALSE,
    locked_until             TIMESTAMPTZ,
    password_reset_token     TEXT,
    password_reset_expiry    TIMESTAMPTZ,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_identities_email   ON identities(LOWER(email));
CREATE INDEX IF NOT EXISTS idx_identities_tenant  ON identities(tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_identities_sso
    ON identities(sso_provider, sso_subject) WHERE sso_provider IS NOT NULL;

-- ---------------------------------------------------------------------
-- tenant_invites: platform onboarding invites (generalizes the old
-- onboarding_invites). Accepting one triggers async provisioning.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tenant_invites (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    contact_email  VARCHAR(255) NOT NULL,
    token          VARCHAR(128) NOT NULL UNIQUE,
    status         VARCHAR(32)  NOT NULL DEFAULT 'pending', -- pending | accepted | expired | revoked
    expires_at     TIMESTAMPTZ  NOT NULL,
    sent_at        TIMESTAMPTZ,
    accepted_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenant_invites_tenant ON tenant_invites(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_invites_token  ON tenant_invites(token);

-- ---------------------------------------------------------------------
-- tenant_sso_configs: optional per-tenant identity provider config.
-- client_secret stored encrypted (secret-manager ref / ciphertext).
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tenant_sso_configs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider            VARCHAR(50) NOT NULL,            -- entra | cognito | okta
    client_id           TEXT NOT NULL,
    client_secret_enc   TEXT NOT NULL,                   -- encrypted; never plaintext
    issuer              TEXT,                            -- domain / tenant id / issuer URL
    redirect_uri        TEXT,
    enabled             BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_tenant_sso_tenant ON tenant_sso_configs(tenant_id);

-- ---------------------------------------------------------------------
-- platform_admins: identities granted cross-tenant platform powers
-- (your staff). Controls onboarding + tenant deletion lifecycle.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform_admins (
    identity_id  UUID PRIMARY KEY REFERENCES identities(id) ON DELETE CASCADE,
    role         VARCHAR(50) NOT NULL DEFAULT 'platform_admin',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------
-- platform_audit_logs: cross-tenant admin actions (invites, deletions,
-- restores, force-deletes). tenant_id nullable for global actions.
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform_audit_logs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_identity_id   UUID,
    actor_email         VARCHAR(255),
    tenant_id           UUID REFERENCES tenants(id) ON DELETE SET NULL,
    action              VARCHAR(100) NOT NULL,
    details             JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_audit_tenant ON platform_audit_logs(tenant_id);
CREATE INDEX IF NOT EXISTS idx_platform_audit_action ON platform_audit_logs(action);
