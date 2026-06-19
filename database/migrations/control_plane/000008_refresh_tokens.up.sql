-- Control-plane migration 008: DB-backed refresh tokens.
--
-- Stores one record per active refresh token (identified by SHA-256 hash of
-- the raw token value). Revoked tokens keep their row with revoked_at set so
-- attempted reuse can be detected and flagged. The raw token never touches
-- the database — only its hash is stored.

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
