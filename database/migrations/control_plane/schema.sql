-- =====================================================================
-- StoneSuite Control-Plane Schema — single canonical file.
--
-- This file IS the current schema. To change the control-plane schema,
-- edit this file directly. On every startup the runner applies this file
-- idempotently (CREATE IF NOT EXISTS / INSERT ON CONFLICT DO NOTHING /
-- ALTER IF NOT EXISTS), so a fresh database and an existing one both
-- converge to the same state without version tracking.
--
-- There are no numbered migration files. History lives in git.
-- =====================================================================

-- ── tenants ───────────────────────────────────────────────────────────
-- One row per customer organisation (+ the platform owner itself).
-- Holds routing (db_name / db_connection_ref) and lifecycle state.
CREATE TABLE IF NOT EXISTS tenants (
    id                 UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    slug               VARCHAR(63)  NOT NULL UNIQUE,
    display_name       VARCHAR(255) NOT NULL,
    status             VARCHAR(32)  NOT NULL DEFAULT 'invited',
    is_platform_owner  BOOLEAN      NOT NULL DEFAULT FALSE,

    db_name            VARCHAR(63),
    db_connection_ref  TEXT,
    region             VARCHAR(64)  NOT NULL DEFAULT 'default',

    schema_version     INT          NOT NULL DEFAULT 0,
    migration_status   VARCHAR(32)  NOT NULL DEFAULT 'pending',

    design_version     VARCHAR(16)  NOT NULL DEFAULT 'v2',

    metadata           JSONB        NOT NULL DEFAULT '{}'::jsonb,

    r2_bucket          TEXT         NOT NULL DEFAULT '',

    deleted_at         TIMESTAMPTZ,
    hard_delete_after  TIMESTAMPTZ,

    created_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_platform_owner
    ON tenants(is_platform_owner) WHERE is_platform_owner = TRUE;

-- Ensure r2_bucket column exists (idempotent for DBs created before this column was added).
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS r2_bucket TEXT NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS design_version VARCHAR(16) NOT NULL DEFAULT 'v2';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Promote all tenants to design v2 (only v2 is supported).
UPDATE tenants SET design_version = 'v2' WHERE design_version IS NULL OR design_version != 'v2';
ALTER TABLE tenants ALTER COLUMN design_version SET DEFAULT 'v2';

-- Widen db_connection_ref to TEXT (was VARCHAR(255) in early schemas).
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'db_connection_ref' AND data_type != 'text'
  ) THEN
    ALTER TABLE tenants ALTER COLUMN db_connection_ref TYPE TEXT;
  END IF;
END $$;

