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

-- ── identity_tenants ──────────────────────────────────────────────────
-- Which workspaces a login may enter, and as what class of principal.
--
-- Exists because identities.tenant_id is a single NOT NULL column: one email,
-- one identity, one tenant. That holds fine for staff (an employee belongs to
-- one workspace) but not for a customer, who may buy from two StoneSuite
-- tenants and needs one login that reaches both.
--
-- This table is PORTAL-ONLY in v1 and there is deliberately no backfill: staff
-- auth continues to read identities.tenant_id exactly as before, so the blast
-- radius of the multi-tenant change is zero for existing users. The `kind`
-- column reserves 'staff' for a future migration of that path.
--
-- For a portal identity, identities.tenant_id degrades to a HOME HINT — the
-- workspace it was first created in. It is NOT the authority on which tenant a
-- portal session is operating in; the JWT's tenant_id claim is. Any code that
-- derives a portal user's active tenant from identities.tenant_id is a bug
-- (the session would silently jump workspaces). The column cannot be dropped
-- or relaxed here: down-migrations are forbidden and it is still load-bearing
-- for every staff identity.
--
-- The link carries no customer_id. The customer lives in the tenant database,
-- so that link belongs there (customer_portal_user) where it can be a real
-- foreign key — putting it here would invite a cross-database join.
CREATE TABLE IF NOT EXISTS identity_tenants (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id UUID        NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    tenant_id   UUID        NOT NULL REFERENCES tenants(id)    ON DELETE CASCADE,
    kind        VARCHAR(16) NOT NULL DEFAULT 'portal',   -- portal | staff (staff reserved, unused in v1)
    status      VARCHAR(16) NOT NULL DEFAULT 'active',   -- active | revoked
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at  TIMESTAMPTZ     NULL,
    CONSTRAINT uq_identity_tenant         UNIQUE (identity_id, tenant_id),
    CONSTRAINT chk_identity_tenant_kind   CHECK (kind   IN ('portal', 'staff')),
    CONSTRAINT chk_identity_tenant_status CHECK (status IN ('active', 'revoked'))
);
CREATE INDEX IF NOT EXISTS idx_identity_tenants_identity
    ON identity_tenants (identity_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_identity_tenants_tenant
    ON identity_tenants (tenant_id) WHERE status = 'active';

-- ── portal_invites ────────────────────────────────────────────────────
-- Customer-portal invitations, mirroring user_invites for workspace staff.
--
-- A dedicated table rather than reusing identities.password_reset_token,
-- which serves staff invites, staff password resets AND (previously) portal
-- setup from one column. Three flows sharing one credential column means a
-- token minted for one surface can be redeemed on another; separating the
-- portal onto its own token removes that class of bug rather than guarding
-- against it at every endpoint.
--
-- customer_uuid has no foreign key: the customer lives in the tenant database.
-- It is recorded so a resend can rebuild the invite without re-reading the
-- tenant DB, and so an invite remains attributable after the portal user row
-- is revoked.
CREATE TABLE IF NOT EXISTS portal_invites (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    identity_id   UUID         NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    email         VARCHAR(255) NOT NULL,
    full_name     VARCHAR(255) NOT NULL DEFAULT '',
    customer_uuid UUID         NOT NULL,   -- tenant-DB customer.customer_uuid; no cross-DB FK
    token         VARCHAR(128) UNIQUE NOT NULL,
    status        VARCHAR(16)  NOT NULL DEFAULT 'pending',
    invited_by    UUID         REFERENCES identities(id) ON DELETE SET NULL,
    expires_at    TIMESTAMPTZ  NOT NULL,
    accepted_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT portal_invites_status_chk CHECK (status IN ('pending', 'accepted', 'revoked'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_portal_invites_token  ON portal_invites(token);
CREATE INDEX IF NOT EXISTS idx_portal_invites_tenant        ON portal_invites(tenant_id);
CREATE INDEX IF NOT EXISTS idx_portal_invites_identity      ON portal_invites(identity_id);
-- One live invite per email per workspace; a resend refreshes that row rather
-- than stacking a second one.
CREATE UNIQUE INDEX IF NOT EXISTS idx_portal_invites_tenant_email_pending
    ON portal_invites(tenant_id, LOWER(email)) WHERE status = 'pending';

-- ── platform_feedback ─────────────────────────────────────────────────
-- In-app feedback/bug/feature-request tickets raised by tenant staff or
-- customer-portal users, reviewed by StoneSuite platform admins. Lives in
-- the control-plane DB (not the tenant DB) because the admin ticket list is
-- inherently cross-tenant — scoping it per-tenant would force a fan-out
-- read across every tenant database just to render one list.
--
-- ticket_seq backs the human-facing ticket number ("FB-000123"), formatted
-- in application code (see feedback.FormatTicketNumber) rather than stored
-- as text, so the sequence is the single source of truth and can never
-- drift out of sync with a duplicated string column.
CREATE SEQUENCE IF NOT EXISTS platform_feedback_ticket_seq;

CREATE TABLE IF NOT EXISTS platform_feedback (
    id                          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_seq                  BIGINT       NOT NULL DEFAULT nextval('platform_feedback_ticket_seq'),
    tenant_id                   UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    -- Reporter identity, kept nullable (ON DELETE SET NULL) because a ticket
    -- must survive the reporter's account being removed later. reporter_email
    -- / reporter_name are snapshotted at submission time for the same reason
    -- — the admin list must keep showing who reported it even after the
    -- identity row is gone.
    reporter_identity_id        UUID         REFERENCES identities(id) ON DELETE SET NULL,
    reporter_kind               VARCHAR(16)  NOT NULL,
    reporter_email              VARCHAR(255) NOT NULL DEFAULT '',
    reporter_name               VARCHAR(255) NOT NULL DEFAULT '',

    category                    VARCHAR(32)  NOT NULL,
    -- Which section of the app the reporter had open — "area", not
    -- "workspace": that word already means the tenant a customer-portal
    -- identity is signed into (identity_tenants). '' means unspecified
    -- (older rows, or a reporter who skipped it).
    area                        VARCHAR(32)  NOT NULL DEFAULT '',
    rating                      SMALLINT,
    description                 TEXT         NOT NULL,
    -- Captured silently from the reporter's browser at submission time (no
    -- extra field for them to fill in) so admins have repro context for free.
    page_url                    TEXT         NOT NULL DEFAULT '',
    user_agent                  TEXT         NOT NULL DEFAULT '',

    status                      VARCHAR(16)  NOT NULL DEFAULT 'new',
    priority                    VARCHAR(16)  NOT NULL DEFAULT 'normal',
    assigned_admin_identity_id  UUID         REFERENCES identities(id) ON DELETE SET NULL,
    internal_notes              TEXT         NOT NULL DEFAULT '',

    -- Last time the reporter viewed this ticket's thread; comments/status
    -- changes after this timestamp count toward their unread badge.
    reporter_last_seen_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    created_at                  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_platform_feedback_ticket_seq UNIQUE (ticket_seq),
    CONSTRAINT chk_platform_feedback_reporter_kind CHECK (reporter_kind IN ('staff', 'portal')),
    CONSTRAINT chk_platform_feedback_category CHECK (category IN
        ('bug', 'feature_request', 'ux_improvement', 'performance', 'general')),
    CONSTRAINT chk_platform_feedback_rating CHECK (rating IS NULL OR (rating BETWEEN 1 AND 5)),
    CONSTRAINT chk_platform_feedback_status CHECK (status IN
        ('new', 'in_progress', 'done', 'cancelled')),
    CONSTRAINT chk_platform_feedback_priority CHECK (priority IN
        ('low', 'normal', 'high', 'urgent'))
);

-- Guard for a platform_feedback table created before the area column
-- existed (this table itself is new, but the guard costs nothing and
-- matches how every other column added after initial creation is done here).
ALTER TABLE platform_feedback ADD COLUMN IF NOT EXISTS area VARCHAR(32) NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_platform_feedback_area'
    ) THEN
        ALTER TABLE platform_feedback
            ADD CONSTRAINT chk_platform_feedback_area CHECK (area = '' OR area IN
                ('dashboard', 'crm', 'sales', 'purchases', 'inventory', 'finance', 'configuration', 'account', 'other'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_platform_feedback_tenant ON platform_feedback(tenant_id);
CREATE INDEX IF NOT EXISTS idx_platform_feedback_reporter
    ON platform_feedback(reporter_identity_id, tenant_id);
CREATE INDEX IF NOT EXISTS idx_platform_feedback_status ON platform_feedback(status);
CREATE INDEX IF NOT EXISTS idx_platform_feedback_created ON platform_feedback(created_at DESC);

-- ── platform_feedback_comments ───────────────────────────────────────
-- Reply thread for a ticket, shared by the reporter's "My Tickets" view and
-- the platform admin detail page. A status change is appended as a row with
-- event_type='status_change' rather than living in a separate history
-- table, so the reporter sees one unified timeline. is_internal rows are
-- admin-only notes and are filtered out of every reporter-facing response.
CREATE TABLE IF NOT EXISTS platform_feedback_comments (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    feedback_id         UUID         NOT NULL REFERENCES platform_feedback(id) ON DELETE CASCADE,
    author_identity_id  UUID         REFERENCES identities(id) ON DELETE SET NULL,
    author_kind         VARCHAR(16)  NOT NULL,
    author_name         VARCHAR(255) NOT NULL DEFAULT '',
    body                TEXT         NOT NULL DEFAULT '',
    is_internal         BOOLEAN      NOT NULL DEFAULT FALSE,
    event_type          VARCHAR(16)  NOT NULL DEFAULT 'comment',
    old_status          VARCHAR(16),
    new_status          VARCHAR(16),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_platform_feedback_comments_author_kind CHECK
        (author_kind IN ('staff', 'portal', 'platform_admin')),
    CONSTRAINT chk_platform_feedback_comments_event_type CHECK
        (event_type IN ('comment', 'status_change'))
);

CREATE INDEX IF NOT EXISTS idx_platform_feedback_comments_feedback
    ON platform_feedback_comments(feedback_id, created_at);

-- ── platform_feedback_attachments ────────────────────────────────────
-- Files are stored in the REPORTING TENANT's own R2 bucket (tenants.r2_bucket)
-- under key prefix `feedback/{feedback_id}/...` — that bucket already exists
-- and already has the app origin in its CORS policy for every tenant, so
-- submitting a feedback attachment needs no new bucket or CORS change.
CREATE TABLE IF NOT EXISTS platform_feedback_attachments (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    feedback_id      UUID         NOT NULL REFERENCES platform_feedback(id) ON DELETE CASCADE,
    file_name        TEXT         NOT NULL,
    content_type     TEXT         NOT NULL,
    size_bytes       BIGINT       NOT NULL DEFAULT 0,
    storage_key      TEXT         NOT NULL UNIQUE,
    checksum_sha256  TEXT         NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_feedback_attachments_feedback
    ON platform_feedback_attachments(feedback_id);
