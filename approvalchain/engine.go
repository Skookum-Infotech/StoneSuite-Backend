// approvalchain/engine.go
package approvalchain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Sentinel errors a module built on this engine returns directly via
// errors.Is -- controllers map these to HTTP status the same way
// controllers/estimate.go maps estimate.Err*: ErrApprovalRequired and
// ErrApprovalNotRequired -> 409, ErrNotApprover -> 403, ErrNotFound -> 404.
//
// Estimate, Quote and Sales Order predate this engine and keep their own
// differently-worded sentinels (estimate.ErrNotApprover etc.) rather than
// switching to these, so their already-shipped error text doesn't change;
// every module added after this engine should use these directly instead of
// declaring its own.
var (
	ErrNotFound            = errors.New("record not found")
	ErrNotApprover         = errors.New("you are not a configured approver for this record's current status")
	ErrApprovalRequired    = errors.New("this record must be approved before it can leave its current status")
	ErrApprovalNotRequired = errors.New("this record's current status does not require approval")
)

// Approval status values stored in RecordSpec.ApprovalStatusColumn, mirroring
// the AD-8 design proven in estimate/approval.go.
const (
	StatusNone     = "none"
	StatusPending  = "pending"
	StatusApproved = "approved"
)

// AlwaysAllowedExitCodes are target status codes a manual transition may
// move a record to even while it sits in a gated, unapproved status.
// Rejecting, cancelling, or voiding a record is a way OUT of the approval
// process, not a way past it, so none of those need sign-off first.
var AlwaysAllowedExitCodes = map[string]bool{"VOID": true, "CANC": true, "RJCT": true}

// CheckTransitionGate enforces AD-8 at a manual status transition. Each
// module's own Transition already loads requiredHere (activeApproverCount at
// the record's current status) and approvalStatus for its own validation;
// this just centralizes the resulting predicate instead of every module
// hand-rolling it. Returns ErrApprovalRequired when blocked.
func CheckTransitionGate(requiredHere int, approvalStatus, toStatusCode string) error {
	if requiredHere > 0 && approvalStatus != StatusApproved && !AlwaysAllowedExitCodes[toStatusCode] {
		return ErrApprovalRequired
	}
	return nil
}

// RecordSpec names the columns Engine needs on a relational module's main
// record table and its history table. Every relational module's table
// already has this shape (see ModuleConfig's package doc), but column
// *prefixes* aren't always the table name -- e.g. inventory_adjustment's own
// columns use "adjustment_", and fabrication_job's approval columns use
// "job_" while everything else on that table uses "fabrication_job_" -- so
// this is spelled out explicitly per module rather than derived from Table
// by convention.
type RecordSpec struct {
	Table                string // e.g. "credit_memo"
	HistoryTable         string // e.g. "credit_memo_history"
	IDColumn             string // e.g. "credit_memo_id" -- also the FK column name on HistoryTable/ApproverTable/ApprovalTable
	UUIDColumn           string // e.g. "credit_memo_uuid"
	StatusColumn         string // e.g. "credit_memo_status"
	ApprovalStatusColumn string // e.g. "credit_memo_approval_status"
	ApprovedByColumn     string // e.g. "credit_memo_approved_by"
	UpdatedAtColumn      string
	UpdatedByColumn      string
	RecordVersionColumn  string
	DeletedAtColumn      string
}

// ApproveOutcome reports what Approve actually did, before the caller
// reloads its own typed record via its module's Get.
type ApproveOutcome struct {
	Finalized bool // record auto-advanced to the gate's target status
	Override  bool // finalization was a super-admin bypass, not quorum
}

