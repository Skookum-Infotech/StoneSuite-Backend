package crmstore

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// workflowStore is the DesignV1 implementation: it delegates to the JSONB
// workflow engine (workflow.Engine + workflow.* store functions). This is the
// original CRM behavior, kept byte-for-byte compatible.
type workflowStore struct{}

var _ Store = (*workflowStore)(nil)

func (s *workflowStore) engine() *workflow.Engine { return workflow.NewEngine() }

func (s *workflowStore) AllStatuses(ctx context.Context, pool *pgxpool.Pool) ([]workflow.StatusInfo, error) {
	return workflow.ListCRMStatuses(ctx, pool)
}

func (s *workflowStore) Statuses(ctx context.Context, pool *pgxpool.Pool, key string) ([]workflow.StatusInfo, error) {
	def, err := s.defForKey(ctx, pool, key)
	if err != nil {
		return nil, err
	}
	out := make([]workflow.StatusInfo, 0, len(def.States))
	for _, st := range def.States {
		out = append(out, statusFromState(def, st))
	}
	return out, nil
}

func (s *workflowStore) KeyForRecord(ctx context.Context, pool *pgxpool.Pool, id string) (string, error) {
	rec, err := workflow.GetRecord(ctx, pool, id)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		return "", ErrRecordNotFound
	}
	if err != nil {
		return "", err
	}
	def, err := workflow.LoadDefinition(ctx, pool, rec.WorkflowID)
	if err != nil {
		return "", err
	}
	return def.Workflow.Key, nil
}

func (s *workflowStore) ListRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string) ([]workflow.Record, error) {
	wf, err := workflow.GetWorkflowByKey(ctx, pool, key)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil, ClientError{Msg: "Workflow not found."}
	}
	if err != nil {
		return nil, err
	}
	callerUserID, teamIDs := s.scopeFilter(ctx, pool, scope, actorIdentityID)
	return workflow.ListRecords(ctx, pool, wf.ID, scope, callerUserID, teamIDs)
}

func (s *workflowStore) CreateRecord(ctx context.Context, pool *pgxpool.Pool, key string, in CreateInput) (*workflow.Record, error) {
	def, err := s.defForKey(ctx, pool, key)
	if err != nil {
		return nil, err
	}
	owner := in.OwnerUserID
	if owner == "" {
		if uid, uerr := workflow.UserIDByIdentity(ctx, pool, in.ActorIdentityID); uerr == nil {
			owner = uid
		}
	}
	rec, err := s.engine().CreateRecord(ctx, pool, def, owner, in.TeamID, in.CoreFields, in.CustomFields)
	if err != nil {
		return nil, mapWorkflowErr(err)
	}
	return rec, nil
}

func (s *workflowStore) GetRecord(ctx context.Context, pool *pgxpool.Pool, id string) (*workflow.Record, error) {
	rec, err := workflow.GetRecord(ctx, pool, id)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		return nil, ErrRecordNotFound
	}
	return rec, err
}

func (s *workflowStore) UpdateRecord(ctx context.Context, pool *pgxpool.Pool, id string, core, custom map[string]any) error {
	rec, err := workflow.GetRecord(ctx, pool, id)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		return ErrRecordNotFound
	}
	if err != nil {
		return err
	}
	def, err := workflow.LoadDefinition(ctx, pool, rec.WorkflowID)
	if err != nil {
		return err
	}
	merged := rec.CustomFields
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range custom {
		merged[k] = v
	}
	if err := workflow.ValidateCustomFields(def.Fields, merged); err != nil {
		return ClientError{Msg: err.Error()}
	}
	mergedCore := rec.CoreFields
	if mergedCore == nil {
		mergedCore = map[string]any{}
	}
	for k, v := range core {
		mergedCore[k] = v
	}
	return workflow.UpdateRecordAllFields(ctx, pool, rec.ID, mergedCore, merged)
}

func (s *workflowStore) DeleteRecord(ctx context.Context, pool *pgxpool.Pool, id string) error {
	return workflow.DeleteRecord(ctx, pool, id)
}

func (s *workflowStore) AvailableTransitions(ctx context.Context, pool *pgxpool.Pool, id string) ([]workflow.StatusInfo, error) {
	rec, err := workflow.GetRecord(ctx, pool, id)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	return workflow.AvailableTransitions(ctx, pool, rec)
}

