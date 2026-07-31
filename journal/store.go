package journal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is the subset of pgx behavior journal needs (consumer-side
// interface, structurally identical to workflow.Querier but declared locally
// so this package imports zero stonesuite-backend/* app packages — the same
// discipline query/ and ai/ follow). A *pgxpool.Pool or a pgx.Tx both satisfy
// it. Every exported function in this package takes one of these and expects
// it to be the caller's own in-flight pgx.Tx, so the journal write commits
// atomically with whatever header row the caller is posting on behalf of.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrUnbalancedEntry is returned when a proposed entry's debits and credits
// do not sum to the same amount.
var ErrUnbalancedEntry = errors.New("journal entry is not balanced")

// ErrInvalidLine is returned when fewer than two lines are given, or a line
// does not have exactly one of debit/credit populated and positive.
var ErrInvalidLine = errors.New("invalid journal entry line")

// ErrPeriodClosed is returned when an entry's effective date falls in a closed
// accounting period — either an accounting_period row marked closed, or (for
// tenants with no fiscal calendar) a date at or before books_closed_through.
var ErrPeriodClosed = errors.New("accounting period is closed for this date")

// ErrNoAccountingPeriod is returned when a tenant HAS a fiscal calendar but no
// period covers the entry's effective date — typically posting past the last
// generated fiscal year. Distinct from ErrPeriodClosed because the remedy is
// different: generate the next fiscal year, not reopen a period.
var ErrNoAccountingPeriod = errors.New("no accounting period exists for this date")

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// validateLines enforces double-entry shape: at least two lines, each with
// exactly one side populated and positive, summing to the same total debit
// and total credit.
func validateLines(lines []LineInput) error {
	if len(lines) < 2 {
		return ErrInvalidLine
	}
	var totalDebit, totalCredit float64
	for _, l := range lines {
		if l.Debit < 0 || l.Credit < 0 {
			return ErrInvalidLine
		}
		if (l.Debit > 0) == (l.Credit > 0) {
			return ErrInvalidLine // both zero, or both positive
		}
		totalDebit += l.Debit
		totalCredit += l.Credit
	}
	if round2(totalDebit) != round2(totalCredit) {
		return ErrUnbalancedEntry
	}
	return nil
}

// lookupPeriod reads, in one round trip, everything verdictFor needs: the
// status of the accounting_period covering effectiveDate (if any), whether the
// tenant has a fiscal calendar at all, and the two accounting_settings dates.
//
// The period table is queried with raw SQL rather than through the
// accountingperiod package on purpose: journal imports zero stonesuite-backend
// packages, the same discipline query/ and ai/ follow, and it already reads
// accounting_settings the same way.
func lookupPeriod(ctx context.Context, q querier, effectiveDate time.Time) (periodLookup, error) {
	var (
		status *string
		l      periodLookup
	)
	err := q.QueryRow(ctx, `
		SELECT (SELECT ap.accounting_period_status
		          FROM accounting_period ap
		         WHERE $1::date BETWEEN ap.period_start AND ap.period_end),
		       EXISTS (SELECT 1 FROM accounting_period),
		       s.base_period_start,
		       s.books_closed_through
		FROM accounting_settings s
		WHERE s.accounting_settings_id = 1`, effectiveDate,
	).Scan(&status, &l.CalendarExists, &l.BasePeriodStart, &l.ClosedThrough)
	if err != nil {
		return periodLookup{}, fmt.Errorf("load accounting period for %s: %w",
			effectiveDate.Format(time.DateOnly), err)
	}
	if status != nil {
		l.Found, l.Status = true, *status
	}
	return l, nil
}

// CheckPeriodOpen returns nil when effectiveDate may be posted to, and
// ErrPeriodClosed or ErrNoAccountingPeriod when it may not.
//
// This is the single choke point for period enforcement: CreateEntry calls it
// on every GL write, so a posting module cannot forget the check. Modules may
// still call it early — before doing expensive work or mutating a header — to
// fail with a friendlier, document-specific message; cashtransfer does.
func CheckPeriodOpen(ctx context.Context, q querier, effectiveDate time.Time) error {
	l, err := lookupPeriod(ctx, q, effectiveDate)
	if err != nil {
		return err
	}
	switch verdictFor(effectiveDate, l) {
	case verdictClosed:
		return ErrPeriodClosed
	case verdictNoPeriod:
		return ErrNoAccountingPeriod
	default:
		return nil
	}
}

// IsPeriodClosed reports whether effectiveDate falls within a closed
// accounting period.
//
// Retained for backward compatibility with callers written against the
// original single "books closed through" date (spec AD-4). It now resolves
// through the full period rules, so a missing period reads as closed — a
// caller asking a yes/no question gets the fail-closed answer. Prefer
// CheckPeriodOpen, which distinguishes the two cases.
func IsPeriodClosed(ctx context.Context, q querier, effectiveDate time.Time) (bool, error) {
	l, err := lookupPeriod(ctx, q, effectiveDate)
	if err != nil {
		return false, err
	}
	return verdictFor(effectiveDate, l) != verdictOpen, nil
}

// CreateEntry inserts a balanced journal entry and applies each line's signed
// effect (debit positive, credit negative) to coa_account.coa_account_balance
// — all against the caller-supplied querier, which MUST be an in-flight
// pgx.Tx so this write commits atomically with the caller's own header update
// (spec AD-1, AD-3).
func CreateEntry(ctx context.Context, q querier, in CreateEntryInput) (*JournalEntry, error) {
	if err := validateLines(in.Lines); err != nil {
		return nil, err
	}

	// The period guard lives here, at the choke point, rather than in each
	// posting module: every GL write goes through CreateEntry (ReverseEntry
	// included), so a module cannot post into a closed period by forgetting to
	// check. Callers that check earlier for a better message still land here.
	if err := CheckPeriodOpen(ctx, q, in.EntryDate); err != nil {
		return nil, err
	}

	var newID int
	var newUUID string
	err := q.QueryRow(ctx, `
		INSERT INTO journal_entry (
			entry_date, memo, source_type, source_id, is_reversal,
			reverses_journal_entry_id, journal_entry_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING journal_entry_id, journal_entry_uuid`,
		in.EntryDate, in.Memo, in.SourceType, in.SourceID, in.IsReversal,
		nullableInt(in.ReversesJournalEntryID), nullableInt(in.CreatedBy),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert journal entry: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := q.Exec(ctx,
		`UPDATE journal_entry SET journal_entry_number = $1 WHERE journal_entry_id = $2`,
		number, newID); err != nil {
		return nil, fmt.Errorf("set journal entry number: %w", err)
	}

	out := &JournalEntry{
		InternalID: newID, UUID: newUUID, Number: number,
		EntryDate: in.EntryDate, Memo: in.Memo,
		SourceType: in.SourceType, SourceID: in.SourceID, IsReversal: in.IsReversal,
	}
	for i, l := range in.Lines {
		lineNumber := i + 1
		if _, err := q.Exec(ctx, `
			INSERT INTO journal_entry_line (journal_entry_id, line_number, coa_account_id, debit, credit)
			VALUES ($1,$2,$3,$4,$5)`,
			newID, lineNumber, l.AccountID, l.Debit, l.Credit); err != nil {
			return nil, fmt.Errorf("insert journal entry line %d: %w", lineNumber, err)
		}
		delta := l.Debit - l.Credit
		if _, err := q.Exec(ctx, `
			UPDATE coa_account SET coa_account_balance = coa_account_balance + $2
			WHERE coa_account_id = $1`, l.AccountID, delta); err != nil {
			return nil, fmt.Errorf("update account balance for line %d: %w", lineNumber, err)
		}
		out.Lines = append(out.Lines, JournalEntryLine{
			LineNumber: lineNumber, AccountID: l.AccountID, Debit: l.Debit, Credit: l.Credit,
		})
	}
	return out, nil
}