// Approve records one approver's sign-off on a record at its current status
// and, once every configured approver for that status has signed off (or a
// super admin overrides), auto-advances the record to the gate's configured
// TargetStatusCode in the same transaction -- see Gate. Mirrors the AD-8
// design proven in estimate/approval.go, generalized so every relational
// module shares one implementation instead of copy-pasting it.
//
// callerIsSuperAdmin lets a super admin approve a gate they aren't
// personally configured on, skipping quorum entirely rather than counting as
// one more sign-off -- logged distinctly as "approve_override" vs "approve".
func Approve(ctx context.Context, pool *pgxpool.Pool, cfg ModuleConfig, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (ApproveOutcome, error) {
	rec := cfg.Record
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ApproveOutcome{}, fmt.Errorf("begin approve %s: %w", rec.Table, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, fmt.Sprintf(
		`SELECT %s, %s FROM %s WHERE %s = $1 AND %s IS NULL FOR UPDATE`,
		rec.IDColumn, rec.StatusColumn, rec.Table, rec.UUIDColumn, rec.DeletedAtColumn,
	), uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApproveOutcome{}, ErrNotFound
	}
	if err != nil {
		return ApproveOutcome{}, fmt.Errorf("load %s for approval: %w", rec.Table, err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, cfg.RecordTypeCode)
	if err != nil {
		return ApproveOutcome{}, err
	}
	curStatusCode, err := statusCodeByID(ctx, tx, curStatusID)
	if err != nil {
		return ApproveOutcome{}, err
	}
	gate, gated := cfg.GateFor(curStatusCode)
	if !gated {
		return ApproveOutcome{}, ErrApprovalNotRequired
	}

	required, err := activeApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, curStatusID)
	if err != nil {
		return ApproveOutcome{}, err
	}
	if required == 0 {
		return ApproveOutcome{}, ErrApprovalNotRequired
	}

	isApprover, err := isConfiguredApprover(ctx, tx, cfg.ApproverTable, recordTypeID, curStatusID, approverEmployeeID)
	if err != nil {
		return ApproveOutcome{}, err
	}
	if !isApprover && !callerIsSuperAdmin {
		return ApproveOutcome{}, ErrNotApprover
	}

	if !isApprover && callerIsSuperAdmin {
		if err := finalize(ctx, tx, cfg, recordTypeID, internalID, curStatusID, gate.TargetStatusCode, approverEmployeeID, "approve_override"); err != nil {
			return ApproveOutcome{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ApproveOutcome{}, fmt.Errorf("commit approve %s: %w", rec.Table, err)
		}
		return ApproveOutcome{Finalized: true, Override: true}, nil
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s (%s, record_status_id, approver_employee_id) VALUES ($1, $2, $3)
		 ON CONFLICT (%s, record_status_id, approver_employee_id) DO NOTHING`,
		cfg.ApprovalTable, rec.IDColumn, rec.IDColumn,
	), internalID, curStatusID, approverEmployeeID); err != nil {
		return ApproveOutcome{}, fmt.Errorf("record %s approval: %w", rec.Table, err)
	}

	approved, err := signOffCount(ctx, tx, cfg.ApprovalTable, rec.IDColumn, internalID, curStatusID)
	if err != nil {
		return ApproveOutcome{}, err
	}
	if approved >= required {
		if err := finalize(ctx, tx, cfg, recordTypeID, internalID, curStatusID, gate.TargetStatusCode, approverEmployeeID, "approve"); err != nil {
			return ApproveOutcome{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ApproveOutcome{}, fmt.Errorf("commit approve %s: %w", rec.Table, err)
		}
		return ApproveOutcome{Finalized: true}, nil
	}

	// Still waiting on the remaining configured approver(s) -- record this
	// sign-off but leave the record in its current gated status.
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET %s = $2, %s = NOW() WHERE %s = $1`,
		rec.Table, rec.ApprovalStatusColumn, rec.UpdatedAtColumn, rec.IDColumn,
	), internalID, StatusPending); err != nil {
		return ApproveOutcome{}, fmt.Errorf("update %s approval status: %w", rec.Table, err)
	}
	writeGenericHistory(ctx, tx, rec, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return ApproveOutcome{}, fmt.Errorf("commit approve %s: %w", rec.Table, err)
	}
	return ApproveOutcome{}, nil
}

// finalize moves the record from curStatusID to targetStatusCode inside the
// caller's already-open tx, once approval is fully resolved -- either quorum
// was just met or a super admin overrode the gate. Recomputes the *target*
// status's own approval gate rather than assuming it can never itself be
// gated, matching how each module's own Transition treats its target.
func finalize(ctx context.Context, tx pgx.Tx, cfg ModuleConfig, recordTypeID, internalID, curStatusID int, targetStatusCode string, approverEmployeeID int, historyAction string) error {
	rec := cfg.Record
	toStatusID, _, err := statusIDAndLabelByCode(ctx, tx, recordTypeID, targetStatusCode)
	if err != nil {
		return fmt.Errorf("resolve %s status: %w", targetStatusCode, err)
	}
	targetApprovers, err := activeApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, toStatusID)
	if err != nil {
		return err
	}
	newApprovalStatus := StatusNone
	if targetApprovers > 0 {
		newApprovalStatus = StatusPending
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET %s = $2, %s = $3, %s = $4, %s = NOW(), %s = $4, %s = %s + 1 WHERE %s = $1`,
		rec.Table, rec.StatusColumn, rec.ApprovalStatusColumn, rec.ApprovedByColumn,
		rec.UpdatedAtColumn, rec.UpdatedByColumn, rec.RecordVersionColumn, rec.RecordVersionColumn, rec.IDColumn,
	), internalID, toStatusID, newApprovalStatus, nullIntOrNil(approverEmployeeID)); err != nil {
		return fmt.Errorf("finalize approve %s: %w", rec.Table, err)
	}
	writeGenericHistory(ctx, tx, rec, internalID, historyAction, &curStatusID, &toStatusID, approverEmployeeID)
	return nil
}

// ApproverInfo names one configured approver for display and whether
// they've already signed off on the current round.
type ApproverInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Approved bool   `json:"approved"`
}

// ApprovalInfo tells a detail page whether a record is actually gated right
// now, who is configured to sign off on it and who already has, and whether
// the requesting caller can approve it -- so the UI can show a banner
// instead of a transition control that would just 409.
type ApprovalInfo struct {
	// Gated is computed fresh (activeApproverCount > 0 && approvalStatus !=
	// approved), NOT read off the stored approval-status column alone, which
	// goes stale the moment an admin edits the approver list out from under
	// a record already sitting in a gated status.
	Gated bool `json:"gated"`
	// Approvers, RequiredApprovals, ApprovedCount, CanApprove, IsOverride and
	// CallerAlreadyApproved are only meaningful while Gated is true.
	Approvers         []ApproverInfo `json:"approvers"`
	RequiredApprovals int            `json:"requiredApprovals"`
	ApprovedCount     int            `json:"approvedCount"`
	CanApprove        bool           `json:"canApprove"`
	// IsOverride is true when CanApprove is true only because the caller is
	// a super admin, not because they're on Approvers.
	IsOverride bool `json:"isOverride"`
	// CallerAlreadyApproved is true when the caller is a configured approver
	// who has already signed off this round -- quorum may still need others,
	// but the caller's own part is done.
	CallerAlreadyApproved bool `json:"callerAlreadyApproved"`
}

// GetInfo resolves ApprovalInfo for a record. Returns Gated: false without
// error once the record isn't currently gated (including when it's simply
// sitting in a status that isn't one of cfg.Gates at all).
func GetInfo(ctx context.Context, pool *pgxpool.Pool, cfg ModuleConfig, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (ApprovalInfo, error) {
	rec := cfg.Record
	var internalID, curStatusID int
	var approvalStatus string
	err := pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT %s, %s, %s FROM %s WHERE %s = $1 AND %s IS NULL`,
		rec.IDColumn, rec.StatusColumn, rec.ApprovalStatusColumn, rec.Table, rec.UUIDColumn, rec.DeletedAtColumn,
	), uuid).Scan(&internalID, &curStatusID, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalInfo{}, ErrNotFound
	}
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load %s for approval info: %w", rec.Table, err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, pool, cfg.RecordTypeCode)
	if err != nil {
		return ApprovalInfo{}, err
	}
	curStatusCode, err := statusCodeByID(ctx, pool, curStatusID)
	if err != nil {
		return ApprovalInfo{}, err
	}
	if _, gated := cfg.GateFor(curStatusCode); !gated {
		return ApprovalInfo{Gated: false}, nil
	}

	required, err := activeApproverCount(ctx, pool, cfg.ApproverTable, recordTypeID, curStatusID)
	if err != nil {
		return ApprovalInfo{}, err
	}
	if required == 0 || approvalStatus == StatusApproved {
		return ApprovalInfo{Gated: false}, nil
	}

	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT e.employee_id, COALESCE(NULLIF(u.full_name,''), u.email),
			EXISTS(SELECT 1 FROM %s ap
				WHERE ap.%s = $3 AND ap.record_status_id = $2 AND ap.approver_employee_id = e.employee_id)
		FROM %s ea
		JOIN employee e ON e.employee_id = ea.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE ea.record_type_id = $1 AND ea.record_status_id = $2 AND ea.is_active
		ORDER BY 2`, cfg.ApprovalTable, rec.IDColumn, cfg.ApproverTable),
		recordTypeID, curStatusID, internalID)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load %s approvers: %w", rec.Table, err)
	}
	defer rows.Close()
	var approvers []ApproverInfo
	approvedCount := 0
	callerAlreadyApproved := false
	for rows.Next() {
		var a ApproverInfo
		var id int
		if err := rows.Scan(&id, &a.Name, &a.Approved); err != nil {
			return ApprovalInfo{}, fmt.Errorf("scan %s approver: %w", rec.Table, err)
		}
		a.ID = fmt.Sprint(id)
		if a.Approved {
			approvedCount++
			if id == callerEmployeeID {
				callerAlreadyApproved = true
			}
		}
		approvers = append(approvers, a)
	}
	if err := rows.Err(); err != nil {
		return ApprovalInfo{}, fmt.Errorf("iterate %s approvers: %w", rec.Table, err)
	}

	isApprover, err := isConfiguredApprover(ctx, pool, cfg.ApproverTable, recordTypeID, curStatusID, callerEmployeeID)
	if err != nil {
		return ApprovalInfo{}, err
	}
	return ApprovalInfo{
		Gated:                 true,
		Approvers:             approvers,
		RequiredApprovals:     required,
		ApprovedCount:         approvedCount,
		CanApprove:            isApprover || callerIsSuperAdmin,
		IsOverride:            !isApprover && callerIsSuperAdmin,
		CallerAlreadyApproved: callerAlreadyApproved,
	}, nil
}

// ActiveApproverCount returns how many active approvers are configured on
// approverTable for a record type at a status. Zero means that status isn't
// gated at all, regardless of what a stored approval-status column says --
// every module's own Transition should call this (not read its approval-
// status column alone) to decide whether CheckTransitionGate applies.
func ActiveApproverCount(ctx context.Context, q workflow.Querier, approverTable string, recordTypeID, statusID int) (int, error) {
	return activeApproverCount(ctx, q, approverTable, recordTypeID, statusID)
}

func activeApproverCount(ctx context.Context, q workflow.Querier, approverTable string, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`, approverTable),
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active approvers in %s: %w", approverTable, err)
	}
	return n, nil
}

