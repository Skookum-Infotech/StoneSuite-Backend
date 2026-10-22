package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/approvalchain"
)

const (
	reasonNotPending       = "not_pending"
	reasonRecipientChanged = "recipient_changed"
	// approvalMinAgeDays / approvalMaxAgeDays bound how long (tenant-local calendar
	// days) a record may have sat at its gate to be a candidate: the first
	// reminder is due at 2 days, and the largest seeded offset (6) plus
	// maxStageAgeDays is the oldest a stage can still be sent.
	approvalMinAgeDays = 2
	approvalMaxAgeDays = 8
)

// ApprovalRecord is one record waiting at an approval gate.
// Entered is the tenant-local calendar date (midnight, tenant zone) it reached the gate.
type ApprovalRecord struct {
	UUID            string
	Number          string
	OwnerEmail      string
	OwnerEmployeeID int
	InternalID      int
	Entered         time.Time
}

// approvalSelectSQL builds the shared projection for one spec. Registry names are
// interpolated only after spec.ident(); every value is a $n parameter ($1 type code,
// $2 gate status code). The entry time ignores self-transition history rows
// (from = to, e.g. a partial sign-off or an edit), which don't restart the clock. Columns: uuid, number, owner email, owner employee id,
// internal id, entry timestamp (UTC wall clock), deleted, approval status,
// status code, has configured approvers.
func (s approvalSpec) approvalSelectSQL() string {
	r := s.Cfg.Record
	return strings.NewReplacer(
		"{t}", r.Table, "{h}", r.HistoryTable, "{id}", r.IDColumn, "{uuid}", r.UUIDColumn,
		"{st}", r.StatusColumn, "{ap}", r.ApprovalStatusColumn, "{del}", r.DeletedAtColumn,
		"{cr}", r.CreatedAtColumn, "{own}", r.OwnerColumn, "{num}", r.NumberColumn,
		"{apr}", s.Cfg.ApproverTable, "{active}", userStatusActive,
	).Replace(`
SELECT d.{uuid}::text, COALESCE(d.{num}, ''), COALESCE(u.email, ''), COALESCE(d.{own}, 0), d.{id},
       COALESCE((SELECT MAX(h.at) FROM {h} h WHERE h.{id} = d.{id} AND h.to_status_id = d.{st}
         AND h.from_status_id IS DISTINCT FROM h.to_status_id), d.{cr}),
       (d.{del} IS NOT NULL), d.{ap}, rs.record_status_code,
       EXISTS (SELECT 1 FROM {apr} ea WHERE ea.record_type_id = rt.record_type_id
                 AND ea.record_status_id = rs.record_status_id AND ea.is_active)
FROM {t} d
JOIN lkp_record_status rs ON rs.record_status_id = d.{st}
JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type AND rt.record_type_code = $1
LEFT JOIN employee e ON e.employee_id = d.{own} AND e.employee_deleted_at IS NULL
LEFT JOIN users u ON u.id = e.employee_user_id AND u.status = '{active}'
`)
}

// approvalRow is the raw scan of approvalSelectSQL.
type approvalRow struct {
	rec         ApprovalRecord
	entered     time.Time
	deleted     bool
	approval    string
	status      string
	hasApprover bool
}

func scanApprovalRow(row pgx.Row) (approvalRow, error) {
	var a approvalRow
	err := row.Scan(&a.rec.UUID, &a.rec.Number, &a.rec.OwnerEmail, &a.rec.OwnerEmployeeID, &a.rec.InternalID,
		&a.entered, &a.deleted, &a.approval, &a.status, &a.hasApprover)
	return a, err
}

// approvalAnchor interprets a TIMESTAMP (UTC wall clock) as UTC and returns its tenant-local date.
func approvalAnchor(ts time.Time, loc *time.Location) time.Time {
	utc := time.Date(ts.Year(), ts.Month(), ts.Day(), ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(), time.UTC)
	return localDate(utc, loc)
}

