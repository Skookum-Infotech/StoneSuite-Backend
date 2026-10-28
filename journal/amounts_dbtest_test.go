//go:build dbtest

package journal

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCreateEntry_RejectsSubCentAmounts proves the shared create path refuses
// amounts that would balance only before NUMERIC(…,2) rounding.
func TestCreateEntry_RejectsSubCentAmounts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	fromID, toID := seedTwoAccounts(t, tx)

	// 0.004 + 0.004 + 0.004 vs 0.012 balances in float but each rounds to 0.00.
	_, err = CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  time.Now(),
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-0000000000a1",
		Lines: []LineInput{
			{AccountID: toID, Debit: 0.004},
			{AccountID: toID, Debit: 0.004},
			{AccountID: fromID, Credit: 0.008},
		},
	})
	if !errors.Is(err, ErrInvalidLine) {
		t.Fatalf("CreateEntry err = %v, want ErrInvalidLine", err)
	}
}

// TestReverseEntry_RefusesDoubleReversal proves a second reversal of the same
// entry is refused with a ClientError and posts nothing.
func TestReverseEntry_RefusesDoubleReversal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	fromID, toID := seedTwoAccounts(t, tx)

	entry, err := CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  time.Now(),
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-0000000000a2",
		Lines:      []LineInput{{AccountID: toID, Debit: 75}, {AccountID: fromID, Credit: 75}},
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if _, err := ReverseEntry(ctx, tx, entry.InternalID, time.Now(), "first", 0); err != nil {
		t.Fatalf("first ReverseEntry: %v", err)
	}
	_, err = ReverseEntry(ctx, tx, entry.InternalID, time.Now(), "second", 0)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("second ReverseEntry err = %v, want ClientError", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM journal_entry WHERE reverses_journal_entry_id = $1`,
		entry.InternalID).Scan(&n); err != nil {
		t.Fatalf("count reversals: %v", err)
	}
	if n != 1 {
		t.Errorf("reversal entries = %d, want 1", n)
	}
}