func signOffCount(ctx context.Context, q workflow.Querier, approvalTable, idColumn string, internalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE %s = $1 AND record_status_id = $2`, approvalTable, idColumn),
		internalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sign-offs in %s: %w", approvalTable, err)
	}
	return n, nil
}

func isConfiguredApprover(ctx context.Context, q workflow.Querier, approverTable string, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM %s WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`, approverTable),
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check approver in %s: %w", approverTable, err)
	}
	return exists, nil
}

func statusCodeByID(ctx context.Context, q workflow.Querier, statusID int) (string, error) {
	var code string
	if err := q.QueryRow(ctx, `SELECT record_status_code FROM lkp_record_status WHERE record_status_id = $1`, statusID).Scan(&code); err != nil {
		return "", fmt.Errorf("resolve status id %d: %w", statusID, err)
	}
	return code, nil
}

// writeGenericHistory records one <table>_history row inside the caller's
// transaction, matching the shape every relational module's history table
// shares (see estimate_history / writeHistory in estimate/store_create.go).
func writeGenericHistory(ctx context.Context, tx pgx.Tx, rec RecordSpec, internalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s (%s, from_status_id, to_status_id, action, actor_employee_id) VALUES ($1,$2,$3,$4,$5)`,
		rec.HistoryTable, rec.IDColumn,
	), internalID, fromStatusID, toStatusID, action, nullIntOrNil(actorEmployeeID))
}
