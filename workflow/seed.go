package workflow

import (
	"context"
	"encoding/json"
	"fmt"
)

// seed* types describe default workflows declaratively so seeding is data-driven.
type seedState struct {
	key, name         string
	initial, terminal bool
	order             int
	color             string
}

type seedTransition struct {
	from, to       string // state keys
	name           string
	requiredFields []string
}

type seedField struct {
	key, label string
	dataType   DataType
	required   bool
	options    []string
}

type seedWorkflow struct {
	key, name, description string
	pipelineOrder          int // 0=unordered; 1=Lead, 2=Prospect, 3=Customer
	states                 []seedState
	transitions            []seedTransition
	fields                 []seedField
}

// defaultWorkflows are the Lead/Prospect/Customer CRM pipelines seeded for
// every new tenant. pipeline_order encodes the CRM dependency chain used to
// enforce disable rules (upstream must be disabled before downstream).
var defaultWorkflows = []seedWorkflow{
	{
		key: "lead", name: "Lead", description: "Inbound leads pipeline.",
		pipelineOrder: 1,
		states: []seedState{
			{key: "lead_new", name: "LEAD-New", initial: true, order: 0, color: "#64748b"},
			{key: "lead_in_progress", name: "LEAD-In Progress", order: 1, color: "#3b82f6"},
			{key: "lead_qualified", name: "LEAD-Qualified", order: 2, color: "#8b5cf6"},
			{key: "lead_unqualified", name: "LEAD-UnQualified", terminal: true, order: 3, color: "#ef4444"},
			{key: "lead_converted", name: "LEAD-Converted", terminal: true, order: 4, color: "#22c55e"},
			{key: "lead_dead", name: "LEAD-Dead", terminal: true, order: 5, color: "#6b7280"},
		},
		transitions: []seedTransition{
			{from: "lead_new", to: "lead_in_progress", name: "Start Progress"},
			{from: "lead_new", to: "lead_unqualified", name: "Disqualify"},
			{from: "lead_in_progress", to: "lead_qualified", name: "Qualify"},
			{from: "lead_in_progress", to: "lead_unqualified", name: "Disqualify"},
			{from: "lead_in_progress", to: "lead_dead", name: "Mark Dead"},
			{from: "lead_qualified", to: "lead_converted", name: "Convert"},
			{from: "lead_qualified", to: "lead_dead", name: "Mark Dead"},
		},
		fields: []seedField{
			{key: "company_name", label: "Company Name", dataType: TypeString, required: true},
			{key: "email", label: "Email", dataType: TypeEmail, required: true},
			{key: "phone", label: "Phone", dataType: TypeString},
			{key: "source", label: "Source", dataType: TypeEnum,
				options: []string{"web", "referral", "event", "cold_call", "partner"}},
			{key: "estimated_value", label: "Estimated Value", dataType: TypeNumber},
		},
	},
	{
		key: "prospect", name: "Prospect", description: "Active sales opportunities.",
		pipelineOrder: 2,
		states: []seedState{
			{key: "prospect_in_discussion", name: "PROSPECT-In Discussion", initial: true, order: 0, color: "#64748b"},
			{key: "prospect_identified_dms", name: "PROSPECT-Identified Decision Makers", order: 1, color: "#3b82f6"},
			{key: "prospect_qualified", name: "PROSPECT-Qualified", order: 2, color: "#8b5cf6"},
			{key: "prospect_proposal", name: "PROSPECT-Proposal", order: 3, color: "#f59e0b"},
			{key: "prospect_in_negotiation", name: "PROSPECT-In Negotiation", order: 4, color: "#f97316"},
			{key: "prospect_purchasing", name: "PROSPECT-Purchasing", order: 5, color: "#a855f7"},
			{key: "prospect_closed_lost", name: "PROSPECT-Closed Lost", terminal: true, order: 6, color: "#ef4444"},
		},
		transitions: []seedTransition{
			{from: "prospect_in_discussion", to: "prospect_identified_dms", name: "Identify Decision Makers"},
			{from: "prospect_in_discussion", to: "prospect_closed_lost", name: "Close Lost"},
			{from: "prospect_identified_dms", to: "prospect_qualified", name: "Qualify"},
			{from: "prospect_identified_dms", to: "prospect_closed_lost", name: "Close Lost"},
			{from: "prospect_qualified", to: "prospect_proposal", name: "Send Proposal"},
			{from: "prospect_qualified", to: "prospect_closed_lost", name: "Close Lost"},
			{from: "prospect_proposal", to: "prospect_in_negotiation", name: "Begin Negotiation"},
			{from: "prospect_proposal", to: "prospect_closed_lost", name: "Close Lost"},
			{from: "prospect_in_negotiation", to: "prospect_purchasing", name: "Move to Purchase"},
			{from: "prospect_in_negotiation", to: "prospect_closed_lost", name: "Close Lost"},
		},
		fields: []seedField{
			{key: "company_name", label: "Company Name", dataType: TypeString, required: true},
			{key: "email", label: "Email", dataType: TypeEmail, required: true},
			{key: "phone", label: "Phone", dataType: TypeString},
			{key: "deal_size", label: "Deal Size", dataType: TypeNumber},
			{key: "close_date", label: "Expected Close Date", dataType: TypeDate},
		},
	},
	{
		key: "customer", name: "Customer", description: "Customer lifecycle.",
		pipelineOrder: 3,
		states: []seedState{
			{key: "customer_closed_won", name: "CUSTOMER-Closed Won", initial: true, order: 0, color: "#22c55e"},
			{key: "customer_renewal", name: "CUSTOMER-Renewal", order: 1, color: "#3b82f6"},
			{key: "customer_closed_lost", name: "CUSTOMER-Closed Lost", terminal: true, order: 2, color: "#ef4444"},
		},
		transitions: []seedTransition{
			{from: "customer_closed_won", to: "customer_renewal", name: "Up for Renewal"},
			{from: "customer_closed_won", to: "customer_closed_lost", name: "Mark Lost"},
			{from: "customer_renewal", to: "customer_closed_won", name: "Renew"},
			{from: "customer_renewal", to: "customer_closed_lost", name: "Mark Lost"},
		},
		// Core customer fields (mirror onboarding form).
		fields: []seedField{
			{key: "company_name", label: "Company Name", dataType: TypeString, required: true},
			{key: "legal_name", label: "Legal Name", dataType: TypeString},
			{key: "industry", label: "Industry", dataType: TypeString},
			{key: "website", label: "Website", dataType: TypeString},
			{key: "country", label: "Country", dataType: TypeString},
			{key: "currency", label: "Currency", dataType: TypeString},
			{key: "timezone", label: "Timezone", dataType: TypeString},
			{key: "tax_id", label: "Tax / VAT ID", dataType: TypeString},
			{key: "billing_address", label: "Billing Address", dataType: TypeString},
			{key: "shipping_address", label: "Shipping Address", dataType: TypeString},
			{key: "super_admin_name", label: "Super Admin Name", dataType: TypeString},
			{key: "super_admin_email", label: "Super Admin Email", dataType: TypeEmail, required: true},
			{key: "super_admin_phone", label: "Super Admin Phone", dataType: TypeString},
		},
	},
}