// listApprovalCandidates returns live, non-rejected records sitting at spec's gate
// whose tenant-local entry date is within [today-8d, today-2d] and whose gate has
// at least one configured active approver (an ungated record is never reminded).
func listApprovalCandidates(ctx context.Context, q Querier, spec approvalSpec, today time.Time, loc *time.Location) ([]ApprovalRecord, error) {
	if err := spec.ident(); err != nil {
		return nil, err
	}
	r := spec.Cfg.Record
	where := strings.NewReplacer("{del}", r.DeletedAtColumn, "{ap}", r.ApprovalStatusColumn).Replace(`
WHERE rs.record_status_code = $2
  AND d.{del} IS NULL
  AND d.{ap} <> $3
  AND EXISTS (SELECT 1 FROM ` + spec.Cfg.ApproverTable + ` ea WHERE ea.record_type_id = rt.record_type_id
              AND ea.record_status_id = rs.record_status_id AND ea.is_active)
ORDER BY d.{id}`)
	where = strings.ReplaceAll(where, "{id}", r.IDColumn)
	rows, err := q.Query(ctx, spec.approvalSelectSQL()+where, spec.RecordTypeCode, spec.Gate.StatusCode, approvalchain.StatusRejected)
	if err != nil {
		return nil, fmt.Errorf("list %s approval candidates: %w", spec.Workflow, err)
	}
	defer rows.Close()
	oldest := today.AddDate(0, 0, -approvalMaxAgeDays)
	newest := today.AddDate(0, 0, -approvalMinAgeDays)
	var out []ApprovalRecord
	for rows.Next() {
		a, err := scanApprovalRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s approval candidate: %w", spec.Workflow, err)
		}
		a.rec.Entered = approvalAnchor(a.entered, loc)
		if a.rec.Entered.Before(oldest) || a.rec.Entered.After(newest) {
			continue
		}
		out = append(out, a.rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s approval candidates: %w", spec.Workflow, err)
	}
	return out, nil
}

// approvalState re-checks one record live at send time. reason "" means the
// planned reminder is still valid; precedence: voided, not_pending,
// anchor_changed, recipient_changed. rcpt is the live recipient row for recipientUserID.
func approvalState(ctx context.Context, q Querier, spec approvalSpec, uuid string, plannedAnchor time.Time,
	recipientUserID string, loc *time.Location) (reason string, rec ApprovalRecord, rcpt approvalRecipient, err error) {
	if err := spec.ident(); err != nil {
		return "", ApprovalRecord{}, approvalRecipient{}, err
	}
	sql := spec.approvalSelectSQL() + ` WHERE d.` + spec.Cfg.Record.UUIDColumn + ` = $2::uuid`
	a, scanErr := scanApprovalRow(q.QueryRow(ctx, sql, spec.RecordTypeCode, uuid))
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return reasonVoided, ApprovalRecord{}, approvalRecipient{}, nil
	}
	if scanErr != nil {
		return "", ApprovalRecord{}, approvalRecipient{}, fmt.Errorf("%s approval state %s: %w", spec.Workflow, uuid, scanErr)
	}
	rec = a.rec
	rec.Entered = approvalAnchor(a.entered, loc)
	switch {
	case a.deleted:
		return reasonVoided, rec, approvalRecipient{}, nil
	case a.status != spec.Gate.StatusCode || a.approval == approvalchain.StatusRejected || !a.hasApprover:
		return reasonNotPending, rec, approvalRecipient{}, nil
	case !sameDay(rec.Entered, plannedAnchor):
		return reasonAnchorChanged, rec, approvalRecipient{}, nil
	}
	recs, err := approvalRecipients(ctx, q, spec, rec)
	if err != nil {
		return "", rec, approvalRecipient{}, err
	}
	for _, r := range recs {
		if r.UserID == recipientUserID {
			return "", rec, r, nil
		}
	}
	return reasonRecipientChanged, rec, approvalRecipient{}, nil
}
