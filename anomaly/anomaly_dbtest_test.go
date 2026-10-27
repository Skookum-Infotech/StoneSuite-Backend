//go:build dbtest

package anomaly

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
)

const dbtestRuleKey = "dbtest_rule"

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// cleanTestRows removes every row this file creates, before and after a test.
func cleanTestRows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	clean := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM anomaly WHERE anomaly_rule_key = $1`, dbtestRuleKey)
	}
	clean()
	t.Cleanup(clean)
}

// fixedRule returns the same findings on every scan, like a rule whose
// condition is still true.
type fixedRule struct{ findings []Finding }

func (fixedRule) Key() string { return dbtestRuleKey }
func (r fixedRule) Detect(context.Context, Querier, time.Time) ([]Finding, error) {
	return r.findings, nil
}

type fakeSender struct {
	fail bool
	sent []services.NotificationRequest
}

func (s *fakeSender) Send(_ context.Context, req services.NotificationRequest) (services.NotificationResult, error) {
	if s.fail {
		return services.NotificationResult{}, errors.New("notify down")
	}
	s.sent = append(s.sent, req)
	return services.NotificationResult{}, nil
}

func seedUser(t *testing.T, pool *pgxpool.Pool, status string) string {
	t.Helper()
	identity := newUUID()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES ($1::uuid, $2, 'Anomaly Test', $3)`,
		identity, fmt.Sprintf("anomaly-%s@example.test", identity), status)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE identity_id = $1::uuid`, identity)
	})
	return identity
}

func finding(subject string, audience ...string) Finding {
	return Finding{
		RuleKey: dbtestRuleKey, SubjectKey: subject, Severity: SeverityWarning,
		Resource: "expense", RecordUUID: newUUID(),
		Title: "Test finding " + subject, Detail: "detail",
		Audience: Audience{IdentityIDs: audience},
	}
}

// Every shipped rule's SQL must execute against the real schema: a typo in a
// table or column name only shows up here.
func TestDefaultRulesRunAgainstTheSchema(t *testing.T) {
	pool := newTestPool(t)
	for _, rule := range DefaultRules() {
		_, err := rule.Detect(context.Background(), pool, time.Now())
		assert.NoError(t, err, rule.Key())
	}
}

func TestScanStoresOnceAndExcludesTheInvolvedPerson(t *testing.T) {
	pool := newTestPool(t)
	cleanTestRows(t, pool)
	ctx := context.Background()
	owner, culprit := seedUser(t, pool, "active"), seedUser(t, pool, "active")

	f := finding("s1", owner, culprit)
	f.InvolvedIdentityID = culprit
	rule := fixedRule{findings: []Finding{f}}

	first, err := Scan(ctx, pool, []Rule{rule}, time.Now())
	require.NoError(t, err)
	assert.Equal(t, ScanResult{Raised: 1}, first)

	second, err := Scan(ctx, pool, []Rule{rule}, time.Now())
	require.NoError(t, err)
	assert.Equal(t, ScanResult{Existing: 1}, second, "a rescan must not raise the same subject again")

	var recipients []string
	rows, err := pool.Query(ctx, `
		SELECT r.recipient_identity_id::text FROM anomaly_recipient r
		JOIN anomaly a ON a.anomaly_id = r.anomaly_id
		WHERE a.anomaly_rule_key = $1`, dbtestRuleKey)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		recipients = append(recipients, id)
	}
	assert.Equal(t, []string{owner}, recipients, "the person who tripped the rule is never a recipient")
}

func TestScanRejectsAnInvalidFindingWithoutStoppingTheRest(t *testing.T) {
	pool := newTestPool(t)
	cleanTestRows(t, pool)
	bad := finding("bad")
	bad.Title = ""
	res, err := Scan(context.Background(), pool, []Rule{fixedRule{findings: []Finding{bad, finding("good")}}}, time.Now())
	require.Error(t, err)
	assert.Equal(t, 1, res.Raised)
}

func TestListVisibilityPagingAndReview(t *testing.T) {
	pool := newTestPool(t)
	cleanTestRows(t, pool)
	ctx := context.Background()
	mine, stranger := newUUID(), newUUID()

	_, err := Scan(ctx, pool, []Rule{fixedRule{findings: []Finding{finding("a", mine), finding("b", mine), finding("c", stranger)}}}, time.Now())
	require.NoError(t, err)

	owner := Viewer{IdentityID: mine}
	page, err := List(ctx, pool, owner, ListFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor, "a second finding of mine exists")

	cur, err := DecodeCursor(page.NextCursor)
	require.NoError(t, err)
	next, err := List(ctx, pool, owner, ListFilter{Limit: 1, Cursor: cur})
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	assert.NotEqual(t, page.Items[0].ID, next.Items[0].ID)
	assert.Empty(t, next.NextCursor, "that was the last of mine")

	admin := Viewer{IdentityID: newUUID(), SeesAll: true}
	all, err := List(ctx, pool, admin, ListFilter{Limit: MaxPageSize})
	require.NoError(t, err)
	titles := map[string]bool{}
	for _, it := range all.Items {
		titles[it.Title] = true
	}
	assert.True(t, titles["Test finding a"] && titles["Test finding b"] && titles["Test finding c"], "a super admin sees every finding")

	// A finding addressed to someone else is a not-found, never a forbidden.
	var strangersID string
	for _, it := range all.Items {
		if it.Title == "Test finding c" {
			strangersID = it.ID
		}
	}
	_, err = UpdateStatus(ctx, pool, owner, strangersID, StatusDismissed, "")
	assert.ErrorIs(t, err, ErrNotFound)

	got, err := UpdateStatus(ctx, pool, owner, page.Items[0].ID, StatusDismissed, "expected for us")
	require.NoError(t, err)
	assert.Equal(t, StatusDismissed, got.Status)
	assert.Equal(t, "expected for us", got.Note)
	assert.NotNil(t, got.StatusChangedAt)

	dismissed, err := List(ctx, pool, owner, ListFilter{Status: StatusDismissed})
	require.NoError(t, err)
	assert.Len(t, dismissed.Items, 1)

	// A dismissed finding does not reopen when its condition is still true.
	_, err = Scan(ctx, pool, []Rule{fixedRule{findings: []Finding{finding("a", mine), finding("b", mine)}}}, time.Now())
	require.NoError(t, err)
	still, err := List(ctx, pool, owner, ListFilter{Status: StatusDismissed})
	require.NoError(t, err)
	assert.Len(t, still.Items, 1)
}

func TestAlertRetriesAfterAFailureAndNeverDuplicates(t *testing.T) {
	pool := newTestPool(t)
	cleanTestRows(t, pool)
	ctx := context.Background()
	active := seedUser(t, pool, "active")
	_, err := Scan(ctx, pool, []Rule{fixedRule{findings: []Finding{finding("alert", active)}}}, time.Now())
	require.NoError(t, err)

	down := &fakeSender{fail: true}
	res, err := Alert(ctx, pool, down, "tenant-1")
	require.Error(t, err)
	assert.Equal(t, AlertResult{Failed: 1}, res)

	up := &fakeSender{}
	res, err = Alert(ctx, pool, up, "tenant-1")
	require.NoError(t, err)
	assert.Equal(t, AlertResult{Sent: 1}, res, "the failed send is retried")
	require.Len(t, up.sent, 1)
	assert.Equal(t, "anomaly."+dbtestRuleKey, up.sent[0].EventType)
	require.Len(t, up.sent[0].Recipients, 1)
	assert.Equal(t, active, up.sent[0].Recipients[0].UserID)

	res, err = Alert(ctx, pool, up, "tenant-1")
	require.NoError(t, err)
	assert.Equal(t, AlertResult{}, res, "an announced finding is not sent again")
	assert.Len(t, up.sent, 1)
}

func TestAlertMarksUnaddressedFindingsAnnounced(t *testing.T) {
	pool := newTestPool(t)
	cleanTestRows(t, pool)
	ctx := context.Background()
	disabled := seedUser(t, pool, "disabled")
	_, err := Scan(ctx, pool, []Rule{fixedRule{findings: []Finding{finding("nobody", disabled)}}}, time.Now())
	require.NoError(t, err)

	sender := &fakeSender{}
	res, err := Alert(ctx, pool, sender, "tenant-1")
	require.NoError(t, err)
	assert.Equal(t, AlertResult{Unaddressed: 1}, res)
	assert.Empty(t, sender.sent)

	res, err = Alert(ctx, pool, sender, "tenant-1")
	require.NoError(t, err)
	assert.Equal(t, AlertResult{}, res, "not retried forever")
}

func TestUnbalancedJournalRule(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	var account int
	if err := pool.QueryRow(ctx, `SELECT coa_account_id FROM coa_account ORDER BY coa_account_id LIMIT 1`).Scan(&account); err != nil {
		t.Skip("no chart-of-accounts row in the test database; skipping")
	}
	entryUUID := newUUID()
	var entryID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO journal_entry (journal_entry_uuid, entry_date, source_type, source_id)
		VALUES ($1::uuid, $2, 'dbtest', $1::uuid) RETURNING journal_entry_id`,
		entryUUID, time.Now().Format("2006-01-02")).Scan(&entryID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM journal_entry_line WHERE journal_entry_id = $1`, entryID)
		_, _ = pool.Exec(ctx, `DELETE FROM journal_entry WHERE journal_entry_id = $1`, entryID)
		_, _ = pool.Exec(ctx, `DELETE FROM anomaly WHERE anomaly_rule_key = $1 AND anomaly_subject_key = $2`, RuleUnbalancedJournal, entryUUID)
	})
	_, err := pool.Exec(ctx, `
		INSERT INTO journal_entry_line (journal_entry_id, line_number, coa_account_id, debit, credit)
		VALUES ($1, 1, $2, 100.00, 0), ($1, 2, $2, 0, 90.00)`, entryID, account)
	require.NoError(t, err)

	found, err := UnbalancedJournal{}.Detect(ctx, pool, time.Now())
	require.NoError(t, err)
	var mine *Finding
	for i := range found {
		if found[i].SubjectKey == entryUUID {
			mine = &found[i]
		}
	}
	require.NotNil(t, mine, "the unbalanced entry is reported")
	assert.Equal(t, SeverityCritical, mine.Severity)
	assert.Contains(t, mine.Detail, "100.00")
	assert.Contains(t, mine.Detail, "90.00")
	assert.True(t, mine.Audience.SuperAdmins)
}

// newUUID returns a random version-4 UUID string.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("read random bytes: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// seedVendorBill inserts a live vendor bill for vendorID and returns its uuid.
func seedVendorBill(t *testing.T, pool *pgxpool.Pool, vendorID int, invoiceNo, total string, ownerEmployee any) string {
	t.Helper()
	ctx := context.Background()
	var billUUID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id,
		                         vendor_bill_vendor_invoice_number, vendor_bill_grand_total, vendor_bill_owner_id)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL'),
		        (SELECT record_status_id FROM lkp_record_status WHERE record_status_code = 'DRFT'
		           AND record_status_record_type = (SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL')),
		        $1, $2, $3::numeric, $4)
		RETURNING vendor_bill_uuid::text`, vendorID, invoiceNo, total, ownerEmployee).Scan(&billUUID))
	return billUUID
}

