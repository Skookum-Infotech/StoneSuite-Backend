// Package workflow implements StoneSuite's dynamic, data-defined workflow
// engine: state machines (states + transitions), per-workflow custom field
// definitions (validated, capped at 15 keys), and records that move between
// states with a recorded history. Definitions are edited by super admins;
// the engine loads them and enforces valid transitions and field contracts.
//
// All workflow data lives in the per-tenant database, so every operation runs
// against the tenant pool attached to the request by the tenancy resolver.
package workflow

import "time"

// MaxCustomFields caps how many custom field definitions a workflow may have.
// 15 is the enforced limit; it matches the UI cap and keeps JSONB growth bounded.
const MaxCustomFields = 15

// DataType is the type of a custom field value.
type DataType string

const (
	TypeString DataType = "string"
	TypeNumber DataType = "number"
	TypeDate   DataType = "date" // ISO-8601 date or RFC3339 datetime
	TypeBool   DataType = "bool"
	TypeEnum   DataType = "enum"
	TypeEmail  DataType = "email"
)

// ActionType identifies a transition action (executed in Phase 4).
type ActionType string

const (
	ActionSendEmail    ActionType = "send_email"
	ActionAssignOwner  ActionType = "assign_owner"
	ActionSetField     ActionType = "set_field"
	ActionWebhook      ActionType = "webhook"
	ActionCreateRecord ActionType = "create_record"
)

// Workflow is a state machine definition.
type Workflow struct {
	ID            string   `json:"id"`
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Enabled       bool     `json:"enabled"`
	IsDefault     bool     `json:"isDefault"`
	PipelineOrder int      `json:"pipelineOrder"` // 0 = unordered; 1=Lead,2=Prospect,3=Customer
	// ApproverUserIds is populated by the controller layer (not this package —
	// approver config lives in crmstore) for CRM workflows only. Left nil for
	// non-CRM workflows and by callers that don't need it (e.g. ListWorkflows).
	ApproverUserIds []string `json:"approverUserIds,omitempty"`
}

// State is a node in a workflow.
type State struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflowId"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	IsInitial  bool   `json:"isInitial"`
	IsTerminal bool   `json:"isTerminal"`
	SortOrder  int    `json:"sortOrder"`
	Color      string `json:"color"`
}

// Guard holds transition pre-conditions (currently: required field keys).
type Guard struct {
	RequiredFields []string `json:"requiredFields,omitempty"`
}

// Transition is a directed edge between two states.
type Transition struct {
	ID                 string `json:"id"`
	WorkflowID         string `json:"workflowId"`
	FromStateID        string `json:"fromStateId"`
	ToStateID          string `json:"toStateId"`
	Name               string `json:"name"`
	RequiredPermission string `json:"requiredPermission"` // "resource:action" (optional)
	Guard              Guard  `json:"guard"`
	SortOrder          int    `json:"sortOrder"`
}

// FieldDefinition is the contract for one custom field on a workflow.
type FieldDefinition struct {
	ID         string          `json:"id"`
	WorkflowID string          `json:"workflowId"`
	Key        string          `json:"key"`
	Label      string          `json:"label"`
	DataType   DataType        `json:"dataType"`
	Required   bool            `json:"required"`
	Options    []string        `json:"options"`    // enum members
	Validation FieldValidation `json:"validation"` // optional constraints
	SortOrder  int             `json:"sortOrder"`
}

// FieldValidation holds optional per-field constraints.
type FieldValidation struct {
	Regex string   `json:"regex,omitempty"` // string/email
	Min   *float64 `json:"min,omitempty"`   // number
	Max   *float64 `json:"max,omitempty"`   // number
}

// Definition is a fully loaded workflow (states, transitions, field defs).
type Definition struct {
	Workflow    Workflow          `json:"workflow"`
	States      []State           `json:"states"`
	Transitions []Transition      `json:"transitions"`
	Fields      []FieldDefinition `json:"fields"`
}

// Record is a workflow instance (a lead/prospect/customer/...).
type Record struct {
	ID             string         `json:"id"`
	WorkflowID     string         `json:"workflowId"`
	RecordNumber   string         `json:"recordNumber,omitempty"`
	CurrentStateID string         `json:"currentStateId"`
	OwnerUserID    string         `json:"ownerUserId,omitempty"`
	TeamID         string         `json:"teamId,omitempty"`
	ParentRecordID string         `json:"parentRecordId,omitempty"`
	CoreFields     map[string]any `json:"coreFields"`
	CustomFields   map[string]any `json:"customFields"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// StatusInfo represents a workflow state as a selectable status option.
type StatusInfo struct {
	StateID      string `json:"stateId"`
	StateKey     string `json:"stateKey"`
	StatusLabel  string `json:"statusLabel"` // e.g. "LEAD-New"
	WorkflowKey  string `json:"workflowKey"`
	WorkflowName string `json:"workflowName"`
	IsInitial    bool   `json:"isInitial"`
	IsTerminal   bool   `json:"isTerminal"`
	SortOrder    int    `json:"sortOrder"`
	Color        string `json:"color"`
}

// initialState returns the workflow's initial state, or false if none is set.
func (d *Definition) initialState() (State, bool) {
	for _, s := range d.States {
		if s.IsInitial {
			return s, true
		}
	}
	return State{}, false
}

// stateByID looks up a state in the definition.
func (d *Definition) stateByID(id string) (State, bool) {
	for _, s := range d.States {
		if s.ID == id {
			return s, true
		}
	}
	return State{}, false
}

// transition finds the edge from one state to another, if it exists.
func (d *Definition) transition(fromStateID, toStateID string) (Transition, bool) {
	for _, t := range d.Transitions {
		if t.FromStateID == fromStateID && t.ToStateID == toStateID {
			return t, true
		}
	}
	return Transition{}, false
}
