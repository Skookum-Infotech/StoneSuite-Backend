-- Migration 023: decouple workflow_record_attachments from workflow_records.
--
-- The attachment table was originally keyed to workflow_records(id), but the
-- v2 relational CRM design stores records in the customer table instead. Drop
-- the FK so attachments can be associated with any record UUID regardless of
-- which table it lives in. Existence is now enforced in application code.
ALTER TABLE workflow_record_attachments
  DROP CONSTRAINT IF EXISTS workflow_record_attachments_record_id_fkey;
