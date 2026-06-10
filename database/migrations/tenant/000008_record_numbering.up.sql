-- =====================================================================
-- Tenant-template schema — Phase 8: per-workflow record auto-numbering.
--
-- Lets a super admin configure auto-generated record numbers (prefix +
-- zero-padded sequence + suffix) per workflow. One row per workflow,
-- created lazily via upsert when the config is first set — no seeding
-- required for new or future workflows.
-- =====================================================================

CREATE TABLE IF NOT EXISTS workflow_numbering_configs (
    workflow_id  UUID PRIMARY KEY REFERENCES workflows(id) ON DELETE CASCADE,
    enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    prefix       TEXT NOT NULL DEFAULT '',
    suffix       TEXT NOT NULL DEFAULT '',
    min_digits   INT NOT NULL DEFAULT 1,
    next_number  BIGINT NOT NULL DEFAULT 1,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- record_number: the generated number assigned at creation time, e.g. "LEAD-0001".
-- NULL when numbering is not enabled for the record's workflow.
ALTER TABLE workflow_records ADD COLUMN IF NOT EXISTS record_number TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_records_record_number
    ON workflow_records(workflow_id, record_number) WHERE record_number IS NOT NULL;
