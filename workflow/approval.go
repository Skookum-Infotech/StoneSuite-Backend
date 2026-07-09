package workflow

import (
	"context"
	"errors"
	"fmt"
)

// Approval-flow sentinel errors (caller-facing; mapped to HTTP status by the
// controller layer, never surfaced as 500s).
var (
	// ErrNotApprover is returned when the caller is not an active approver for
	// the record's current state.
	ErrNotApprover = errors.New("you are not an assigned approver for this record's current status")
	// ErrAlreadyApproved is returned when the caller has already signed off on
	// the record's current pending cycle.
	ErrAlreadyApproved = errors.New("you have already approved this record")
)

// Approval is the read-only approver-gating overlay for a record's current
// state. It tells the UI whether the record is waiting on approvers, how many,
// who, and whether the current caller may approve.
type Approval struct {
	Status          string   `json:"status"` // none | pending | approved
	Required        int      `json:"required"`
	Approved        int      `json:"approved"`
	ApproverUserIDs []string `json:"approverUserIds"`
	ApprovedUserIDs []string `json:"approvedUserIds"`
	CanApprove      bool     `json:"canApprove"`
}

// approvalStatusOf derives the approval status from the required/approved
// counts. Pure so it is exhaustively unit-testable without a database.
//   - none:     the state has no active approvers (not gated)
//   - approved: every active approver has signed off
//   - pending:  at least one active approver has not yet signed off
func approvalStatusOf(required, approved int) string {
	switch {
	case required == 0:
		return "none"
	case approved >= required:
		return "approved"
	default:
		return "pending"
	}
}

// approvalGateLocked reports whether a record in a state with the given
// required/approved counts must be blocked from any outbound transition. Pure.
func approvalGateLocked(required, approved int) bool {
	return required > 0 && approved < required
}

// ----- store queries ---------------------------------------------------------