-- ── identities ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS identities (
    id                       UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email                    VARCHAR(255) NOT NULL UNIQUE,
    password_hash            TEXT,
    full_name                VARCHAR(255) NOT NULL DEFAULT '',
    email_verified           BOOLEAN      NOT NULL DEFAULT FALSE,
    email_verification_code  TEXT,
    sso_provider             VARCHAR(50),
    sso_subject              TEXT,
    failed_login_attempts    INT          NOT NULL DEFAULT 0,
    is_locked                BOOLEAN      NOT NULL DEFAULT FALSE,
    locked_until             TIMESTAMPTZ,
    password_reset_token     TEXT,
    password_reset_expiry    TIMESTAMPTZ,
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_identities_email  ON identities(LOWER(email));
CREATE INDEX IF NOT EXISTS idx_identities_tenant ON identities(tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_identities_sso
    ON identities(sso_provider, sso_subject) WHERE sso_provider IS NOT NULL;

-- SAML logout support: last-known IdP session index, set on each SAML login,
-- read back on SP-initiated logout to build a matching LogoutRequest.
ALTER TABLE identities ADD COLUMN IF NOT EXISTS sso_session_index TEXT;

-- ── tenant_invites ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tenant_invites (
    id             UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    contact_email  VARCHAR(255) NOT NULL,
    token          VARCHAR(128) NOT NULL UNIQUE,
    status         VARCHAR(32)  NOT NULL DEFAULT 'pending',
    expires_at     TIMESTAMPTZ  NOT NULL,
    sent_at        TIMESTAMPTZ,
    accepted_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenant_invites_tenant ON tenant_invites(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_invites_token  ON tenant_invites(token);

-- ── tenant_sso_configs ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tenant_sso_configs (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider          VARCHAR(50)  NOT NULL,
    client_id         TEXT         NOT NULL,
    client_secret_enc TEXT         NOT NULL,
    issuer            TEXT,
    redirect_uri      TEXT,
    enabled           BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_tenant_sso_tenant ON tenant_sso_configs(tenant_id);

-- SAML SSO support: protocol discriminator + IdP metadata fields (nullable;
-- unused by existing OIDC configs, populated when protocol='saml').
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS protocol VARCHAR(50) NOT NULL DEFAULT 'oidc';
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS metadata_url TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS idp_entity_id TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS sso_url TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS slo_url TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS certificate_pem_enc TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS certificate_fingerprint TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS name_id_format VARCHAR(255) NOT NULL DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress';
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS metadata_fetched_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'tenant_sso_configs_protocol_check'
    ) THEN
        ALTER TABLE tenant_sso_configs
            ADD CONSTRAINT tenant_sso_configs_protocol_check CHECK (protocol IN ('oidc', 'saml'));
    END IF;
END $$;

-- Role granted to a user JIT-provisioned by this config's SAML flow (see
-- controllers/saml_acs.go). Deliberately NOT a foreign key: roles live in the
-- per-tenant database while this table is control-plane, so Postgres cannot
-- enforce the reference across databases -- the id is validated at write time
-- by controllers/sso.go against the caller's tenant pool. NULL keeps the
-- original conservative behaviour: provision the user with no role at all.
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS default_role_id UUID;

-- ── tenant_sso_domains ────────────────────────────────────────────────
-- Home-realm discovery: maps an email domain to the SSO config that should
-- handle sign-in for it, so the login page can resolve a work email to a
-- tenant + provider without asking the user for a workspace slug.
--
-- domain is globally unique (one domain resolves to exactly one config,
-- otherwise discovery is ambiguous). Cascades off the config so deleting an
-- SSO configuration cleans up its domains.
CREATE TABLE IF NOT EXISTS tenant_sso_domains (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    sso_config_id UUID        NOT NULL REFERENCES tenant_sso_configs(id) ON DELETE CASCADE,
    domain        TEXT        NOT NULL,
    -- Reserved for future DNS TXT ownership verification. NULL = unverified;
    -- no code path consults this yet, domains are trusted on registration.
    verified_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_sso_domains_domain
    ON tenant_sso_domains(LOWER(domain));
CREATE INDEX IF NOT EXISTS idx_tenant_sso_domains_config
    ON tenant_sso_domains(sso_config_id);

-- ── saml_requests ─────────────────────────────────────────────────────
-- SAML request state: tracks outstanding AuthnRequests so the ACS callback can
-- validate the response belongs to a real, recent, single-use request and
-- resolve which tenant initiated it (SAML assertions carry no tenant context
-- of their own). Rows are short-lived; expired rows are deleted on ACS/cleanup.
CREATE TABLE IF NOT EXISTS saml_requests (
    id          TEXT         PRIMARY KEY,
    tenant_id   UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider    VARCHAR(50)  NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_saml_requests_expires ON saml_requests(expires_at);
CREATE INDEX IF NOT EXISTS idx_saml_requests_tenant ON saml_requests(tenant_id);

-- ── saml_login_codes ──────────────────────────────────────────────────
-- SAML login handoff codes: short-lived, single-use codes exchanged by the
-- frontend (POST /api/auth/saml/exchange) for a real JWT after ACS succeeds.
-- Keeping the JWT out of the ACS redirect URL avoids leaking it via browser
-- history or Referer headers.
CREATE TABLE IF NOT EXISTS saml_login_codes (
    code         TEXT         PRIMARY KEY,
    identity_id  UUID         NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    tenant_id    UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_saml_login_codes_expires ON saml_login_codes(expires_at);

-- ── platform_admins ───────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS platform_admins (
    identity_id UUID        PRIMARY KEY REFERENCES identities(id) ON DELETE CASCADE,
    role        VARCHAR(50) NOT NULL DEFAULT 'platform_admin',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── platform_audit_logs ───────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS platform_audit_logs (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_identity_id UUID,
    actor_email       VARCHAR(255),
    tenant_id         UUID         REFERENCES tenants(id) ON DELETE SET NULL,
    action            VARCHAR(100) NOT NULL,
    details           JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_audit_tenant ON platform_audit_logs(tenant_id);
CREATE INDEX IF NOT EXISTS idx_platform_audit_action ON platform_audit_logs(action);

-- ── user_invites ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_invites (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email            VARCHAR(255) NOT NULL,
    full_name        VARCHAR(255) NOT NULL DEFAULT '',
    initial_role_id  UUID,
    token            VARCHAR(128) UNIQUE NOT NULL,
    status           VARCHAR(16)  NOT NULL DEFAULT 'pending',
    invited_by       UUID         REFERENCES identities(id) ON DELETE SET NULL,
    expires_at       TIMESTAMPTZ  NOT NULL,
    accepted_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT user_invites_status_chk CHECK (status IN ('pending', 'accepted', 'revoked'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_invites_token ON user_invites(token);
CREATE INDEX IF NOT EXISTS idx_user_invites_tenant ON user_invites(tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_invites_tenant_email_pending
    ON user_invites(tenant_id, LOWER(email)) WHERE status = 'pending';

-- ── async_jobs ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS async_jobs (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    job_type        VARCHAR(64)  NOT NULL,
    tenant_id       UUID         REFERENCES tenants(id) ON DELETE CASCADE,
    payload         JSONB        NOT NULL DEFAULT '{}',
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending',
    attempts        INT          NOT NULL DEFAULT 0,
    max_attempts    INT          NOT NULL DEFAULT 5,
    last_error      TEXT,
    idempotency_key VARCHAR(255) UNIQUE,
    progress        JSONB,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT async_jobs_status_chk CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'dead'))
);

CREATE INDEX IF NOT EXISTS idx_async_jobs_status_created ON async_jobs(status, created_at);
CREATE INDEX IF NOT EXISTS idx_async_jobs_tenant         ON async_jobs(tenant_id);

-- ── refresh_tokens ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id UUID        NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    token_hash  TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_identity ON refresh_tokens(identity_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash     ON refresh_tokens(token_hash);

-- ── cp_rag_chunks ─────────────────────────────────────────────────────
-- App-help/documentation vectors. Identical for every tenant and not
-- anyone's private data, so it lives ONCE in the control plane and is
-- retrieved WITHOUT any scope filter.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS cp_rag_chunks (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_key   TEXT NOT NULL,
    section   TEXT NOT NULL,
    content   TEXT NOT NULL,
    embedding vector(768) NOT NULL
);
CREATE INDEX IF NOT EXISTS cp_rag_chunks_embedding_idx
    ON cp_rag_chunks USING hnsw (embedding vector_cosine_ops);

-- Hybrid retrieval — lexical (keyword) arm beside the vector arm. A generated
-- tsvector over content + a GIN index lets exact terms / rare tokens (record
-- numbers, names, codes) that a 768-dim embedding blurs be matched precisely.
-- 'simple' config (no stemming) so identifiers like INC-2023-Q4-011 survive
-- tokenization. Idempotent + append-only: the generated STORED column is
-- auto-populated for existing rows on ADD.
ALTER TABLE cp_rag_chunks ADD COLUMN IF NOT EXISTS content_tsv tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED;
CREATE INDEX IF NOT EXISTS cp_rag_chunks_tsv_idx ON cp_rag_chunks USING gin (content_tsv);