func TestDuplicateVendorBillRule(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	var vendorID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_legal_name)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'),
		        (SELECT record_status_id FROM lkp_record_status WHERE record_status_code = 'ACT_'
		           AND record_status_record_type = (SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR')), 'Anomaly Test Vendor')
		RETURNING vendor_id`).Scan(&vendorID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM anomaly WHERE anomaly_rule_key = $1`, RuleDuplicateVendorBill)
		_, _ = pool.Exec(ctx, `DELETE FROM vendor_bill WHERE vendor_bill_vendor_id = $1`, vendorID)
		_, _ = pool.Exec(ctx, `DELETE FROM vendor WHERE vendor_id = $1`, vendorID)
	})

	original := seedVendorBill(t, pool, vendorID, "INV-77", "250.00", nil)
	dup := seedVendorBill(t, pool, vendorID, " inv-77 ", "250.00", nil) // case and spacing ignored
	seedVendorBill(t, pool, vendorID, "INV-77", "251.00", nil)          // different total
	seedVendorBill(t, pool, vendorID, "", "250.00", nil)                // no vendor invoice number

	found, err := DuplicateVendorBill{}.Detect(ctx, pool, time.Now())
	require.NoError(t, err)
	var subjects []string
	for _, f := range found {
		subjects = append(subjects, f.SubjectKey)
	}
	assert.Contains(t, subjects, dup, "the later copy is the finding")
	assert.NotContains(t, subjects, original, "the first bill is never flagged")
	assert.Len(t, subjects, 1, "a different total or a blank invoice number is not a duplicate")

	// Once stored, the same bill is not reported again.
	_, err = Scan(ctx, pool, []Rule{DuplicateVendorBill{}}, time.Now())
	require.NoError(t, err)
	again, err := DuplicateVendorBill{}.Detect(ctx, pool, time.Now())
	require.NoError(t, err)
	assert.Empty(t, again)
}

