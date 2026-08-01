//go:build dbtest

package journal

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedTwoAccounts creates two live, postable, active bank/cash coa_account
// rows for the test to post between, returning their internal ids. It writes
// through the same tx the test already opened (not the pool directly), so
// the accounts it creates are rolled back with everything else the test did
// -- without this, every test in this file would durably insert the same
// hardcoded account codes and the second test to run would collide on
// uq_coa_account_code_live.
func seedTwoAccounts(t *testing.T, tx pgx.Tx) (fromID, toID int) {
	t.Helper()
	ctx := context.Background()
	// Reuses the seeded reference tree (chartofaccounts spec §3.1/3.2): every
	// tenant already has subcategory 1100 (Current Assets) seeded.
	var subcatID int
	if err := tx.QueryRow(ctx,
		`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = 1100`,
	).Scan(&subcatID); err != nil {
		t.Fatalf("load Current Assets subcategory: %v", err)
	}
	insert := func(code, name string) int {
		var id int
		if err := tx.QueryRow(ctx, `
			INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
				coa_account_bs_pnl, coa_account_type, coa_account_is_postable, coa_account_is_active)
			VALUES ($1,$2,$3,'BS','bank',TRUE,TRUE)
			RETURNING coa_account_id`, code, name, subcatID).Scan(&id); err != nil {
			t.Fatalf("seed account %s: %v", code, err)
		}
		return id
	}
	return insert("9990", "Test Bank From"), insert("9991", "Test Bank To")
}

func TestCreateEntry_BalancedAndUpdatesBalances(t *testing.T) {
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
		Memo:       "test transfer",
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000001",
		Lines: []LineInput{
			{AccountID: toID, Debit: 500},
			{AccountID: fromID, Credit: 500},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if entry.Number == "" {
		t.Error("expected a journal entry number to be assigned")
	}

	var fromBalance, toBalance float64
	if err := tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, fromID).Scan(&fromBalance); err != nil {
		t.Fatalf("read from balance: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, toID).Scan(&toBalance); err != nil {
		t.Fatalf("read to balance: %v", err)
	}
	if fromBalance != -500 {
		t.Errorf("from account balance = %v, want -500", fromBalance)
	}
	if toBalance != 500 {
		t.Errorf("to account balance = %v, want 500", toBalance)
	}

	// Balance invariant: coa_account_balance == SUM(debit - credit) over
	// every journal_entry_line for that account.
	var summed float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(debit - credit), 0) FROM journal_entry_line WHERE coa_account_id = $1`,
		toID).Scan(&summed); err != nil {
		t.Fatalf("sum lines: %v", err)
	}
	if summed != toBalance {
		t.Errorf("balance invariant broken: coa_account_balance=%v, SUM(lines)=%v", toBalance, summed)
	}
}

func TestCreateEntry_RejectsUnbalanced(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	fromID, toID := seedTwoAccounts(t, tx)

	_, err = CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  time.Now(),
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000002",
		Lines: []LineInput{
			{AccountID: toID, Debit: 500},
			{AccountID: fromID, Credit: 499},
		},
	})
	if err != ErrUnbalancedEntry {
		t.Errorf("CreateEntry() error = %v, want ErrUnbalancedEntry", err)
	}
}

func TestReverseEntry_NetsBalanceToZero(t *testing.T) {
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
		SourceID:   "00000000-0000-0000-0000-000000000003",
		Lines: []LineInput{
			{AccountID: toID, Debit: 250},
			{AccountID: fromID, Credit: 250},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	if _, err := ReverseEntry(ctx, tx, entry.InternalID, time.Now(), "reversal", 0); err != nil {
		t.Fatalf("ReverseEntry: %v", err)
	}

	var fromBalance, toBalance float64
	tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, fromID).Scan(&fromBalance)
	tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, toID).Scan(&toBalance)
	if fromBalance != 0 {
		t.Errorf("from account balance after reversal = %v, want 0", fromBalance)
	}
	if toBalance != 0 {
		t.Errorf("to account balance after reversal = %v, want 0", toBalance)
	}
}

// TestCreateEntry_RejectsClosedPeriod proves the guard is at the choke point:
// CreateEntry itself refuses, so a posting module cannot reach the ledger by
// forgetting to check. Everything runs inside the test's own transaction, so
// the period rows are rolled back with it.
func TestCreateEntry_RejectsClosedPeriod(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	fromID, toID := seedTwoAccounts(t, tx)
	entryDate := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	seedPeriod(t, tx, "closed", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))

	_, err = CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  entryDate,
		Memo:       "into a closed period",
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000009",
		Lines: []LineInput{
			{AccountID: toID, Debit: 100},
			{AccountID: fromID, Credit: 100},
		},
	})
	if !errors.Is(err, ErrPeriodClosed) {
		t.Fatalf("CreateEntry into a closed period = %v, want ErrPeriodClosed", err)
	}

	// A date the calendar does not cover at all is a different failure, because
	// the remedy is different: generate the year, do not reopen a period.
	err = CheckPeriodOpen(ctx, tx, time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrNoAccountingPeriod) {
		t.Fatalf("CheckPeriodOpen past the calendar = %v, want ErrNoAccountingPeriod", err)
	}
}

// TestCheckPeriodOpen_FallsBackToBooksClosedThrough is the backward-
// compatibility guarantee: with no accounting_period rows, behaviour is
// exactly what it was before the accountingperiod module existed.
func TestCheckPeriodOpen_FallsBackToBooksClosedThrough(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE accounting_settings SET books_closed_through = $1 WHERE accounting_settings_id = 1`,
		time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("set books_closed_through: %v", err)
	}

	if err := CheckPeriodOpen(ctx, tx, time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrPeriodClosed) {
		t.Errorf("on the closed-through date = %v, want ErrPeriodClosed", err)
	}
	if err := CheckPeriodOpen(ctx, tx, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Errorf("the day after = %v, want nil", err)
	}
}

