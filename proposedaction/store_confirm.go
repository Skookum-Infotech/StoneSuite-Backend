package proposedaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
	"stonesuite-backend/domainevent"
	"stonesuite-backend/provenance"
)

// confirmedEventPayload carries ids only, never proposal contents.
type confirmedEventPayload struct {
	ActionID string `json:"actionId"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
}

// Confirm executes a caller's pending proposal in one transaction. The row is
// locked, the caller is re-authorized under their CURRENT roles, the payload
// is re-validated, and the pending->confirmed flip is atomic, so a proposal
// executes at most once. replayed is true when it was already confirmed (the
// stored result is returned and nothing runs again).
func (s *Store) Confirm(ctx context.Context, caller Caller, id string) (Action, bool, error) {
	if !uuidPattern.MatchString(id) {
		return Action{}, false, &NotFoundError{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Action{}, false, fmt.Errorf("confirm proposed action: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	cur, err := scanAction(tx.QueryRow(ctx,
		`SELECT `+actionColumns+` FROM ai_proposed_action WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Action{}, false, &NotFoundError{}
	}
	if err != nil {
		return Action{}, false, fmt.Errorf("lock proposed action: %w", err)
	}
	if cur.IdentityID != caller.IdentityID {
		return Action{}, false, &NotFoundError{Mismatch: true}
	}
	if done, replayed, err := s.confirmShortCircuit(ctx, tx, cur); done {
		return cur, replayed, err
	}

	ex, ok := s.reg.Lookup(cur.Resource, cur.Kind)
	if !ok {
		slog.Error("proposed action executor missing", "action_id", id, "resource", cur.Resource)
		return s.failAfterRollback(ctx, tx, id, msgNotCompleted, ErrExecutionFailed)
	}
	if err := s.authorize(ctx, tx, ex, caller, cur); err != nil {
		return Action{}, false, err
	}
	if err := ex.Validate(ctx, cur.Payload); err != nil {
		ve := asValidation(err)
		return s.failAfterRollback(ctx, tx, id, msgNoLongerValid, ve)
	}
	return s.flipAndExecute(ctx, tx, ex, caller, cur)
}

// confirmShortCircuit handles every non-pending state and an overdue pending
// row. done reports whether Confirm should return (cur, replayed, err) as is.
func (s *Store) confirmShortCircuit(ctx context.Context, tx pgx.Tx, cur Action) (done, replayed bool, err error) {
	switch cur.Status {
	case StatusConfirmed:
		return true, true, nil
	case StatusExpired:
		return true, false, ErrExpired
	case StatusCancelled, StatusFailed:
		return true, false, ErrConflict
	}
	if cur.ExpiresAt.After(nowFunc()) {
		return false, false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_proposed_action SET status = 'expired', resolved_at = now()
		WHERE id = $1 AND status = 'pending'`, cur.ID); err != nil {
		return true, false, fmt.Errorf("expire proposed action: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return true, false, fmt.Errorf("expire proposed action: commit: %w", err)
	}
	return true, false, ErrExpired
}

// authorize re-checks the confirmer's CURRENT grants and row-level scope.
func (s *Store) authorize(ctx context.Context, tx pgx.Tx, ex Executor, caller Caller, cur Action) error {
	act := actionFor(cur.Kind)
	res := ex.Resource()
	dec, err := authz.Check(ctx, tx, caller.IdentityID, res, act)
	if err != nil {
		return fmt.Errorf("confirm proposed action: authz: %w", err)
	}
	if !dec.Allowed {
		return &ForbiddenError{Resource: res, Action: act}
	}
	inScope, err := ex.InScope(ctx, tx, dec.Scope, caller, cur.Kind, cur.Payload)
	if err != nil {
		return fmt.Errorf("confirm proposed action: scope: %w", err)
	}
	if !inScope {
		// Row-level scope denial answers exactly like an unknown id (404) so
		// record ids can't be enumerated; the handler logs the IDOR event.
		return &NotFoundError{Mismatch: true}
	}
	return nil
}

// flipAndExecute performs the atomic flip, runs the executor, emits the
// event and commits. Any failure rolls the whole transaction back.
func (s *Store) flipAndExecute(ctx context.Context, tx pgx.Tx, ex Executor, caller Caller, cur Action) (Action, bool, error) {
	var flipped string
	err := tx.QueryRow(ctx, `
		UPDATE ai_proposed_action SET status = 'confirmed', resolved_at = now()
		WHERE id = $1 AND identity_id = $2 AND status = 'pending' AND expires_at > now()
		RETURNING id`, cur.ID, caller.IdentityID).Scan(&flipped)
	if errors.Is(err, pgx.ErrNoRows) {
		return Action{}, false, ErrConflict
	}
	if err != nil {
		return Action{}, false, fmt.Errorf("confirm proposed action: flip: %w", err)
	}

	via := viaFor(cur.Source)
	execCtx := provenance.With(ctx, via)
	ref, err := ex.Execute(execCtx, tx, caller, cur.Payload)
	if err != nil {
		slog.Error("proposed action execution failed", "action_id", cur.ID, "resource", cur.Resource, "error", err)
		return s.failAfterRollback(ctx, tx, cur.ID, msgNotCompleted, ErrExecutionFailed)
	}
	if err := domainevent.Emit(execCtx, tx, domainevent.Event{
		Type:        eventConfirmedType,
		Resource:    ref.Resource,
		RecordID:    ref.RecordID,
		ActorUserID: caller.UserID,
		Via:         via,
		Payload:     confirmedEventPayload{ActionID: cur.ID, Kind: string(cur.Kind), Source: string(cur.Source)},
	}); err != nil {
		return Action{}, false, fmt.Errorf("confirm proposed action: emit event: %w", err)
	}
	refJSON, err := json.Marshal(ref)
	if err != nil {
		return Action{}, false, fmt.Errorf("confirm proposed action: marshal result: %w", err)
	}
	out, err := scanAction(tx.QueryRow(ctx, `
		UPDATE ai_proposed_action SET result_ref = $2 WHERE id = $1
		RETURNING `+actionColumns, cur.ID, refJSON))
	if err != nil {
		return Action{}, false, fmt.Errorf("confirm proposed action: store result: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Action{}, false, fmt.Errorf("confirm proposed action: commit: %w", err)
	}
	return out, false, nil
}