func TestSelfApprovalRule(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	identity := seedUser(t, pool, "active")
	var userID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id::text FROM users WHERE identity_id = $1::uuid`, identity).Scan(&userID))
	var employeeID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1::uuid, 'Self', 'Approver', $2, 1) RETURNING employee_id`,
		userID, "self-"+identity+"@example.test").Scan(&employeeID))

	var expenseID int
	var expenseUUID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO expense (record_type, expense_status, expense_claimant_id)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'EXPN'),
		        (SELECT record_status_id FROM lkp_record_status WHERE record_status_code = 'SUBM'
		           AND record_status_record_type = (SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'EXPN')),
		        $1)
		RETURNING expense_id, expense_uuid::text`, employeeID).Scan(&expenseID, &expenseUUID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM anomaly WHERE anomaly_rule_key = $1`, RuleSelfApproval)
		_, _ = pool.Exec(ctx, `DELETE FROM expense_approval WHERE expense_id = $1`, expenseID)
		_, _ = pool.Exec(ctx, `DELETE FROM expense WHERE expense_id = $1`, expenseID)
		_, _ = pool.Exec(ctx, `DELETE FROM employee WHERE employee_id = $1`, employeeID)
	})
	_, err := pool.Exec(ctx, `
		INSERT INTO expense_approval (expense_id, record_status_id, approver_employee_id)
		VALUES ($1, (SELECT expense_status FROM expense WHERE expense_id = $1), $2)`, expenseID, employeeID)
	require.NoError(t, err)

	found, err := SelfApproval{}.Detect(ctx, pool, time.Now())
	require.NoError(t, err)
	var mine *Finding
	for i := range found {
		if found[i].RecordUUID == expenseUUID {
			mine = &found[i]
		}
	}
	require.NotNil(t, mine, "an owner approving their own expense is reported")
	assert.Equal(t, "expense", mine.Resource)
	assert.Equal(t, identity, mine.InvolvedIdentityID)
	assert.True(t, mine.Audience.SuperAdmins)

	// The person involved is dropped from the recipients even if they are a super admin.
	recipients, err := resolveRecipients(ctx, pool, *mine)
	require.NoError(t, err)
	assert.NotContains(t, recipients, identity)
}

func TestAlertKeepsInfoFindingsOffTheBell(t *testing.T) {
	pool := newTestPool(t)
	cleanTestRows(t, pool)
	ctx := context.Background()
	active := seedUser(t, pool, "active")
	info := finding("info", active)
	info.Severity = SeverityInfo
	_, err := Scan(ctx, pool, []Rule{fixedRule{findings: []Finding{info}}}, time.Now())
	require.NoError(t, err)

	sender := &fakeSender{}
	res, err := Alert(ctx, pool, sender, "tenant-1")
	require.NoError(t, err)
	assert.Equal(t, AlertResult{PageOnly: 1}, res)
	assert.Empty(t, sender.sent, "info findings appear on the page but are never sent")

	page, err := List(ctx, pool, Viewer{IdentityID: active}, ListFilter{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "the user can still see it on the Anomalies page")
}
