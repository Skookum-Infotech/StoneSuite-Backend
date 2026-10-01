package fabrication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrActionConflict asks the client to refresh stale state or fix its request identity.
var ErrActionConflict = errors.New("fabrication action conflicts with current state; refresh and retry")

// ActionActor is resolved from tenant authentication, never decoded from a body.
type ActionActor struct {
	IdentityID string
	EmployeeID int
	// AllScope is set only after an installation permission check.
	AllScope bool
}

// ActionResult is the durable receipt returned for a command and its retries.
type ActionResult struct {
	EventID string `json:"eventId"`
	Version int64  `json:"version"`
}

type actionCommand struct {
	Code, SubjectID string
	Payload         []byte
}
type actionState struct {
	JobID   int
	Version int64
}

type actionApply func(context.Context, pgx.Tx, actionState) error

// executeAction is internal: each exported typed action must authorize its resource
// before invoking it. Ownership is rechecked after acquiring the job row lock.
func executeAction(ctx context.Context, pool *pgxpool.Pool, jobUUID string, actor ActionActor, meta CommandMeta, command actionCommand, apply actionApply) (ActionResult, error) {
	var zero ActionResult
	for _, value := range []string{jobUUID, actor.IdentityID, meta.RequestID, command.SubjectID} {
		var id pgtype.UUID
		if err := id.Scan(value); err != nil || !id.Valid {
			return zero, ClientError{Msg: "A valid action, request, and identity UUID is required."}
		}
	}
	if meta.ExpectedVersion < 1 || command.Code == "" || !json.Valid(command.Payload) {
		return zero, ClientError{Msg: "A version and valid command payload are required."}
	}
	digest := sha256.Sum256(command.Payload)
	hash := hex.EncodeToString(digest[:])
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin fabrication action: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state actionState
	var workflow int
	var ownerIdentity *string
	err = tx.QueryRow(ctx, `SELECT fj.fabrication_job_id,fj.fabrication_job_record_version,fj.workflow_version,u.identity_id::text
 FROM fabrication_job fj LEFT JOIN employee e ON e.employee_id=fj.job_owner_id LEFT JOIN users u ON u.id=e.employee_user_id
 WHERE fj.fabrication_job_uuid=$1 AND fj.fabrication_job_deleted_at IS NULL FOR UPDATE OF fj`, jobUUID).Scan(&state.JobID, &state.Version, &workflow, &ownerIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrNotFound
	}
	if err != nil {
		return zero, fmt.Errorf("lock action job: %w", err)
	}
	if !actor.AllScope && (ownerIdentity == nil || *ownerIdentity != actor.IdentityID) {
		return zero, ErrNotFound
	}
	if workflow != WorkflowPieceActions {
		return zero, ErrActionConflict
	}
	var priorCode, priorSubject, priorHash string
	var prior []byte
	err = tx.QueryRow(ctx, `SELECT action_code,subject_uuid::text,payload_hash,result FROM fabrication_action_event
 WHERE fabrication_job_id=$1 AND actor_identity_id=$2 AND request_id=$3`, state.JobID, actor.IdentityID, meta.RequestID).Scan(&priorCode, &priorSubject, &priorHash, &prior)
	if err == nil {
		if priorCode != command.Code || priorSubject != command.SubjectID || priorHash != hash {
			return zero, ErrActionConflict
		}
		var result ActionResult
		if err = json.Unmarshal(prior, &result); err != nil {
			return zero, fmt.Errorf("decode action receipt: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("read action receipt: %w", err)
	}
	if state.Version != meta.ExpectedVersion {
		return zero, ErrActionConflict
	}
	if err = apply(ctx, tx, state); err != nil {
		return zero, err
	}
	result := ActionResult{Version: state.Version + 1}
	if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&result.EventID); err != nil {
		return zero, fmt.Errorf("allocate action event: %w", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return zero, fmt.Errorf("encode action result: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE fabrication_job SET fabrication_job_record_version=$2,fabrication_job_updated_at=NOW(),fabrication_job_updated_by=$3 WHERE fabrication_job_id=$1`, state.JobID, result.Version, nullableInt(actor.EmployeeID))
	if err != nil {
		return zero, fmt.Errorf("update action version: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO fabrication_action_event(event_uuid,fabrication_job_id,actor_identity_id,actor_employee_id,request_id,action_code,subject_uuid,payload_hash,prior_version,resulting_version,result)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, result.EventID, state.JobID, actor.IdentityID, nullableInt(actor.EmployeeID), meta.RequestID, command.Code, command.SubjectID, hash, state.Version, result.Version, encoded)
	if err != nil {
		return zero, fmt.Errorf("record fabrication action: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit fabrication action: %w", err)
	}
	return result, nil
}
