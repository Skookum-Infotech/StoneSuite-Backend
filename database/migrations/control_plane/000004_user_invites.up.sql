-- =====================================================================
-- Control-plane migration 004: workspace user invite tokens.
--
-- Unlike tenant_invites (which onboard a brand-new company/tenant),
-- user_invites allow an existing tenant admin to invite colleagues
-- into their workspace. The token is validated by a public endpoint
-- that does NOT have a JWT, so it must live in the control plane
-- where it can be looked up without knowing the tenant a priori.
--
-- Cross-DB references (initial_role_id -> tenant.roles, invited_by ->
-- identities) are stored as plain UUIDs with no FK constraint.
-- =====================================================================

CREATE TABLE IF NOT EXISTS user_invites (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email            VARCHAR(255) NOT NULL,
    full_name        VARCHAR(255) NOT NULL DEFAULT '',
    initial_role_id  UUID,                          -- tenant-DB roles.id (cross-DB, no FK)
    token            VARCHAR(128) UNIQUE NOT NULL,
    status           VARCHAR(16)  NOT NULL DEFAULT 'pending',
    invited_by       UUID         REFERENCES identities(id) ON DELETE SET NULL,
    expires_at       TIMESTAMPTZ  NOT NULL,
    accepted_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT user_invites_status_chk CHECK (status IN ('pending', 'accepted', 'revoked'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_invites_token
    ON user_invites(token);

CREATE INDEX IF NOT EXISTS idx_user_invites_tenant
    ON user_invites(tenant_id);

-- Enforce one pending invite per email per tenant (allows revoked/accepted duplicates).
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_invites_tenant_email_pending
    ON user_invites(tenant_id, LOWER(email))
    WHERE status = 'pending';
