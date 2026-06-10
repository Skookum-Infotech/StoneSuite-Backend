package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of pgx behavior reads/writes need (consumer-side
// interface). A *pgxpool.Pool or a pgx.Tx both satisfy it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Beginner is a Querier that can also start a transaction (e.g. *pgxpool.Pool).
type Beginner interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Sentinel errors.
var (
	ErrWorkflowNotFound   = errors.New("workflow not found")
	ErrRecordNotFound     = errors.New("record not found")
	ErrFieldCap           = fmt.Errorf("a workflow may have at most %d custom fields", MaxCustomFields)
	ErrDisableDependency  = errors.New("workflow has upstream dependency")
)

// ----- workflows -------------------------------------------------------------

// ListWorkflows returns all workflows (config view).
func ListWorkflows(ctx context.Context, q Querier) ([]Workflow, error) {
	rows, err := q.Query(ctx, `
		SELECT id, key, name, description, enabled, is_default, pipeline_order
		FROM workflows ORDER BY pipeline_order, is_default DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	defer rows.Close()
	var out []Workflow
	for rows.Next() {
		var w Workflow
		if err := rows.Scan(&w.ID, &w.Key, &w.Name, &w.Description, &w.Enabled, &w.IsDefault, &w.PipelineOrder); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWorkflowByKey loads a workflow by its key (case-insensitive).
func GetWorkflowByKey(ctx context.Context, q Querier, key string) (*Workflow, error) {
	var w Workflow
	err := q.QueryRow(ctx, `
		SELECT id, key, name, description, enabled, is_default, pipeline_order
		FROM workflows WHERE LOWER(key) = LOWER($1)`, key).
		Scan(&w.ID, &w.Key, &w.Name, &w.Description, &w.Enabled, &w.IsDefault, &w.PipelineOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workflow by key: %w", err)
	}
	return &w, nil
}

// SetWorkflowEnabled toggles a workflow on/off.
// When disabling, enforces the CRM pipeline dependency: a workflow with
// pipeline_order N cannot be disabled while any workflow with pipeline_order
// < N (and > 0) is still enabled — the upstream must be disabled first.
func SetWorkflowEnabled(ctx context.Context, q Querier, id string, enabled bool) error {
	if !enabled {
		if err := checkDisableDependency(ctx, q, id); err != nil {
			return err
		}
	}
	tag, err := q.Exec(ctx,
		`UPDATE workflows SET enabled = $2, updated_at = NOW() WHERE id = $1`, id, enabled)
	if err != nil {
		return fmt.Errorf("set workflow enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkflowNotFound
	}
	return nil
}

// checkDisableDependency returns ErrDisableDependency (wrapping a descriptive
// message) when disabling workflow `id` would break the CRM pipeline chain.
// Only applies to workflows with pipeline_order > 1.
func checkDisableDependency(ctx context.Context, q Querier, id string) error {
	var thisOrder int
	var thisName string
	err := q.QueryRow(ctx,
		`SELECT pipeline_order, name FROM workflows WHERE id = $1`, id).
		Scan(&thisOrder, &thisName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWorkflowNotFound
	}
	if err != nil {
		return fmt.Errorf("check dependency: %w", err)
	}
	// Non-CRM workflows (order 0) have no dependency constraints.
	if thisOrder <= 1 {
		return nil
	}
	// Look for any enabled workflow upstream in the chain.
	rows, err := q.Query(ctx, `
		SELECT name FROM workflows
		WHERE pipeline_order > 0 AND pipeline_order < $1 AND enabled = TRUE`, thisOrder)
	if err != nil {
		return fmt.Errorf("query upstream workflows: %w", err)
	}
	defer rows.Close()
	var upstreams []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan upstream: %w", err)
		}
		upstreams = append(upstreams, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(upstreams) > 0 {
		return fmt.Errorf("%w: cannot disable %q while %v is still enabled — disable the upstream workflow(s) first",
			ErrDisableDependency, thisName, upstreams)
	}
	return nil
}

// LoadDefinition loads a full workflow definition by id (workflow + states +
// transitions + field definitions).
func LoadDefinition(ctx context.Context, q Querier, workflowID string) (*Definition, error) {
	var d Definition
	err := q.QueryRow(ctx, `
		SELECT id, key, name, description, enabled, is_default, pipeline_order
		FROM workflows WHERE id = $1`, workflowID).
		Scan(&d.Workflow.ID, &d.Workflow.Key, &d.Workflow.Name, &d.Workflow.Description,
			&d.Workflow.Enabled, &d.Workflow.IsDefault, &d.Workflow.PipelineOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}

	if d.States, err = loadStates(ctx, q, workflowID); err != nil {
		return nil, err
	}
	if d.Transitions, err = loadTransitions(ctx, q, workflowID); err != nil {
		return nil, err
	}
	if d.Fields, err = ListFields(ctx, q, workflowID); err != nil {
		return nil, err
	}
	// Coalesce nil slices to empty so the JSON always has arrays, not null.
	if d.States == nil {
		d.States = []State{}
	}
	if d.Transitions == nil {
		d.Transitions = []Transition{}
	}
	if d.Fields == nil {
		d.Fields = []FieldDefinition{}
	}
	return &d, nil
}

func loadStates(ctx context.Context, q Querier, workflowID string) ([]State, error) {
	rows, err := q.Query(ctx, `
		SELECT id, workflow_id, key, name, is_initial, is_terminal, sort_order, color
		FROM workflow_states WHERE workflow_id = $1 ORDER BY sort_order, name`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("load states: %w", err)
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var s State
		if err := rows.Scan(&s.ID, &s.WorkflowID, &s.Key, &s.Name, &s.IsInitial,
			&s.IsTerminal, &s.SortOrder, &s.Color); err != nil {
			return nil, fmt.Errorf("scan state: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadTransitions(ctx context.Context, q Querier, workflowID string) ([]Transition, error) {
	rows, err := q.Query(ctx, `
		SELECT id, workflow_id, from_state_id, to_state_id, name,
		       required_permission, guard, sort_order
		FROM workflow_transitions WHERE workflow_id = $1 ORDER BY sort_order, name`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("load transitions: %w", err)
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		var t Transition
		var guardRaw []byte
		if err := rows.Scan(&t.ID, &t.WorkflowID, &t.FromStateID, &t.ToStateID, &t.Name,
			&t.RequiredPermission, &guardRaw, &t.SortOrder); err != nil {
			return nil, fmt.Errorf("scan transition: %w", err)
		}
		if len(guardRaw) > 0 {
			if err := json.Unmarshal(guardRaw, &t.Guard); err != nil {
				return nil, fmt.Errorf("decode guard: %w", err)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ----- field definitions -----------------------------------------------------

// ListFields returns a workflow's custom field definitions in display order.
func ListFields(ctx context.Context, q Querier, workflowID string) ([]FieldDefinition, error) {
	rows, err := q.Query(ctx, `
		SELECT id, workflow_id, key, label, data_type, required, options, validation, sort_order
		FROM workflow_field_definitions WHERE workflow_id = $1 ORDER BY sort_order, key`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("list fields: %w", err)
	}
	defer rows.Close()
	var out []FieldDefinition
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanField(row pgx.Row) (FieldDefinition, error) {
	var f FieldDefinition
	var optsRaw, valRaw []byte
	if err := row.Scan(&f.ID, &f.WorkflowID, &f.Key, &f.Label, &f.DataType,
		&f.Required, &optsRaw, &valRaw, &f.SortOrder); err != nil {
		return f, fmt.Errorf("scan field: %w", err)
	}
	if len(optsRaw) > 0 {
		if err := json.Unmarshal(optsRaw, &f.Options); err != nil {
			return f, fmt.Errorf("decode options: %w", err)
		}
	}
	if len(valRaw) > 0 {
		if err := json.Unmarshal(valRaw, &f.Validation); err != nil {
			return f, fmt.Errorf("decode validation: %w", err)
		}
	}
	// Ensure options is never nil so it serializes as [] (not null) for clients.
	if f.Options == nil {
		f.Options = []string{}
	}
	return f, nil
}

// CreateField adds a custom field definition, enforcing the per-workflow cap.
func CreateField(ctx context.Context, q Querier, f FieldDefinition) (string, error) {
	if err := ValidateFieldDefinition(f); err != nil {
		return "", err
	}
	var count int
	if err := q.QueryRow(ctx,
		`SELECT COUNT(*) FROM workflow_field_definitions WHERE workflow_id = $1`, f.WorkflowID).
		Scan(&count); err != nil {
		return "", fmt.Errorf("count fields: %w", err)
	}
	if count >= MaxCustomFields {
		return "", ErrFieldCap
	}
	opts, _ := json.Marshal(f.Options)
	val, _ := json.Marshal(f.Validation)
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO workflow_field_definitions
			(workflow_id, key, label, data_type, required, options, validation, sort_order)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8) RETURNING id`,
		f.WorkflowID, f.Key, f.Label, f.DataType, f.Required, opts, val, f.SortOrder).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create field (key may be taken): %w", err)
	}
	return id, nil
}

