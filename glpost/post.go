package glpost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"stonesuite-backend/journal"
)

// centsPerUnit converts a money amount to integer cents.
const centsPerUnit = 100

// Cents converts a stored money amount (DECIMAL(15,2) scanned as float64) to
// integer cents, rounding so a value like 12.340000000000002 cannot reach the
// journal, which refuses sub-cent input.
func Cents(amount float64) int64 { return int64(math.Round(amount * centsPerUnit)) }

// querier is the subset of pgx a posting needs; a pgx.Tx satisfies it. Every
// function here must run on the caller's own in-flight transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// UserError is a posting failure the person doing the work can fix (an unmapped
// default account, a closed period). Document modules turn it into their own
// 4xx client error so the status change is refused with a readable reason
// instead of a 500.
type UserError struct{ Msg string }

// Error returns the caller-facing message.
func (e UserError) Error() string { return e.Msg }

// AsUserError reports whether err is (or wraps) a UserError.
func AsUserError(err error) (UserError, bool) {
	var ue UserError
	if errors.As(err, &ue) {
		return ue, true
	}
	return UserError{}, false
}

// Posting is one ledger entry a document should have.
type Posting struct {
	SourceType string // one of the Source* constants
	SourceID   string // the document's UUID
	Date       time.Time
	Memo       string
	Actor      int // employee id; 0 = unknown
	Legs       []Leg
}

// PostOnce writes p as a journal entry unless the document already has a live
// (non-reversal) entry for p.SourceType, or p has nothing to post. It reports
// whether a new entry was written.
//
// The caller MUST hold a lock on the document row (every status-changing path
// does: FOR UPDATE in Transition, in the approval engine), which is what makes
// the exists-then-insert below safe against a concurrent second post.
func PostOnce(ctx context.Context, q querier, p Posting) (bool, error) {
	legs, err := activeLegs(p.Legs)
	if err != nil {
		return false, err
	}
	if len(legs) < 2 {
		return false, nil // a zero-value document has nothing to put on the books
	}
	exists, err := HasEntry(ctx, q, p.SourceType, p.SourceID)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	lines, err := resolveLines(ctx, q, legs)
	if err != nil {
		return false, err
	}
	if _, err := journal.CreateEntry(ctx, q, journal.CreateEntryInput{
		EntryDate: p.Date, Memo: p.Memo, SourceType: p.SourceType, SourceID: p.SourceID,
		CreatedBy: p.Actor, Lines: lines,
	}); err != nil {
		return false, translate(err, p.Date)
	}
	return true, nil
}

// ReverseIfPosted reverses the document's live entry for sourceType, if it has
// one that has not been reversed already. It reports whether a reversal was
// written. A document with no entry (approved before posting existed, or never
// posted) is left alone, which is not an error.
func ReverseIfPosted(ctx context.Context, q querier, sourceType, sourceID string, date time.Time, memo string, actor int) (bool, error) {
	var originalID int
	err := q.QueryRow(ctx, `
		SELECT je.journal_entry_id FROM journal_entry je
		 WHERE je.source_type = $1 AND je.source_id = $2::uuid AND NOT je.is_reversal
		   AND NOT EXISTS (SELECT 1 FROM journal_entry r WHERE r.reverses_journal_entry_id = je.journal_entry_id)
		 ORDER BY je.journal_entry_id LIMIT 1`, sourceType, sourceID).Scan(&originalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find ledger entry to reverse for %s %s: %w", sourceType, sourceID, err)
	}
	if _, err := journal.ReverseEntry(ctx, q, originalID, date, memo, actor); err != nil {
		return false, translate(err, date)
	}
	return true, nil
}

// activeLegs drops zero legs and rejects a malformed one. A negative leg means
// the document's stored totals are inconsistent (e.g. tax larger than the whole
// bill), which is a data problem to surface, not to post around.
func activeLegs(legs []Leg) ([]Leg, error) {
	out := make([]Leg, 0, len(legs))
	var dr, cr int64
	for _, l := range legs {
		if l.DebitCents < 0 || l.CreditCents < 0 || (l.DebitCents > 0 && l.CreditCents > 0) {
			return nil, UserError{Msg: "This document's totals are inconsistent, so it cannot be posted to the ledger. Check its tax, discount and total."}
		}
		if l.DebitCents == 0 && l.CreditCents == 0 {
			continue
		}
		dr += l.DebitCents
		cr += l.CreditCents
		out = append(out, l)
	}
	if dr != cr {
		return nil, fmt.Errorf("glpost: legs are not balanced (debit %d, credit %d cents)", dr, cr)
	}
	return out, nil
}

// resolveLines maps each leg's slot to its mapped account. Every slot a leg
// needs must point at an account, or the whole posting is refused (never a
// partial entry) naming the slot to set.
func resolveLines(ctx context.Context, q querier, legs []Leg) ([]journal.LineInput, error) {
	slots := make([]string, 0, len(legs))
	for _, l := range legs {
		slots = append(slots, l.Slot)
	}
	rows, err := q.Query(ctx,
		`SELECT slot_key, coa_account_id FROM coa_default_mapping WHERE slot_key = ANY($1)`, slots)
	if err != nil {
		return nil, fmt.Errorf("load default accounts: %w", err)
	}
	accounts := map[string]int{}
	for rows.Next() {
		var key string
		var id *int
		if err := rows.Scan(&key, &id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan default account: %w", err)
		}
		if id != nil {
			accounts[key] = *id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load default accounts: %w", err)
	}
	lines := make([]journal.LineInput, 0, len(legs))
	for _, l := range legs {
		id, ok := accounts[l.Slot]
		if !ok {
			return nil, UserError{Msg: fmt.Sprintf(
				"The default %s account is not set. Choose it under Chart of Accounts > Default Accounts, then try again.",
				slotLabels[l.Slot])}
		}
		lines = append(lines, journal.LineInput{
			AccountID: id,
			Debit:     float64(l.DebitCents) / centsPerUnit,
			Credit:    float64(l.CreditCents) / centsPerUnit,
		})
	}
	return lines, nil
}

// translate turns the journal's caller-fault errors into a UserError.
func translate(err error, date time.Time) error {
	switch {
	case errors.Is(err, journal.ErrPeriodClosed):
		return UserError{Msg: fmt.Sprintf(
			"The accounting period for %s is closed, so this cannot be posted to the ledger. Reopen the period or change the document's date.",
			date.Format("Jan 2, 2006"))}
	case errors.Is(err, journal.ErrNoAccountingPeriod):
		return UserError{Msg: fmt.Sprintf(
			"No accounting period covers %s, so this cannot be posted to the ledger. Generate the fiscal year first.",
			date.Format("Jan 2, 2006"))}
	}
	var ce journal.ClientError
	if errors.As(err, &ce) {
		return UserError{Msg: ce.Msg}
	}
	return fmt.Errorf("post to ledger: %w", err)
}

// HasEntry reports whether the document has a live (non-reversal) entry of
// sourceType, reversed or not.
func HasEntry(ctx context.Context, q querier, sourceType, sourceID string) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM journal_entry
		                WHERE source_type = $1 AND source_id = $2::uuid AND NOT is_reversal)`,
		sourceType, sourceID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check ledger entry for %s %s: %w", sourceType, sourceID, err)
	}
	return exists, nil
}

// Today is the calendar date a reversal is dated, at UTC midnight. A void is
// recorded on the day it happens, never back in the original (possibly closed)
// period.
func Today() time.Time {
	y, m, d := time.Now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
