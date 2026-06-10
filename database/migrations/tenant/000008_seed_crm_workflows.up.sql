-- =====================================================================
-- Tenant-template schema — Phase 8: Seed CRM workflows (Lead/Prospect/Customer).
--
-- On first apply (new tenant): inserts default workflows with states, transitions, and fields.
-- On re-apply (existing tenant): skips workflows that already exist (idempotent).
-- =====================================================================

-- Create a temporary table to store state IDs for use in transitions.
CREATE TEMP TABLE _wf_states (workflow_key TEXT, state_key TEXT, state_id UUID) ON COMMIT DROP;

DO $$
DECLARE
  v_workflow_id UUID;
BEGIN

-- ===== LEAD WORKFLOW =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'lead') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('lead', 'Lead', 'Inbound leads pipeline.', TRUE, TRUE, 1)
  RETURNING id INTO v_workflow_id;

  -- Insert states and track IDs.
  WITH inserted_states AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color)
    VALUES
      (v_workflow_id, 'lead_new', 'LEAD-New', TRUE, FALSE, 0, '#64748b'),
      (v_workflow_id, 'lead_in_progress', 'LEAD-In Progress', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'lead_qualified', 'LEAD-Qualified', FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'lead_unqualified', 'LEAD-UnQualified', FALSE, TRUE, 3, '#ef4444'),
      (v_workflow_id, 'lead_converted', 'LEAD-Converted', FALSE, TRUE, 4, '#22c55e'),
      (v_workflow_id, 'lead_dead', 'LEAD-Dead', FALSE, TRUE, 5, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states (workflow_key, state_key, state_id)
  SELECT 'lead', key, id FROM inserted_states;

  -- Insert transitions using stored state IDs.
  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT
    v_workflow_id,
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'lead' AND state_key = t.from_key),
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'lead' AND state_key = t.to_key),
    t.name, '{}'::jsonb, t.sort_order
  FROM (
    VALUES
      ('lead_new', 'lead_in_progress', 'Start Progress', 0),
      ('lead_new', 'lead_unqualified', 'Disqualify', 1),
      ('lead_in_progress', 'lead_qualified', 'Qualify', 2),
      ('lead_in_progress', 'lead_unqualified', 'Disqualify', 3),
      ('lead_in_progress', 'lead_dead', 'Mark Dead', 4),
      ('lead_qualified', 'lead_converted', 'Convert', 5),
      ('lead_qualified', 'lead_dead', 'Mark Dead', 6)
  ) AS t(from_key, to_key, name, sort_order);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order)
  VALUES
    (v_workflow_id, 'company_name', 'Company Name', 'string', TRUE, '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email', 'Email', 'email', TRUE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone', 'Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'first_name', 'First Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'last_name', 'Last Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4),
    (v_workflow_id, 'source', 'Source', 'enum', FALSE, '["web", "referral", "event", "cold_call", "partner"]'::jsonb, '{}'::jsonb, 5),
    (v_workflow_id, 'estimated_value', 'Estimated Value', 'number', FALSE, '[]'::jsonb, '{}'::jsonb, 6),
    (v_workflow_id, 'sales_rep', 'Sales Rep', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 7),
    (v_workflow_id, 'territory', 'Territory', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 8);

  DELETE FROM _wf_states WHERE workflow_key = 'lead';
END IF;

