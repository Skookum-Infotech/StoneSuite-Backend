-- =====================================================================
-- Tenant-template schema — Phase 11: record attachments + audit log.
--
-- workflow_record_attachments: per-record file attachments stored in
-- Cloudflare R2. The storage_key encodes a UUID-based path that is
-- never derived from user-supplied filenames (path-traversal safe).
-- Original display name is preserved only in file_name (sanitized).
--
-- audit_logs: lightweight per-tenant audit trail used by attachment
-- operations (upload / download / delete) and future mutations.
-- =====================================================================

-- -----------------------------------------------------------------
-- Per-tenant audit log
-- -----------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_logs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    action        TEXT        NOT NULL,                           -- e.g. attachment.upload
    resource      TEXT        NOT NULL,                           -- e.g. record_attachment
    resource_id   TEXT        NOT NULL DEFAULT '',                -- the affected row's id
    details       JSONB       NOT NULL DEFAULT '{}'::jsonb,       -- arbitrary context
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor      ON audit_logs(actor_user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action     ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_resource   ON audit_logs(resource, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC);

-- -----------------------------------------------------------------
-- Record attachments
-- -----------------------------------------------------------------
CREATE TABLE IF NOT EXISTS workflow_record_attachments (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    record_id            UUID        NOT NULL REFERENCES workflow_records(id) ON DELETE CASCADE,
    file_name            TEXT        NOT NULL,                    -- sanitized display name
    content_type         TEXT        NOT NULL,
    size_bytes           BIGINT      NOT NULL DEFAULT 0,
    storage_key          TEXT        NOT NULL UNIQUE,             -- R2 object key (UUID-based)
    checksum_sha256      TEXT        NOT NULL DEFAULT '',         -- hex SHA-256 of the uploaded file
    -- status is the v1 extension point for async malware scanning:
    -- pending → (scanner sets) clean | infected | failed
    status               TEXT        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'clean', 'infected', 'failed')),
    uploaded_by_user_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_wf_record_attachments_record ON workflow_record_attachments(record_id);