// seedPeriod inserts one accounting_period (and the fiscal_year it needs)
// through the caller's tx, so it is rolled back with the rest of the test.
//
// status is applied uniformly to accounting_period_status AND all three
// sub-ledger locks (ap_lock_status, ar_lock_status, gl_lock_status) — the
// same shape generateYear/applyStatus produce in production (see
// accountingperiod/store_generate.go, accountingperiod/store_status.go) — so
// every existing caller continues to test what it claims to now that the GL
// choke point reads gl_lock_status instead of the derived overall status.
func seedPeriod(t *testing.T, tx pgx.Tx, status string, start, end time.Time) {
	t.Helper()
	ctx := context.Background()
	var fyID int
	if err := tx.QueryRow(ctx, `
		INSERT INTO fiscal_year (fiscal_year_name, fiscal_year_start, fiscal_year_end)
		VALUES ($1,$2,$3) RETURNING fiscal_year_id`,
		"FYTEST", start, end).Scan(&fyID); err != nil {
		t.Fatalf("seed fiscal year: %v", err)
	}
	var closedAt any
	if status == "closed" {
		closedAt = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO accounting_period (fiscal_year_id, accounting_period_name,
			period_number, period_start, period_end, accounting_period_status,
			accounting_period_closed_at, ap_lock_status, ar_lock_status, gl_lock_status)
		VALUES ($1,$2,1,$3,$4,$5,$6,$5,$5,$5)`,
		fyID, "Test Period", start, end, status, closedAt); err != nil {
		t.Fatalf("seed accounting period: %v", err)
	}
}

// TestCreateEntry_GLLockAloneGatesEvenWhenPeriodStatusIsOpen proves the GL
// choke point reads gl_lock_status specifically, not the derived
// accounting_period_status: it seeds a period whose overall status (and
// AP/AR locks) are "open" but whose gl_lock_status alone is "closed", and
// expects the same rejection seedPeriod's "closed" case produces.
func TestCreateEntry_GLLockAloneGatesEvenWhenPeriodStatusIsOpen(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	fromID, toID := seedTwoAccounts(t, tx)
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	entryDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	var fyID int
	if err := tx.QueryRow(ctx, `
		INSERT INTO fiscal_year (fiscal_year_name, fiscal_year_start, fiscal_year_end)
		VALUES ($1,$2,$3) RETURNING fiscal_year_id`,
		"FYTEST", start, end).Scan(&fyID); err != nil {
		t.Fatalf("seed fiscal year: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO accounting_period (fiscal_year_id, accounting_period_name,
			period_number, period_start, period_end, accounting_period_status,
			ap_lock_status, ar_lock_status, gl_lock_status)
		VALUES ($1,$2,1,$3,$4,'open','open','open','closed')`,
		fyID, "Test Period", start, end); err != nil {
		t.Fatalf("seed accounting period with GL-only lock: %v", err)
	}

	if err := CheckPeriodOpen(ctx, tx, entryDate); !errors.Is(err, ErrPeriodClosed) {
		t.Fatalf("CheckPeriodOpen with gl_lock_status closed = %v, want ErrPeriodClosed", err)
	}

	_, err = CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  entryDate,
		Memo:       "GL locked while period status is open",
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000010",
		Lines: []LineInput{
			{AccountID: toID, Debit: 100},
			{AccountID: fromID, Credit: 100},
		},
	})
	if !errors.Is(err, ErrPeriodClosed) {
		t.Fatalf("CreateEntry with gl_lock_status closed = %v, want ErrPeriodClosed", err)
	}
}
