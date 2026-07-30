//go:build dbtest

package journal

import (
	"context"
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