// DeleteField removes a custom field definition.
func DeleteField(ctx context.Context, q Querier, workflowID, fieldID string) error {
	tag, err := q.Exec(ctx,
		`DELETE FROM workflow_field_definitions WHERE id = $1 AND workflow_id = $2`, fieldID, workflowID)
	if err != nil {
		return fmt.Errorf("delete field: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("field not found")
	}
	return nil
}

// ----- records ---------------------------------------------------------------

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	var owner, team, parent *string
	var coreRaw, customRaw []byte
	err := row.Scan(&r.ID, &r.WorkflowID, &r.RecordNumber, &r.CurrentStateID, &owner, &team, &parent,
		&coreRaw, &customRaw, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan record: %w", err)
	}
	if owner != nil {
		r.OwnerUserID = *owner
	}
	if team != nil {
		r.TeamID = *team
	}
	if parent != nil {
		r.ParentRecordID = *parent
	}
	r.CoreFields = map[string]any{}
	r.CustomFields = map[string]any{}
	if len(coreRaw) > 0 {
		_ = json.Unmarshal(coreRaw, &r.CoreFields)
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &r.CustomFields)
	}
	return &r, nil
}

const recordColumns = `id, workflow_id, COALESCE(record_number, ''),
	COALESCE(current_state_id::text, ''), owner_user_id, team_id, parent_record_id,
	core_fields, custom_fields, created_at, updated_at`

