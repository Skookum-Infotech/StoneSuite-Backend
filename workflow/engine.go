package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Engine runs workflow operations against a loaded Definition. It is stateless;
// definitions and records are passed in, so its decision logic is unit-testable
// without a database.
type Engine struct{}

// NewEngine constructs an Engine.
func NewEngine() *Engine { return &Engine{} }

// TransitionError signals an invalid or disallowed transition (caller error).
type TransitionError struct{ Reason string }

func (e TransitionError) Error() string { return e.Reason }

// ValidateTransition checks that moving rec to toStateID is legal for def:
// the target state exists, an edge from the current state to it exists, and the
// transition's guard (required fields) is satisfied. Pure function.
func (e *Engine) ValidateTransition(def *Definition, rec *Record, toStateID string) (Transition, error) {
	if _, ok := def.stateByID(toStateID); !ok {
		return Transition{}, TransitionError{Reason: "target state does not belong to this workflow"}
	}
	if rec.CurrentStateID == "" {
		return Transition{}, TransitionError{Reason: "record has no current state"}
	}
	if rec.CurrentStateID == toStateID {
		return Transition{}, TransitionError{Reason: "record is already in that state"}
	}
	t, ok := def.transition(rec.CurrentStateID, toStateID)
	if !ok {
		return Transition{}, TransitionError{Reason: "no transition exists between these states"}
	}
	if missing := unmetRequiredFields(t.Guard, rec); len(missing) > 0 {
		return Transition{}, TransitionError{
			Reason: fmt.Sprintf("transition blocked: required fields not set: %v", missing),
		}
	}
	return t, nil
}

// unmetRequiredFields returns guard-required field keys that are absent/empty
// in the record (checked across both core and custom fields).
func unmetRequiredFields(g Guard, rec *Record) []string {
	var missing []string
	for _, key := range g.RequiredFields {
		if v, ok := rec.CustomFields[key]; ok && !isEmpty(v) {
			continue
		}
		if v, ok := rec.CoreFields[key]; ok && !isEmpty(v) {
			continue
		}
		missing = append(missing, key)
	}
	return missing
}

// CreateRecord validates custom fields, places the record in the workflow's
// initial state, and writes a creation history row — atomically.
func (e *Engine) CreateRecord(ctx context.Context, pool Beginner, def *Definition,
	ownerUserID, teamID string, core, custom map[string]any) (*Record, error) {
	return e.createRecord(ctx, pool, def, ownerUserID, teamID, "", core, custom, false)
}

// ConvertRecord creates a new record in def starting at its initial state,
// linked to parentRecordID as its lineage source (e.g. lead → prospect).
// Required-field validation is skipped so the target record can be created with
// only the fields the source provides; the user fills in the rest via edit.
func (e *Engine) ConvertRecord(ctx context.Context, pool Beginner, def *Definition,
	ownerUserID, teamID, parentRecordID string, core, custom map[string]any) (*Record, error) {
	return e.createRecord(ctx, pool, def, ownerUserID, teamID, parentRecordID, core, custom, true)
}

// createRecord is the shared implementation behind CreateRecord and ConvertRecord.
// skipRequired suppresses required-field enforcement (used for conversions).
func (e *Engine) createRecord(ctx context.Context, pool Beginner, def *Definition,
	ownerUserID, teamID, parentRecordID string, core, custom map[string]any, skipRequired bool) (*Record, error) {

	if !def.Workflow.Enabled {
		return nil, TransitionError{Reason: "workflow is disabled"}
	}
	if custom == nil {
		custom = map[string]any{}
	}
	if core == nil {
		core = map[string]any{}
	}
	var validateErr error
	if skipRequired {
		validateErr = ValidateCustomFieldsPartial(def.Fields, custom)
	} else {
		validateErr = ValidateCustomFields(def.Fields, custom)
	}
	if validateErr != nil {
		return nil, validateErr
	}
	init, ok := def.initialState()
	if !ok {
		return nil, errors.New("workflow has no initial state")
	}

	coreRaw, _ := json.Marshal(core)
	customRaw, _ := json.Marshal(custom)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create record: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recordNumber, err := generateRecordNumber(ctx, tx, def.Workflow.ID)
	if err != nil {
		return nil, err
	}

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workflow_records
			(workflow_id, record_number, current_state_id, owner_user_id, team_id, parent_record_id, core_fields, custom_fields)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb) RETURNING id`,
		def.Workflow.ID, nullIfEmpty(recordNumber), init.ID, nullIfEmpty(ownerUserID), nullIfEmpty(teamID),
		nullIfEmpty(parentRecordID), coreRaw, customRaw).Scan(&id); err != nil {
		return nil, fmt.Errorf("insert record: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workflow_record_history
			(record_id, from_state_id, to_state_id, actor_user_id, snapshot)
		VALUES ($1, NULL, $2, $3, $4::jsonb)`,
		id, init.ID, nullIfEmpty(ownerUserID), customRaw); err != nil {
		return nil, fmt.Errorf("insert create history: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create record: %w", err)
	}
	return GetRecord(ctx, pool, id)
}

// Apply validates and performs a transition: moves the record to the target
// state and records history — atomically. Transition actions (Phase 4) will be
// executed at the marked extension point.
func (e *Engine) Apply(ctx context.Context, pool Beginner, def *Definition,
	rec *Record, toStateID, actorUserID string) (*Record, error) {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the record row for the rest of this transaction so concurrent
	// transitions on the same record serialize instead of racing on the
	// current_state_id snapshot taken before this call (ADR-2).
	var currentStateID string
	if err := tx.QueryRow(ctx,
		`SELECT current_state_id FROM workflow_records WHERE id = $1 FOR UPDATE`,
		rec.ID).Scan(&currentStateID); err != nil {
		return nil, fmt.Errorf("lock record: %w", err)
	}

	// Re-validate against the authoritative state: it may have changed since
	// rec was loaded if another transition committed in the meantime.
	fresh := *rec
	fresh.CurrentStateID = currentStateID
	t, err := e.ValidateTransition(def, &fresh, toStateID)
	if err != nil {
		return nil, err
	}
	snapshot, _ := json.Marshal(rec.CustomFields)

	if _, err := tx.Exec(ctx,
		`UPDATE workflow_records SET current_state_id = $2, updated_at = NOW() WHERE id = $1`,
		rec.ID, toStateID); err != nil {
		return nil, fmt.Errorf("update record state: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workflow_record_history
			(record_id, from_state_id, to_state_id, actor_user_id, transition_id, snapshot)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)`,
		rec.ID, currentStateID, toStateID, nullIfEmpty(actorUserID), t.ID, snapshot); err != nil {
		return nil, fmt.Errorf("insert transition history: %w", err)
	}

	// --- Phase 4 extension point ---------------------------------------------
	// Transition actions (send_email/assign_owner/set_field/webhook/...) will be
	// loaded from workflow_transition_actions and executed here (in/after the tx
	// as appropriate). Intentionally not run in Phase 3.

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return GetRecord(ctx, pool, rec.ID)
}
