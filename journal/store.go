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

// IsPeriodClosed reports whether effectiveDate falls within the tenant's
// closed accounting period (spec AD-4: a single "books closed through" date).
// A nil books_closed_through means nothing is closed.
func IsPeriodClosed(ctx context.Context, q querier, effectiveDate time.Time) (bool, error) {
	var closedThrough *time.Time
	if err := q.QueryRow(ctx,
		`SELECT books_closed_through FROM accounting_settings WHERE accounting_settings_id = 1`,
	).Scan(&closedThrough); err != nil {
		return false, fmt.Errorf("load accounting settings: %w", err)
	}
	if closedThrough == nil {
		return false, nil
	}
	return isClosed(effectiveDate, *closedThrough), nil
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
