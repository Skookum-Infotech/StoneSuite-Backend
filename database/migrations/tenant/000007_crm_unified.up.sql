-- =====================================================================
-- Tenant-template schema — Phase 7: Unified CRM workflow fields.
--
-- Adds pipeline_order to workflows so the Lead→Prospect→Customer
-- dependency chain can be enforced server-side when toggling enabled.
-- Adds parent_record_id to workflow_records to track lineage when a
-- lead is converted to a prospect or a prospect to a customer.
-- =====================================================================

-- pipeline_order: position in the CRM dependency chain.
-- 0 = unordered (non-CRM workflows). 1=Lead, 2=Prospect, 3=Customer.
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS pipeline_order INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_workflows_pipeline_order ON workflows(pipeline_order) WHERE pipeline_order > 0;

-- parent_record_id: the workflow_record this record was converted from.
-- NULL for records that were created directly (no conversion lineage).
ALTER TABLE workflow_records ADD COLUMN IF NOT EXISTS parent_record_id UUID REFERENCES workflow_records(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_workflow_records_parent ON workflow_records(parent_record_id) WHERE parent_record_id IS NOT NULL;
