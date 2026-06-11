-- =====================================================================
-- Tenant-template schema — Phase 10: Seed Sales & Purchases workflows.
--
-- Seeds 16 new workflows (8 Sales + 8 Purchases) with states, transitions,
-- and basic field definitions. Idempotent: skips workflows that already exist.
-- All use pipeline_order = 0 (no CRM conversion chain).
-- =====================================================================

CREATE TEMP TABLE _wf_states10 (workflow_key TEXT, state_key TEXT, state_id UUID) ON COMMIT DROP;

DO $$
DECLARE
  v_workflow_id UUID;
BEGIN

-- ===== ESTIMATE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'estimate') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('estimate', 'Estimate', 'Price estimates for customers.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'estimate_draft',    'ESTIMATE-Draft',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'estimate_sent',     'ESTIMATE-Sent',     FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'estimate_accepted', 'ESTIMATE-Accepted', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'estimate_rejected', 'ESTIMATE-Rejected', FALSE, TRUE,  3, '#ef4444'),
      (v_workflow_id, 'estimate_expired',  'ESTIMATE-Expired',  FALSE, TRUE,  4, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'estimate', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='estimate' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='estimate' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('estimate_draft', 'estimate_sent',     'Send to Customer', 0),
    ('estimate_sent',  'estimate_accepted', 'Accept',           1),
    ('estimate_sent',  'estimate_rejected', 'Reject',           2),
    ('estimate_sent',  'estimate_expired',  'Mark Expired',     3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'valid_until',   'Valid Until',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'estimate';
END IF;

-- ===== QUOTE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'quote') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('quote', 'Quote', 'Formal quotes issued to customers.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'quote_draft',    'QUOTE-Draft',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'quote_sent',     'QUOTE-Sent',     FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'quote_accepted', 'QUOTE-Accepted', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'quote_rejected', 'QUOTE-Rejected', FALSE, TRUE,  3, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'quote', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='quote' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='quote' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('quote_draft', 'quote_sent',     'Send Quote', 0),
    ('quote_sent',  'quote_accepted', 'Accept',     1),
    ('quote_sent',  'quote_rejected', 'Reject',     2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'valid_until',   'Valid Until',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'quote';
END IF;

-- ===== SALES ORDER =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'sales_order') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('sales_order', 'Sales Order', 'Confirmed customer orders.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'so_new',       'SO-New',       TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'so_confirmed', 'SO-Confirmed', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'so_processing','SO-Processing',FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'so_fulfilled', 'SO-Fulfilled', FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'so_cancelled', 'SO-Cancelled', FALSE, TRUE,  4, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'sales_order', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='sales_order' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='sales_order' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('so_new',       'so_confirmed',  'Confirm Order',  0),
    ('so_new',       'so_cancelled',  'Cancel',         1),
    ('so_confirmed', 'so_processing', 'Start Processing',2),
    ('so_confirmed', 'so_cancelled',  'Cancel',         3),
    ('so_processing','so_fulfilled',  'Mark Fulfilled', 4),
    ('so_processing','so_cancelled',  'Cancel',         5)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'order_date',    'Order Date',    'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'sales_order';
END IF;

-- ===== INSTALLATION / FABRICATION =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'installation') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('installation', 'Installation / Fabrication', 'Installation and fabrication job management.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'inst_scheduled',  'INST-Scheduled',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'inst_in_progress','INST-In Progress',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'inst_on_hold',    'INST-On Hold',      FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'inst_completed',  'INST-Completed',    FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'inst_cancelled',  'INST-Cancelled',    FALSE, TRUE,  4, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'installation', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='installation' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='installation' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('inst_scheduled',  'inst_in_progress','Start Work',   0),
    ('inst_scheduled',  'inst_cancelled',  'Cancel',       1),
    ('inst_in_progress','inst_on_hold',    'Put On Hold',  2),
    ('inst_in_progress','inst_completed',  'Mark Complete',3),
    ('inst_in_progress','inst_cancelled',  'Cancel',       4),
    ('inst_on_hold',    'inst_in_progress','Resume',       5),
    ('inst_on_hold',    'inst_cancelled',  'Cancel',       6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name',  'Customer Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'scheduled_date', 'Scheduled Date',  'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'location',       'Location/Address','string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'technician',     'Assigned Technician','string',FALSE,'[]'::jsonb,'{}'::jsonb, 3),
    (v_workflow_id, 'notes',          'Notes',           'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'installation';
END IF;

-- ===== INVOICE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'invoice') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('invoice', 'Invoice', 'Customer invoices and billing.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'inv_draft',   'INV-Draft',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'inv_issued',  'INV-Issued',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'inv_overdue', 'INV-Overdue', FALSE, FALSE, 2, '#f97316'),
      (v_workflow_id, 'inv_paid',    'INV-Paid',    FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'inv_void',    'INV-Void',    FALSE, TRUE,  4, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'invoice', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='invoice' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='invoice' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('inv_draft',  'inv_issued',  'Issue Invoice',  0),
    ('inv_draft',  'inv_void',    'Void',           1),
    ('inv_issued', 'inv_paid',    'Mark Paid',      2),
    ('inv_issued', 'inv_overdue', 'Mark Overdue',   3),
    ('inv_issued', 'inv_void',    'Void',           4),
    ('inv_overdue','inv_paid',    'Mark Paid',      5),
    ('inv_overdue','inv_void',    'Void',           6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'invoice_date',  'Invoice Date',  'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'due_date',      'Due Date',      'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'invoice';
END IF;

-- ===== PAYMENT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'payment') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('payment', 'Payment', 'Customer payment tracking and reconciliation.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'pmt_pending',    'PMT-Pending',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'pmt_received',   'PMT-Received',   FALSE, TRUE,  1, '#22c55e'),
      (v_workflow_id, 'pmt_refunded',   'PMT-Refunded',   FALSE, TRUE,  2, '#f97316'),
      (v_workflow_id, 'pmt_voided',     'PMT-Voided',     FALSE, TRUE,  3, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'payment', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='payment' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='payment' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('pmt_pending', 'pmt_received', 'Mark Received', 0),
    ('pmt_pending', 'pmt_voided',   'Void',          1),
    ('pmt_received','pmt_refunded', 'Issue Refund',  2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name',  'Customer Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'amount',         'Amount',         'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'payment_date',   'Payment Date',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'payment_method', 'Payment Method', 'enum',   FALSE,
      '["cash","check","credit_card","bank_transfer","other"]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'payment';
END IF;

-- ===== CREDIT MEMO =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'credit_memo') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('credit_memo', 'Credit Memo', 'Credit memos issued against customer invoices.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'cm_draft',   'CM-Draft',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'cm_issued',  'CM-Issued',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'cm_applied', 'CM-Applied', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'cm_void',    'CM-Void',    FALSE, TRUE,  3, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'credit_memo', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='credit_memo' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='credit_memo' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('cm_draft',  'cm_issued',  'Issue Credit Memo', 0),
    ('cm_draft',  'cm_void',    'Void',              1),
    ('cm_issued', 'cm_applied', 'Apply to Invoice',  2),
    ('cm_issued', 'cm_void',    'Void',              3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name', 'Customer Name', 'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'credit_amount', 'Credit Amount', 'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'reason',        'Reason',        'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'credit_memo';
END IF;

-- ===== REFUND =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'refund') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('refund', 'Refund', 'Customer refund requests and processing.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'ref_requested', 'REFUND-Requested', TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'ref_approved',  'REFUND-Approved',  FALSE, FALSE, 1, '#8b5cf6'),
      (v_workflow_id, 'ref_rejected',  'REFUND-Rejected',  FALSE, TRUE,  2, '#ef4444'),
      (v_workflow_id, 'ref_processed', 'REFUND-Processed', FALSE, TRUE,  3, '#22c55e')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'refund', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='refund' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='refund' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('ref_requested', 'ref_approved',  'Approve',  0),
    ('ref_requested', 'ref_rejected',  'Reject',   1),
    ('ref_approved',  'ref_processed', 'Process',  2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'customer_name',  'Customer Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'refund_amount',  'Refund Amount',  'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'reason',         'Reason',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'refund';
END IF;

-- ===== VENDOR =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor', 'Vendor', 'Vendor and supplier directory.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vendor_active',   'VENDOR-Active',   TRUE,  FALSE, 0, '#22c55e'),
      (v_workflow_id, 'vendor_on_hold',  'VENDOR-On Hold',  FALSE, FALSE, 1, '#f59e0b'),
      (v_workflow_id, 'vendor_inactive', 'VENDOR-Inactive', FALSE, TRUE,  2, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vendor_active',  'vendor_on_hold',  'Put On Hold',  0),
    ('vendor_active',  'vendor_inactive', 'Deactivate',   1),
    ('vendor_on_hold', 'vendor_active',   'Reactivate',   2),
    ('vendor_on_hold', 'vendor_inactive', 'Deactivate',   3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'company_name', 'Company Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'email',        'Email',         'email',  FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'phone',        'Phone',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'contact_name', 'Contact Name',  'string', FALSE, '[]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'payment_terms','Payment Terms', 'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor';
END IF;

-- ===== REQUISITION =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'requisition') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('requisition', 'Requisition', 'Internal purchase requests.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'req_draft',     'REQ-Draft',     TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'req_submitted', 'REQ-Submitted', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'req_approved',  'REQ-Approved',  FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'req_rejected',  'REQ-Rejected',  FALSE, TRUE,  3, '#ef4444'),
      (v_workflow_id, 'req_purchased', 'REQ-Purchased', FALSE, TRUE,  4, '#22c55e'),
      (v_workflow_id, 'req_cancelled', 'REQ-Cancelled', FALSE, TRUE,  5, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'requisition', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='requisition' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='requisition' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('req_draft',     'req_submitted', 'Submit',        0),
    ('req_draft',     'req_cancelled', 'Cancel',        1),
    ('req_submitted', 'req_approved',  'Approve',       2),
    ('req_submitted', 'req_rejected',  'Reject',        3),
    ('req_submitted', 'req_cancelled', 'Cancel',        4),
    ('req_approved',  'req_purchased', 'Mark Purchased',5),
    ('req_approved',  'req_cancelled', 'Cancel',        6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'description',    'Description',    'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'requested_by',   'Requested By',   'string', FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'estimated_cost', 'Estimated Cost', 'number', FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'needed_by',      'Needed By Date', 'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'requisition';
END IF;

-- ===== PURCHASE ORDER =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'purchase_order') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('purchase_order', 'Purchase Order', 'Purchase orders sent to vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'po_draft',              'PO-Draft',              TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'po_sent',               'PO-Sent',               FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'po_partially_received', 'PO-Partially Received', FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'po_received',           'PO-Received',           FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'po_cancelled',          'PO-Cancelled',          FALSE, TRUE,  4, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'purchase_order', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='purchase_order' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='purchase_order' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('po_draft',              'po_sent',               'Send to Vendor',     0),
    ('po_draft',              'po_cancelled',          'Cancel',             1),
    ('po_sent',               'po_partially_received', 'Partial Receipt',    2),
    ('po_sent',               'po_received',           'Mark Received',      3),
    ('po_sent',               'po_cancelled',          'Cancel',             4),
    ('po_partially_received', 'po_received',           'Mark Fully Received',5),
    ('po_partially_received', 'po_cancelled',          'Cancel',             6)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',   'Vendor Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'order_date',    'Order Date',    'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'expected_date', 'Expected Date', 'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'total_amount',  'Total Amount',  'number', FALSE, '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'purchase_order';
END IF;

-- ===== ITEM RECEIPT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'item_receipt') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('item_receipt', 'Item Receipt', 'Record goods received against purchase orders.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'ir_pending',     'IR-Pending',     TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'ir_received',    'IR-Received',    FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'ir_reconciled',  'IR-Reconciled',  FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'ir_discrepancy', 'IR-Discrepancy', FALSE, TRUE,  3, '#ef4444')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'item_receipt', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='item_receipt' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='item_receipt' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('ir_pending',  'ir_received',    'Mark Received',  0),
    ('ir_received', 'ir_reconciled',  'Reconcile',      1),
    ('ir_received', 'ir_discrepancy', 'Flag Discrepancy',2)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',   'Vendor Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'received_date', 'Received Date', 'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'notes',         'Notes',         'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'item_receipt';
END IF;

-- ===== VENDOR BILL =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor_bill') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor_bill', 'Vendor Bill', 'Bills received from vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vb_draft',    'VB-Draft',    TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'vb_received', 'VB-Received', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'vb_approved', 'VB-Approved', FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'vb_disputed', 'VB-Disputed', FALSE, FALSE, 3, '#f59e0b'),
      (v_workflow_id, 'vb_paid',     'VB-Paid',     FALSE, TRUE,  4, '#22c55e'),
      (v_workflow_id, 'vb_void',     'VB-Void',     FALSE, TRUE,  5, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor_bill', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_bill' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_bill' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vb_draft',    'vb_received', 'Mark Received', 0),
    ('vb_draft',    'vb_void',     'Void',          1),
    ('vb_received', 'vb_approved', 'Approve',       2),
    ('vb_received', 'vb_disputed', 'Dispute',       3),
    ('vb_approved', 'vb_paid',     'Mark Paid',     4),
    ('vb_approved', 'vb_void',     'Void',          5),
    ('vb_disputed', 'vb_approved', 'Resolve',       6),
    ('vb_disputed', 'vb_void',     'Void',          7)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',  'Vendor Name',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'bill_date',    'Bill Date',    'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'due_date',     'Due Date',     'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'total_amount', 'Total Amount', 'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor_bill';
END IF;

-- ===== VENDOR PAYMENT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor_payment') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor_payment', 'Vendor Payment', 'Payments made to vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vp_pending',   'VP-Pending',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'vp_scheduled', 'VP-Scheduled', FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'vp_sent',      'VP-Sent',      FALSE, FALSE, 2, '#f59e0b'),
      (v_workflow_id, 'vp_cleared',   'VP-Cleared',   FALSE, TRUE,  3, '#22c55e'),
      (v_workflow_id, 'vp_voided',    'VP-Voided',    FALSE, TRUE,  4, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor_payment', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_payment' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_payment' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vp_pending',   'vp_scheduled', 'Schedule',   0),
    ('vp_pending',   'vp_voided',    'Void',       1),
    ('vp_scheduled', 'vp_sent',      'Mark Sent',  2),
    ('vp_scheduled', 'vp_voided',    'Void',       3),
    ('vp_sent',      'vp_cleared',   'Clear',      4),
    ('vp_sent',      'vp_voided',    'Void',       5)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',    'Vendor Name',    'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'amount',         'Amount',         'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'payment_date',   'Payment Date',   'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'payment_method', 'Payment Method', 'enum',   FALSE,
      '["check","bank_transfer","credit_card","other"]'::jsonb, '{}'::jsonb, 3);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor_payment';
END IF;

-- ===== VENDOR CREDIT =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'vendor_credit') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('vendor_credit', 'Vendor Credits', 'Credits received from vendors.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'vc_draft',   'VC-Draft',   TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'vc_issued',  'VC-Issued',  FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'vc_applied', 'VC-Applied', FALSE, TRUE,  2, '#22c55e'),
      (v_workflow_id, 'vc_void',    'VC-Void',    FALSE, TRUE,  3, '#6b7280')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'vendor_credit', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_credit' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='vendor_credit' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('vc_draft',  'vc_issued',  'Issue Credit',    0),
    ('vc_draft',  'vc_void',    'Void',            1),
    ('vc_issued', 'vc_applied', 'Apply to Bill',   2),
    ('vc_issued', 'vc_void',    'Void',            3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'vendor_name',   'Vendor Name',   'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'credit_amount', 'Credit Amount', 'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'reason',        'Reason',        'string', FALSE, '[]'::jsonb, '{}'::jsonb, 2);

  DELETE FROM _wf_states10 WHERE workflow_key = 'vendor_credit';
END IF;

-- ===== EXPENSE =====
IF NOT EXISTS (SELECT 1 FROM workflows WHERE LOWER(key) = 'expense') THEN
  INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
  VALUES ('expense', 'Expenses', 'Employee expense submission and reimbursement.', TRUE, TRUE, 0)
  RETURNING id INTO v_workflow_id;

  WITH s AS (
    INSERT INTO workflow_states (workflow_id, key, name, is_initial, is_terminal, sort_order, color) VALUES
      (v_workflow_id, 'exp_draft',       'EXP-Draft',       TRUE,  FALSE, 0, '#64748b'),
      (v_workflow_id, 'exp_submitted',   'EXP-Submitted',   FALSE, FALSE, 1, '#3b82f6'),
      (v_workflow_id, 'exp_approved',    'EXP-Approved',    FALSE, FALSE, 2, '#8b5cf6'),
      (v_workflow_id, 'exp_rejected',    'EXP-Rejected',    FALSE, TRUE,  3, '#ef4444'),
      (v_workflow_id, 'exp_reimbursed',  'EXP-Reimbursed',  FALSE, TRUE,  4, '#22c55e')
    RETURNING key, id
  )
  INSERT INTO _wf_states10 SELECT 'expense', key, id FROM s;

  INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, guard, sort_order)
  SELECT v_workflow_id,
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='expense' AND state_key=t.fk),
    (SELECT state_id FROM _wf_states10 WHERE workflow_key='expense' AND state_key=t.tk),
    t.name, '{}'::jsonb, t.so
  FROM (VALUES
    ('exp_draft',     'exp_submitted',  'Submit',     0),
    ('exp_submitted', 'exp_approved',   'Approve',    1),
    ('exp_submitted', 'exp_rejected',   'Reject',     2),
    ('exp_approved',  'exp_reimbursed', 'Reimburse',  3)
  ) AS t(fk, tk, name, so);

  INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, options, validation, sort_order) VALUES
    (v_workflow_id, 'submitted_by',  'Submitted By',  'string', TRUE,  '[]'::jsonb, '{}'::jsonb, 0),
    (v_workflow_id, 'amount',        'Amount',        'number', TRUE,  '[]'::jsonb, '{}'::jsonb, 1),
    (v_workflow_id, 'expense_date',  'Expense Date',  'date',   FALSE, '[]'::jsonb, '{}'::jsonb, 2),
    (v_workflow_id, 'category',      'Category',      'enum',   FALSE,
      '["travel","meals","office_supplies","equipment","software","other"]'::jsonb, '{}'::jsonb, 3),
    (v_workflow_id, 'description',   'Description',   'string', FALSE, '[]'::jsonb, '{}'::jsonb, 4);

  DELETE FROM _wf_states10 WHERE workflow_key = 'expense';
END IF;

END $$;