// SeedDefaultWorkflows installs the default workflows into a freshly provisioned
// tenant database. Idempotent: a workflow whose key already exists is skipped.
func SeedDefaultWorkflows(ctx context.Context, q Querier) error {
	for _, sw := range defaultWorkflows {
		if err := seedOne(ctx, q, sw); err != nil {
			return fmt.Errorf("seed workflow %q: %w", sw.key, err)
		}
	}
	return nil
}

func seedOne(ctx context.Context, q Querier, sw seedWorkflow) error {
	// Skip if already present (idempotent re-seed).
	var existing string
	err := q.QueryRow(ctx, `SELECT id FROM workflows WHERE LOWER(key) = LOWER($1)`, sw.key).Scan(&existing)
	if err == nil {
		return nil
	}

	var workflowID string
	if err := q.QueryRow(ctx, `
		INSERT INTO workflows (key, name, description, enabled, is_default, pipeline_order)
		VALUES ($1, $2, $3, TRUE, TRUE, $4) RETURNING id`,
		sw.key, sw.name, sw.description, sw.pipelineOrder).Scan(&workflowID); err != nil {
		return fmt.Errorf("insert workflow: %w", err)
	}

	stateID := map[string]string{}
	for _, s := range sw.states {
		var id string
		if err := q.QueryRow(ctx, `
			INSERT INTO workflow_states
				(workflow_id, key, name, is_initial, is_terminal, sort_order, color)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			workflowID, s.key, s.name, s.initial, s.terminal, s.order, s.color).Scan(&id); err != nil {
			return fmt.Errorf("insert state %q: %w", s.key, err)
		}
		stateID[s.key] = id
	}

	for i, t := range sw.transitions {
		guard := Guard{RequiredFields: t.requiredFields}
		guardRaw, _ := json.Marshal(guard)
		if _, err := q.Exec(ctx, `
			INSERT INTO workflow_transitions
				(workflow_id, from_state_id, to_state_id, name, guard, sort_order)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6)`,
			workflowID, stateID[t.from], stateID[t.to], t.name, guardRaw, i); err != nil {
			return fmt.Errorf("insert transition %s->%s: %w", t.from, t.to, err)
		}
	}

	for i, f := range sw.fields {
		opts, _ := json.Marshal(f.options)
		if _, err := q.Exec(ctx, `
			INSERT INTO workflow_field_definitions
				(workflow_id, key, label, data_type, required, options, validation, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6::jsonb,'{}'::jsonb,$7)`,
			workflowID, f.key, f.label, f.dataType, f.required, opts, i); err != nil {
			return fmt.Errorf("insert field %q: %w", f.key, err)
		}
	}
	return nil
}