// GetRecord loads a single record by id.
func GetRecord(ctx context.Context, q Querier, id string) (*Record, error) {
	return scanRecord(q.QueryRow(ctx, `SELECT `+recordColumns+` FROM workflow_records WHERE id = $1`, id))
}

// ListRecords returns records for a workflow, filtered by the caller's scope.
//   - "all":  every record in the workflow
//   - "team": records the caller owns OR assigned to one of the caller's teams
//   - "own":  records the caller owns
func ListRecords(ctx context.Context, q Querier, workflowID, scope, callerUserID string, teamIDs []string) ([]Record, error) {
	var (
		rows pgx.Rows
		err  error
		base = `SELECT ` + recordColumns + ` FROM workflow_records WHERE workflow_id = $1`
	)
	switch scope {
	case "all":
		rows, err = q.Query(ctx, base+` ORDER BY created_at DESC`, workflowID)
	case "team":
		rows, err = q.Query(ctx, base+`
			AND (owner_user_id = $2 OR team_id = ANY($3)) ORDER BY created_at DESC`,
			workflowID, nullIfEmpty(callerUserID), teamIDs)
	default: // own (most restrictive)
		rows, err = q.Query(ctx, base+`
			AND owner_user_id = $2 ORDER BY created_at DESC`, workflowID, nullIfEmpty(callerUserID))
	}
	if err != nil {
		return nil, fmt.Errorf("list records: %w", err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpdateRecordFields replaces a record's custom_fields (already validated).
func UpdateRecordFields(ctx context.Context, q Querier, id string, custom map[string]any) error {
	raw, _ := json.Marshal(custom)
	tag, err := q.Exec(ctx,
		`UPDATE workflow_records SET custom_fields = $2::jsonb, updated_at = NOW() WHERE id = $1`,
		id, raw)
	if err != nil {
		return fmt.Errorf("update record fields: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRecordNotFound
	}
	return nil
}

// ----- identity / team helpers ----------------------------------------------

// UserIDByIdentity maps a control-plane identity id to the tenant-local user id.
func UserIDByIdentity(ctx context.Context, q Querier, identityID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM users WHERE identity_id = $1`, identityID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("no tenant user for identity")
	}
	if err != nil {
		return "", fmt.Errorf("user by identity: %w", err)
	}
	return id, nil
}

// TeamIDsForUser lists the team ids a tenant user belongs to.
func TeamIDsForUser(ctx context.Context, q Querier, userID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT team_id FROM team_members WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("teams for user: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan team id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpdateRecordAllFields replaces both core_fields and custom_fields on a record.
// Pass nil for either map to leave it unchanged.
func UpdateRecordAllFields(ctx context.Context, q Querier, id string, core, custom map[string]any) error {
	coreRaw, _ := json.Marshal(core)
	customRaw, _ := json.Marshal(custom)
	tag, err := q.Exec(ctx,
		`UPDATE workflow_records
		    SET core_fields = $2::jsonb, custom_fields = $3::jsonb, updated_at = NOW()
		  WHERE id = $1`,
		id, coreRaw, customRaw)
	if err != nil {
		return fmt.Errorf("update record fields: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRecordNotFound
	}
	return nil
}

// DeleteRecord permanently removes a workflow record and its history.
func DeleteRecord(ctx context.Context, q Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM workflow_records WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRecordNotFound
	}
	return nil
}

// ListCRMStatuses returns all states from the three CRM workflows (lead/prospect/
// customer) ordered by pipeline_order and sort_order, ready for a combined
// status dropdown. Each entry carries the prefixed label (e.g. "LEAD-New").
func ListCRMStatuses(ctx context.Context, q Querier) ([]StatusInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT ws.id, ws.key, ws.name, ws.is_initial, ws.is_terminal, ws.sort_order, ws.color,
		       wf.key, wf.name, wf.pipeline_order
		  FROM workflow_states ws
		  JOIN workflows wf ON wf.id = ws.workflow_id
		 WHERE wf.pipeline_order > 0
		 ORDER BY wf.pipeline_order, ws.sort_order, ws.name`)
	if err != nil {
		return nil, fmt.Errorf("list crm statuses: %w", err)
	}
	defer rows.Close()
	var out []StatusInfo
	for rows.Next() {
		var si StatusInfo
		var pipelineOrder int
		if err := rows.Scan(&si.StateID, &si.StateKey, &si.StatusLabel,
			&si.IsInitial, &si.IsTerminal, &si.SortOrder, &si.Color,
			&si.WorkflowKey, &si.WorkflowName, &pipelineOrder); err != nil {
			return nil, fmt.Errorf("scan status: %w", err)
		}
		out = append(out, si)
	}
	if out == nil {
		out = []StatusInfo{}
	}
	return out, rows.Err()
}

// AvailableTransitions returns the states reachable from the record's current
// state via defined transitions. Used to populate edit/transition dropdowns.
func AvailableTransitions(ctx context.Context, q Querier, rec *Record) ([]StatusInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT ws.id, ws.key, ws.name, ws.is_initial, ws.is_terminal, ws.sort_order, ws.color,
		       wf.key, wf.name
		  FROM workflow_transitions wt
		  JOIN workflow_states ws ON ws.id = wt.to_state_id
		  JOIN workflows wf ON wf.id = wt.workflow_id
		 WHERE wt.workflow_id = $1 AND wt.from_state_id = $2
		 ORDER BY wt.sort_order, ws.name`, rec.WorkflowID, rec.CurrentStateID)
	if err != nil {
		return nil, fmt.Errorf("available transitions: %w", err)
	}
	defer rows.Close()
	var out []StatusInfo
	for rows.Next() {
		var si StatusInfo
		if err := rows.Scan(&si.StateID, &si.StateKey, &si.StatusLabel,
			&si.IsInitial, &si.IsTerminal, &si.SortOrder, &si.Color,
			&si.WorkflowKey, &si.WorkflowName); err != nil {
			return nil, fmt.Errorf("scan transition state: %w", err)
		}
		out = append(out, si)
	}
	if out == nil {
		out = []StatusInfo{}
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