func (s *workflowStore) TransitionRecord(ctx context.Context, pool *pgxpool.Pool, id, toStatusID, actorIdentityID string) (*workflow.Record, error) {
	rec, err := workflow.GetRecord(ctx, pool, id)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	def, err := workflow.LoadDefinition(ctx, pool, rec.WorkflowID)
	if err != nil {
		return nil, err
	}
	// Per-transition permission refinement, if declared on the edge.
	if t, ferr := s.engine().ValidateTransition(def, rec, toStatusID); ferr == nil && t.RequiredPermission != "" {
		if res, act, ok := splitPermission(t.RequiredPermission); ok {
			d, cerr := authz.Check(ctx, pool, actorIdentityID, res, act)
			if cerr != nil || !d.Allowed {
				return nil, ClientError{Msg: "This transition requires " + t.RequiredPermission + "."}
			}
		}
	}
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, actorIdentityID)
	updated, err := s.engine().Apply(ctx, pool, def, rec, toStatusID, actorUserID)
	if err != nil {
		return nil, mapWorkflowErr(err)
	}
	return updated, nil
}

func (s *workflowStore) ConvertRecord(ctx context.Context, pool *pgxpool.Pool, id, targetKey string, core, custom map[string]any, actorIdentityID string) (*workflow.Record, string, error) {
	sourceRec, err := workflow.GetRecord(ctx, pool, id)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		return nil, "", ErrRecordNotFound
	}
	if err != nil {
		return nil, "", err
	}
	targetDef, err := s.defForKey(ctx, pool, targetKey)
	if err != nil {
		return nil, "", err
	}
	if core == nil {
		core = map[string]any{}
	}
	for k, v := range sourceRec.CoreFields {
		if _, exists := core[k]; !exists {
			core[k] = v
		}
	}
	if custom == nil {
		custom = map[string]any{}
	}
	targetFields := make(map[string]struct{}, len(targetDef.Fields))
	for _, f := range targetDef.Fields {
		targetFields[f.Key] = struct{}{}
	}
	for k, v := range sourceRec.CustomFields {
		if _, inTarget := targetFields[k]; inTarget {
			if _, exists := custom[k]; !exists {
				custom[k] = v
			}
		}
	}
	for k, v := range sourceRec.CoreFields {
		if _, inTarget := targetFields[k]; inTarget {
			if _, exists := custom[k]; !exists {
				custom[k] = v
			}
		}
	}
	owner, _ := workflow.UserIDByIdentity(ctx, pool, actorIdentityID)
	newRec, err := s.engine().ConvertRecord(ctx, pool, targetDef, owner, "", sourceRec.ID, core, custom)
	if err != nil {
		return nil, "", mapWorkflowErr(err)
	}
	return newRec, sourceRec.ID, nil
}

// Approve is not part of the DesignV1 workflow model.
func (s *workflowStore) Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error) {
	return nil, ErrNotSupported
}

// ----- helpers ---------------------------------------------------------------

func (s *workflowStore) defForKey(ctx context.Context, pool *pgxpool.Pool, key string) (*workflow.Definition, error) {
	wf, err := workflow.GetWorkflowByKey(ctx, pool, key)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil, ClientError{Msg: "Workflow not found."}
	}
	if err != nil {
		return nil, err
	}
	return workflow.LoadDefinition(ctx, pool, wf.ID)
}

func (s *workflowStore) scopeFilter(ctx context.Context, pool *pgxpool.Pool, scope, identityID string) (string, []string) {
	if scope == string(authz.ScopeAll) {
		return "", nil
	}
	uid, err := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err != nil {
		return "", nil
	}
	if scope == string(authz.ScopeTeam) {
		teams, _ := workflow.TeamIDsForUser(ctx, pool, uid)
		return uid, teams
	}
	return uid, nil
}

func statusFromState(def *workflow.Definition, st workflow.State) workflow.StatusInfo {
	return workflow.StatusInfo{
		StateID:      st.ID,
		StateKey:     st.Key,
		StatusLabel:  st.Name,
		WorkflowKey:  def.Workflow.Key,
		WorkflowName: def.Workflow.Name,
		IsInitial:    st.IsInitial,
		IsTerminal:   st.IsTerminal,
		SortOrder:    st.SortOrder,
		Color:        st.Color,
	}
}

func mapWorkflowErr(err error) error {
	var te workflow.TransitionError
	if errors.As(err, &te) {
		return ClientError{Msg: te.Error()}
	}
	var ve workflow.ValidationErrors
	if errors.As(err, &ve) {
		return ClientError{Msg: ve.Error()}
	}
	return err
}

func splitPermission(p string) (authz.Resource, authz.Action, bool) {
	parts := strings.SplitN(p, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return authz.Resource(parts[0]), authz.Action(parts[1]), true
}
