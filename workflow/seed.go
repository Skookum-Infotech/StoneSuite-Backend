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
	states                 []seedState
	transitions            []seedTransition
	fields                 []seedField
}

// defaultWorkflows are the Lead/Prospect/Customer pipelines every new tenant
// gets. They are ordinary rows: a tenant can disable them or add custom fields.
var defaultWorkflows = []seedWorkflow{
	{
		key: "lead", name: "Lead", description: "Inbound leads pipeline.",
		states: []seedState{
			{key: "new", name: "New", initial: true, order: 0, color: "#64748b"},
			{key: "contacted", name: "Contacted", order: 1, color: "#3b82f6"},
			{key: "qualified", name: "Qualified", order: 2, color: "#8b5cf6"},
			{key: "converted", name: "Converted", terminal: true, order: 3, color: "#22c55e"},
			{key: "disqualified", name: "Disqualified", terminal: true, order: 4, color: "#ef4444"},
		},
		transitions: []seedTransition{
			{from: "new", to: "contacted", name: "Contact"},
			{from: "contacted", to: "qualified", name: "Qualify", requiredFields: []string{"phone"}},
			{from: "qualified", to: "converted", name: "Convert"},
			{from: "new", to: "disqualified", name: "Disqualify"},
			{from: "contacted", to: "disqualified", name: "Disqualify"},
		},
		fields: []seedField{
			{key: "source", label: "Source", dataType: TypeEnum, options: []string{"web", "referral", "event", "cold"}},
			{key: "phone", label: "Phone", dataType: TypeString},
			{key: "estimated_value", label: "Estimated Value", dataType: TypeNumber},
		},
	},
	{
		key: "prospect", name: "Prospect", description: "Active sales opportunities.",
		states: []seedState{
			{key: "new", name: "New", initial: true, order: 0, color: "#64748b"},
			{key: "proposal_sent", name: "Proposal Sent", order: 1, color: "#3b82f6"},
			{key: "negotiation", name: "Negotiation", order: 2, color: "#f59e0b"},
			{key: "won", name: "Won", terminal: true, order: 3, color: "#22c55e"},
			{key: "lost", name: "Lost", terminal: true, order: 4, color: "#ef4444"},
		},
		transitions: []seedTransition{
			{from: "new", to: "proposal_sent", name: "Send Proposal"},
			{from: "proposal_sent", to: "negotiation", name: "Begin Negotiation"},
			{from: "negotiation", to: "won", name: "Mark Won"},
			{from: "proposal_sent", to: "lost", name: "Mark Lost"},
			{from: "negotiation", to: "lost", name: "Mark Lost"},
		},
		fields: []seedField{
			{key: "deal_size", label: "Deal Size", dataType: TypeNumber},
			{key: "close_date", label: "Expected Close Date", dataType: TypeDate},
		},
	},
	{
		key: "customer", name: "Customer", description: "Customer lifecycle.",
		states: []seedState{
			{key: "onboarding", name: "Onboarding", initial: true, order: 0, color: "#3b82f6"},
			{key: "active", name: "Active", order: 1, color: "#22c55e"},
			{key: "churned", name: "Churned", terminal: true, order: 2, color: "#ef4444"},
		},
		transitions: []seedTransition{
			{from: "onboarding", to: "active", name: "Activate"},
			{from: "active", to: "churned", name: "Mark Churned"},
		},
		fields: []seedField{
			{key: "account_email", label: "Account Email", dataType: TypeEmail, required: true},
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
		INSERT INTO workflows (key, name, description, enabled, is_default)
		VALUES ($1, $2, $3, TRUE, TRUE) RETURNING id`,
		sw.key, sw.name, sw.description).Scan(&workflowID); err != nil {
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
