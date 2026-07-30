//go:build dbtest

package cashtransfer

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

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

// seedAccount creates one live bank-type coa_account row and returns its uuid.
func seedAccount(t *testing.T, pool *pgxpool.Pool, code, name string, postable, active bool) string {
	t.Helper()
	ctx := context.Background()
	var subcatID int
	if err := pool.QueryRow(ctx,
		`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = 1100`,
	).Scan(&subcatID); err != nil {
		t.Fatalf("load Current Assets subcategory: %v", err)
	}
	var uuid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			coa_account_bs_pnl, coa_account_type, coa_account_is_postable, coa_account_is_active)
		VALUES ($1,$2,$3,'BS','bank',$4,$5)
		RETURNING coa_account_uuid`, code, name, subcatID, postable, active).Scan(&uuid); err != nil {
		t.Fatalf("seed account %s: %v", code, err)
	}
	return uuid
}

func TestCreate_Validations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9980", "From", true, true)
	to := seedAccount(t, pool, "9981", "To", true, true)
	inactive := seedAccount(t, pool, "9982", "Inactive", true, false)

	tests := []struct {
		name string
		in   CreateInput
	}{
		{"same account", CreateInput{FromAccountUUID: from, ToAccountUUID: from, Amount: 100}},
		{"zero amount", CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 0}},
		{"negative amount", CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: -5}},
		{"inactive destination", CreateInput{FromAccountUUID: from, ToAccountUUID: inactive, Amount: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Create(ctx, pool, tt.in, 0); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestPost_CreatesBalancedEntryAndUpdatesBalances(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9970", "PostFrom", true, true)
	to := seedAccount(t, pool, "9971", "PostTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 1000}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}
	posted, err := Post(ctx, pool, ct.ID, 0)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if posted.StatusCode != postedStatusCode {
		t.Errorf("status = %s, want %s", posted.StatusCode, postedStatusCode)
	}
	if posted.JournalEntryID == nil {
		t.Fatal("expected journalEntryId to be set after posting")
	}

	var fromBalance, toBalance float64
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, from).Scan(&fromBalance)
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, to).Scan(&toBalance)
	if fromBalance != -1000 || toBalance != 1000 {
		t.Errorf("balances after post: from=%v to=%v, want from=-1000 to=1000", fromBalance, toBalance)
	}

	// Re-posting must fail.
	if _, err := Post(ctx, pool, ct.ID, 0); !errors.Is(err, ErrAlreadyPosted) {
		t.Errorf("second Post() error = %v, want ErrAlreadyPosted", err)
	}

	// Editing a posted transfer must fail.
	if _, err := Update(ctx, pool, ct.ID, UpdateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 1}, 0); err == nil {
		t.Error("expected Update on a posted transfer to fail")
	}
}

func TestPost_ConcurrentDoublePost(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9960", "RaceFrom", true, true)
	to := seedAccount(t, pool, "9961", "RaceTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 250}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = Post(ctx, pool, ct.ID, 0)
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 successful Post out of 2 concurrent attempts, got %d", successes)
	}
}

func TestReverse_OnlyValidWhenPosted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9950", "RevFrom", true, true)
	to := seedAccount(t, pool, "9951", "RevTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 300}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := Reverse(ctx, pool, ct.ID, 0); !errors.Is(err, ErrNotPosted) {
		t.Errorf("Reverse on a draft = %v, want ErrNotPosted", err)
	}

	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}
	if _, err := Post(ctx, pool, ct.ID, 0); err != nil {
		t.Fatalf("Post: %v", err)
	}
	reversed, err := Reverse(ctx, pool, ct.ID, 0)
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if reversed.StatusCode != reversedStatusCode {
		t.Errorf("status = %s, want %s", reversed.StatusCode, reversedStatusCode)
	}
	if reversed.ReversalJournalEntryID == nil {
		t.Fatal("expected reversalJournalEntryId to be set")
	}

	var fromBalance, toBalance float64
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, from).Scan(&fromBalance)
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, to).Scan(&toBalance)
	if fromBalance != 0 || toBalance != 0 {
		t.Errorf("balances after reverse: from=%v to=%v, want 0 and 0", fromBalance, toBalance)
	}

	// Reversing twice must fail (status is now RVSD, not POST).
	if _, err := Reverse(ctx, pool, ct.ID, 0); !errors.Is(err, ErrNotPosted) {
		t.Errorf("second Reverse() error = %v, want ErrNotPosted", err)
	}
}

func TestUpdate_RejectsStaleRecordVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9930", "VerFrom", true, true)
	to := seedAccount(t, pool, "9931", "VerTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 50}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ct.RecordVersion != 1 {
		t.Fatalf("new record version = %d, want 1", ct.RecordVersion)
	}

	// A stale version is rejected with ErrVersionMismatch (409), not applied.
	if _, err := Update(ctx, pool, ct.ID, UpdateInput{
		FromAccountUUID: from, ToAccountUUID: to, Amount: 60, RecordVersion: 99,
	}, 0); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("Update() with stale version error = %v, want ErrVersionMismatch", err)
	}

	// The version last read succeeds and advances the counter.
	updated, err := Update(ctx, pool, ct.ID, UpdateInput{
		FromAccountUUID: from, ToAccountUUID: to, Amount: 60, RecordVersion: ct.RecordVersion,
	}, 0)
	if err != nil {
		t.Fatalf("Update() with correct version: %v", err)
	}
	if updated.RecordVersion != ct.RecordVersion+1 {
		t.Errorf("record version after update = %d, want %d", updated.RecordVersion, ct.RecordVersion+1)
	}

	// Omitting the version (0) opts out of the check entirely.
	if _, err := Update(ctx, pool, ct.ID, UpdateInput{
		FromAccountUUID: from, ToAccountUUID: to, Amount: 70,
	}, 0); err != nil {
		t.Errorf("Update() with omitted version = %v, want nil", err)
	}
}

func TestPost_RejectsWhenPeriodClosed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9940", "ClosedFrom", true, true)
	to := seedAccount(t, pool, "9941", "ClosedTo", true, true)

	// Close the books through tomorrow, so today's transfer date is closed.
	closedThrough := time.Now().Add(24 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE accounting_settings SET books_closed_through = $1 WHERE accounting_settings_id = 1`,
		closedThrough); err != nil {
		t.Fatalf("set books_closed_through: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `UPDATE accounting_settings SET books_closed_through = NULL WHERE accounting_settings_id = 1`)
	})

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 100}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}
	if _, err := Post(ctx, pool, ct.ID, 0); !errors.Is(err, ErrPeriodClosed) {
		t.Errorf("Post() error = %v, want ErrPeriodClosed", err)
	}
}
