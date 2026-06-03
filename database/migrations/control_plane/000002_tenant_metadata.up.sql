-- =====================================================================
-- Add a free-form metadata column to tenants so the platform owner can
-- capture rich company-onboarding details (legal name, addresses,
-- finance/admin contacts, etc.) without schema churn. The fields the
-- routing/lifecycle logic needs stay as real columns; everything else
-- the onboarding form collects lands here.
-- =====================================================================
ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
