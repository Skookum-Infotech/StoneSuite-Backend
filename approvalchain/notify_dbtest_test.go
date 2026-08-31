//go:build dbtest

package approvalchain

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func notifyTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedCreditMemoWithOwner seeds a minimal customer + credit memo, owned by a
// real user-linked employee, against the real credit_memo table -- one of
// the four registry entries notify.go's Notify* hooks are actually wired
// for (see TestRegistry_ApprovalNotificationScope) -- so
// activeApproverContacts/remainingApproverContacts/ownerContact/fetchNumber
// are exercised against real SQL, not a mock. credit_memo is picked over
// invoice/payment/refund only because its header has the fewest required
// columns to seed by hand.
func seedCreditMemoWithOwner(t *testing.T, pool *pgxpool.Pool) (internalID int, recordTypeID, draftStatusID int, uuid, number, ownerUserID, ownerEmail string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID, custID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_id`, custTypeID, "Notify Test Customer "+suffix).Scan(&custID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	ownerEmail = "notify-owner-" + suffix + "@example.test"
	var ownerUserIDVal string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, 'Notify Test Owner', 'active') RETURNING id`,
		ownerEmail).Scan(&ownerUserIDVal); err != nil {
		t.Fatalf("seed owner user: %v", err)
	}
	var ownerEmployeeID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1, 'Notify', 'Owner', $2, 1) RETURNING employee_id`,
		ownerUserIDVal, ownerEmail).Scan(&ownerEmployeeID); err != nil {
		t.Fatalf("seed owner employee: %v", err)
	}

	cfg, ok := ForWorkflowKey("credit_memo")
	if !ok {
		t.Fatal("credit_memo must be registered")
	}
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, cfg.RecordTypeCode).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve CRDT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, recordTypeID).Scan(&draftStatusID); err != nil {
		t.Fatalf("resolve DRFT status: %v", err)
	}

	number = "CRDT-NOTIFY-" + suffix
	if err := pool.QueryRow(ctx, `
		INSERT INTO credit_memo (record_type, credit_memo_status, credit_memo_customer_id, credit_memo_number, credit_memo_owner_id, credit_memo_created_by)
		VALUES ($1, $2, $3, $4, $5, 1) RETURNING credit_memo_id, credit_memo_uuid`,
		recordTypeID, draftStatusID, custID, number, ownerEmployeeID).Scan(&internalID, &uuid); err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}
	return internalID, recordTypeID, draftStatusID, uuid, number, ownerUserIDVal, ownerEmail
}

func TestFetchNumber(t *testing.T) {
	pool := notifyTestPool(t)
	internalID, _, _, _, number, _, _ := seedCreditMemoWithOwner(t, pool)

	got, err := fetchNumber(context.Background(), pool, "credit_memo", "credit_memo_id", "credit_memo_number", internalID)
	if err != nil {
		t.Fatalf("fetchNumber: %v", err)
	}
	if got != number {
		t.Errorf("fetchNumber = %q, want %q", got, number)
	}
}

func TestOwnerContact_ResolvesRealOwner(t *testing.T) {
	pool := notifyTestPool(t)
	internalID, _, _, _, _, ownerUserID, ownerEmail := seedCreditMemoWithOwner(t, pool)

	c, ok, err := ownerContact(context.Background(), pool, "credit_memo", "credit_memo_id", "credit_memo_owner_id", internalID)
	if err != nil {
		t.Fatalf("ownerContact: %v", err)
	}
	if !ok {
		t.Fatal("ownerContact ok = false, want true")
	}
	if c.UserID != ownerUserID || c.Email != ownerEmail {
		t.Errorf("ownerContact = %+v, want {UserID:%s Email:%s}", c, ownerUserID, ownerEmail)
	}
}

func TestOwnerContact_NoOwnerSet(t *testing.T) {
	pool := notifyTestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID, custID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_id`, custTypeID, "No Owner Customer "+suffix).Scan(&custID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	cfg, _ := ForWorkflowKey("credit_memo")
	var recordTypeID, draftStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, cfg.RecordTypeCode).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve CRDT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, recordTypeID).Scan(&draftStatusID); err != nil {
		t.Fatalf("resolve DRFT status: %v", err)
	}
	var internalID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO credit_memo (record_type, credit_memo_status, credit_memo_customer_id, credit_memo_number, credit_memo_created_by)
		VALUES ($1, $2, $3, $4, 1) RETURNING credit_memo_id`,
		recordTypeID, draftStatusID, custID, "CRDT-NOOWNER-"+suffix).Scan(&internalID); err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}

	_, ok, err := ownerContact(ctx, pool, "credit_memo", "credit_memo_id", "credit_memo_owner_id", internalID)
	if err != nil {
		t.Fatalf("ownerContact: %v", err)
	}
	if ok {
		t.Error("ownerContact ok = true for a record with no owner set, want false")
	}
}

// TestApproverContacts_RequestedThenRemaining exercises
// activeApproverContacts and remainingApproverContacts together against a
// real approver + sign-off, mirroring exactly what NotifyApprovalRequested
// (before any sign-off) and NotifyRemainingApprovers (after one sign-off,
// quorum not met) each resolve.
func TestApproverContacts_RequestedThenRemaining(t *testing.T) {
	pool := notifyTestPool(t)
	ctx := context.Background()
	internalID, recordTypeID, draftStatusID, _, _, _, _ := seedCreditMemoWithOwner(t, pool)
	cfg, _ := ForWorkflowKey("credit_memo")
	empIDStr := seedEmployeeWithUser(t, pool)

	if _, err := ReplaceApprovers(ctx, pool, cfg, "DRFT", []string{empIDStr}, 0); err != nil {
		t.Fatalf("ReplaceApprovers: %v", err)
	}

	requested, err := activeApproverContacts(ctx, pool, cfg.ApproverTable, recordTypeID, draftStatusID)
	if err != nil {
		t.Fatalf("activeApproverContacts: %v", err)
	}
	if len(requested) != 1 {
		t.Fatalf("len(requested) = %d, want 1", len(requested))
	}

	remaining, err := remainingApproverContacts(ctx, pool, cfg.ApproverTable, cfg.ApprovalTable, cfg.Record.IDColumn, recordTypeID, draftStatusID, internalID)
	if err != nil {
		t.Fatalf("remainingApproverContacts before sign-off: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("len(remaining) before sign-off = %d, want 1", len(remaining))
	}

	empID, err := strconv.Atoi(empIDStr)
	if err != nil {
		t.Fatalf("parse employee id %q: %v", empIDStr, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credit_memo_approval (credit_memo_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, internalID, draftStatusID, empID); err != nil {
		t.Fatalf("seed approval sign-off: %v", err)
	}

	remaining, err = remainingApproverContacts(ctx, pool, cfg.ApproverTable, cfg.ApprovalTable, cfg.Record.IDColumn, recordTypeID, draftStatusID, internalID)
	if err != nil {
		t.Fatalf("remainingApproverContacts after sign-off: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("len(remaining) after sign-off = %d, want 0 (the one configured approver already signed off)", len(remaining))
	}
}