-- ===== PROSPECT WORKFLOW =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'prospect') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('prospect', 'Prospect', 'Active sales opportunities.', TRUE, TRUE, 2)
  RETURNING id INTO v_workflow_id;

  WITH inserted_states AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color)
    VALUES
      (v_workflow_id, 'prospect_in_discussion', 'PROSPECT-In Discussion', TRUE, FALSE, 0, '#64748b'),
      (v_workflow_id, 'prospect_identified_dms', 'PROSPECT-Identified Decision Makers', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'prospect_qualified', 'PROSPECT-Qualified', FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'prospect_proposal', 'PROSPECT-Proposal', FALSE, FALSE, 3, '#f59e0b'),
      (v_workflow_id, 'prospect_in_negotiation', 'PROSPECT-In Negotiation', FALSE, FALSE, 4, '#f97316'),
      (v_workflow_id, 'prospect_purchasing', 'PROSPECT-Purchasing', FALSE, FALSE, 5, '#a855f7'),
      (v_workflow_id, 'prospect_closed_lost', 'PROSPECT-Closed Lost', FALSE, TRUE, 6, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states (workflow_key, state_key, state_id)
  SELECT 'prospect', key, id FROM inserted_states;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT
    v_workflow_id,
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'prospect' AND state_key = t.from_key),
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'prospect' AND state_key = t.to_key),
    t.name, '{}'::jsonb, t.sort_order
  FROM (
    VALUES
      ('prospect_in_discussion', 'prospect_identified_dms', 'Identify Decision Makers', 0),
      ('prospect_in_discussion', 'prospect_closed_lost', 'Close Lost', 1),
      ('prospect_identified_dms', 'prospect_qualified', 'Qualify', 2),
      ('prospect_identified_dms', 'prospect_closed_lost', 'Close Lost', 3),
      ('prospect_qualified', 'prospect_proposal', 'Send Proposal', 4),
      ('prospect_qualified', 'prospect_closed_lost', 'Close Lost', 5),
      ('prospect_proposal', 'prospect_in_negotiation', 'Begin Negotiation', 6),
      ('prospect_proposal', 'prospect_closed_lost', 'Close Lost', 7),
      ('prospect_in_negotiation', 'prospect_purchasing', 'Move to Purchase', 8),
      ('prospect_in_negotiation', 'prospect_closed_lost', 'Close Lost', 9)
  ) AS t(from_key, to_key, name, sort_order);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order)
  VALUES
    (v_workflow_id, 'company_name', 'Company Name', 'string', TRUE, '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email', 'Email', 'email', TRUE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone', 'Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'deal_size', 'Deal Size', 'number', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'close_date', 'Expected Close Date', 'date', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states WHERE workflow_key = 'prospect';
END IF;

-- ===== CUSTOMER WORKFLOW =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'customer') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('customer', 'Customer', 'Customer lifecycle.', TRUE, TRUE, 3)
  RETURNING id INTO v_workflow_id;

  WITH inserted_states AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color)
    VALUES
      (v_workflow_id, 'customer_closed_won', 'CUSTOMER-Closed Won', TRUE, FALSE, 0, '#22c55e'),
      (v_workflow_id, 'customer_renewal', 'CUSTOMER-Renewal', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'customer_closed_lost', 'CUSTOMER-Closed Lost', FALSE, TRUE, 2, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states (workflow_key, state_key, state_id)
  SELECT 'customer', key, id FROM inserted_states;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT
    v_workflow_id,
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'customer' AND state_key = t.from_key),
    (SELECT state_id FROM _wf_states WHERE workflow_key = 'customer' AND state_key = t.to_key),
    t.name, '{}'::jsonb, t.sort_order
  FROM (
    VALUES
      ('customer_closed_won', 'customer_renewal', 'Up for Renewal', 0),
      ('customer_closed_won', 'customer_closed_lost', 'Mark Lost', 1),
      ('customer_renewal', 'customer_closed_won', 'Renew', 2),
      ('customer_renewal', 'customer_closed_lost', 'Mark Lost', 3)
  ) AS t(from_key, to_key, name, sort_order);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order)
  VALUES
    (v_workflow_id, 'company_name', 'Company Name', 'string', TRUE, '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email', 'Email', 'email', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone', 'Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'legal_name', 'Legal Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'industry', 'Industry', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4),
    (v_workflow_id, 'website', 'Website', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 5),
    (v_workflow_id, 'country', 'Country', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 6),
    (v_workflow_id, 'currency', 'Currency', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 7),
    (v_workflow_id, 'timezone', 'Timezone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 8),
    (v_workflow_id, 'tax_id', 'Tax / VAT ID', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 9),
    (v_workflow_id, 'billing_address', 'Billing Address', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 10),
    (v_workflow_id, 'shipping_address', 'Shipping Address', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 11),
    (v_workflow_id, 'super_admin_name', 'Super Admin Name', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 12),
    (v_workflow_id, 'super_admin_email', 'Super Admin Email', 'email', TRUE, '[]'::jsonb, '{}'::jsonb, 13),
    (v_workflow_id, 'super_admin_phone', 'Super Admin Phone', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 14);

  DELETE FROM _wf_states WHERE workflow_key = 'customer';
END IF;

END $$;
