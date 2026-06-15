-- =====================================================================
-- Control-plane migration 005: durable async job queue.
--
-- Replaces the in-memory provisioning channel with a Postgres-backed
-- queue so jobs survive restarts and can be safely claimed by multiple
-- worker instances via `SELECT ... FOR UPDATE SKIP LOCKED`.
--
-- job_type identifies the handler (e.g. 'tenant_provision'). payload
-- carries handler-specific input. progress is an optional JSONB blob
-- a handler updates as it completes steps, so a resumed job can skip
-- already-completed work.
-- =====================================================================

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

CREATE INDEX IF NOT EXISTS idx_async_jobs_status_created
    ON async_jobs(status, created_at);

CREATE INDEX IF NOT EXISTS idx_async_jobs_tenant
    ON async_jobs(tenant_id);