// StateApproverUserIDs returns the tenant user ids of every active approver
// configured for stateID, in configuration order.
func StateApproverUserIDs(ctx context.Context, q Querier, stateID string) ([]string, error) {
	if stateID == "" {
		return []string{}, nil
	}
	rows, err := q.Query(ctx, `
		SELECT approver_user_id::text FROM workflow_state_approver
		WHERE state_id = $1 AND is_active
		ORDER BY created_at, id`, stateID)
	if err != nil {
		return nil, fmt.Errorf("list state approvers: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan state approver: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// approvedUserIDs returns the tenant user ids that have signed off on record
// recordID for its occupancy of stateID, in sign-off order.
func approvedUserIDs(ctx context.Context, q Querier, recordID, stateID string) ([]string, error) {
	if stateID == "" {
		return []string{}, nil
	}
	rows, err := q.Query(ctx, `
		SELECT approver_user_id::text FROM workflow_record_approval
		WHERE record_id = $1 AND state_id = $2
		ORDER BY approved_at, id`, recordID, stateID)
	if err != nil {
		return nil, fmt.Errorf("list record approvals: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan record approval: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// approvalCounts returns (required, approved) for record recordID at stateID:
// how many active approvers the state requires, and how many have signed off.
// Cheap COUNT-only queries used by the transition gate.
func approvalCounts(ctx context.Context, q Querier, recordID, stateID string) (required, approved int, err error) {
	if stateID == "" {
		return 0, 0, nil
	}
	if err = q.QueryRow(ctx,
		`SELECT COUNT(*) FROM workflow_state_approver WHERE state_id = $1 AND is_active`,
		stateID).Scan(&required); err != nil {
		return 0, 0, fmt.Errorf("count state approvers: %w", err)
	}
	if err = q.QueryRow(ctx,
		`SELECT COUNT(*) FROM workflow_record_approval WHERE record_id = $1 AND state_id = $2`,
		recordID, stateID).Scan(&approved); err != nil {
		return 0, 0, fmt.Errorf("count record approvals: %w", err)
	}
	return required, approved, nil
}

// ----- overlay ---------------------------------------------------------------

// ApprovalOverlay builds the read-only approval overlay for a single record at
// its current state, from the caller's perspective. callerUserID may be empty
// (e.g. unresolved identity), in which case CanApprove is always false.
func ApprovalOverlay(ctx context.Context, q Querier, rec *Record, callerUserID string) (*Approval, error) {
	approvers, err := StateApproverUserIDs(ctx, q, rec.CurrentStateID)
	if err != nil {
		return nil, err
	}
	approved, err := approvedUserIDs(ctx, q, rec.ID, rec.CurrentStateID)
	if err != nil {
		return nil, err
	}
	return buildApproval(approvers, approved, callerUserID), nil
}

// buildApproval assembles an Approval from the state's approver set, the
// record's sign-offs, and the caller. Pure so overlay assembly is unit-testable.
func buildApproval(approvers, approved []string, callerUserID string) *Approval {
	required, approvedN := len(approvers), len(approved)
	status := approvalStatusOf(required, approvedN)
	canApprove := status == "pending" &&
		callerUserID != "" &&
		contains(approvers, callerUserID) &&
		!contains(approved, callerUserID)
	// Coalesce to empty slices so the overlay always serializes [] not null,
	// including for records whose current state has no approvers.
	if approvers == nil {
		approvers = []string{}
	}
	if approved == nil {
		approved = []string{}
	}
	return &Approval{
		Status:          status,
		Required:        required,
		Approved:        approvedN,
		ApproverUserIDs: approvers,
		ApprovedUserIDs: approved,
		CanApprove:      canApprove,
	}
}

// AttachApprovalOverlays populates rec.Approval for every record in recs using
// set-based queries (no N+1), from callerUserID's perspective. Records whose
// current state has no active approvers get a "none" overlay.
func AttachApprovalOverlays(ctx context.Context, q Querier, recs []Record, callerUserID string) error {
	if len(recs) == 0 {
		return nil
	}
	stateIDs := make([]string, 0, len(recs))
	recordIDs := make([]string, 0, len(recs))
	seenState := map[string]bool{}
	for i := range recs {
		recordIDs = append(recordIDs, recs[i].ID)
		if s := recs[i].CurrentStateID; s != "" && !seenState[s] {
			seenState[s] = true
			stateIDs = append(stateIDs, s)
		}
	}

	// approvers[stateID] = ordered active approver user ids for that state.
	approvers := map[string][]string{}
	if len(stateIDs) > 0 {
		rows, err := q.Query(ctx, `
			SELECT state_id::text, approver_user_id::text
			FROM workflow_state_approver
			WHERE state_id = ANY($1) AND is_active
			ORDER BY created_at, id`, stateIDs)
		if err != nil {
			return fmt.Errorf("batch load state approvers: %w", err)
		}
		for rows.Next() {
			var sid, uid string
			if err := rows.Scan(&sid, &uid); err != nil {
				rows.Close()
				return fmt.Errorf("scan batch approver: %w", err)
			}
			approvers[sid] = append(approvers[sid], uid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	// approved[recordID+"\x00"+stateID] = ordered sign-off user ids.
	approved := map[string][]string{}
	rows, err := q.Query(ctx, `
		SELECT record_id::text, state_id::text, approver_user_id::text
		FROM workflow_record_approval
		WHERE record_id = ANY($1)
		ORDER BY approved_at, id`, recordIDs)
	if err != nil {
		return fmt.Errorf("batch load record approvals: %w", err)
	}
	for rows.Next() {
		var rid, sid, uid string
		if err := rows.Scan(&rid, &sid, &uid); err != nil {
			rows.Close()
			return fmt.Errorf("scan batch approval: %w", err)
		}
		approved[rid+"\x00"+sid] = append(approved[rid+"\x00"+sid], uid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for i := range recs {
		st := recs[i].CurrentStateID
		recs[i].Approval = buildApproval(approvers[st], approved[recs[i].ID+"\x00"+st], callerUserID)
	}
	return nil
}

// ----- sign-off + queue ------------------------------------------------------

// Approve records callerUserID's sign-off on the record's current state. It
// serializes with transitions via a row lock, so a record cannot both be
// approved and transitioned concurrently. Returns the refreshed record.
//
// Errors: ErrRecordNotFound; ErrNotApprover when the caller is not an active
// approver of the record's current state (this also covers a state with no
// approvers, so callers can map it to a uniform 404 to prevent id enumeration);
// ErrAlreadyApproved when the caller already signed off this cycle.
func Approve(ctx context.Context, pool Beginner, recordID, callerUserID string) (*Record, error) {
	if callerUserID == "" {
		return nil, ErrNotApprover
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the record and read its authoritative current state.
	var stateID *string
	if err := tx.QueryRow(ctx,
		`SELECT current_state_id FROM workflow_records WHERE id = $1 FOR UPDATE`,
		recordID).Scan(&stateID); err != nil {
		return nil, ErrRecordNotFound
	}
	state := ""
	if stateID != nil {
		state = *stateID
	}

	approvers, err := StateApproverUserIDs(ctx, tx, state)
	if err != nil {
		return nil, err
	}
	// Membership is checked first (before any "is it gated?" branch) so a caller
	// who is not a current approver gets a single, uniform ErrNotApprover whether
	// the record is gated, ungated, or the caller simply isn't assigned — the
	// handler maps all of these to 404 so record ids can't be enumerated.
	if !contains(approvers, callerUserID) {
		return nil, ErrNotApprover
	}
	signed, err := approvedUserIDs(ctx, tx, recordID, state)
	if err != nil {
		return nil, err
	}
	if contains(signed, callerUserID) {
		return nil, ErrAlreadyApproved
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO workflow_record_approval (record_id, state_id, approver_user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (record_id, state_id, approver_user_id) DO NOTHING`,
		recordID, state, callerUserID); err != nil {
		return nil, fmt.Errorf("record approval: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve: %w", err)
	}
	return GetRecord(ctx, pool, recordID)
}

// PendingApprovals lists records where callerUserID is an active approver of
// the record's current state and has not yet signed off — the caller's queue.
func PendingApprovals(ctx context.Context, q Querier, callerUserID string) ([]Record, error) {
	if callerUserID == "" {
		return []Record{}, nil
	}
	rows, err := q.Query(ctx, `SELECT `+recordColumns+`
		FROM workflow_records wr
		WHERE EXISTS (
			SELECT 1 FROM workflow_state_approver a
			WHERE a.state_id = wr.current_state_id
			  AND a.approver_user_id = $1 AND a.is_active
		)
		AND NOT EXISTS (
			SELECT 1 FROM workflow_record_approval ra
			WHERE ra.record_id = wr.id
			  AND ra.state_id = wr.current_state_id
			  AND ra.approver_user_id = $1
		)
		ORDER BY wr.created_at DESC`, callerUserID)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// ----- config ----------------------------------------------------------------

// ReplaceStateApprovers sets the active approver set for stateID to exactly
// userIDs (validated by the caller to be real tenant users), replacing whatever
// was there. No count cap is enforced in the backend — the 2-approver limit is
// a UI concern; the backend holds any number. createdBy may be empty.
func ReplaceStateApprovers(ctx context.Context, pool Beginner, stateID string, userIDs []string, createdBy string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace approvers: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM workflow_state_approver WHERE state_id = $1`, stateID); err != nil {
		return fmt.Errorf("clear state approvers: %w", err)
	}
	for _, uid := range userIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO workflow_state_approver (state_id, approver_user_id, created_by)
			VALUES ($1, $2, $3)
			ON CONFLICT (state_id, approver_user_id) DO UPDATE SET is_active = TRUE`,
			stateID, uid, nullIfEmpty(createdBy)); err != nil {
			return fmt.Errorf("insert state approver: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace approvers: %w", err)
	}
	return nil
}

// contains reports whether s is in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
