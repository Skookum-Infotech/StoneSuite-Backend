//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// poSpec returns the purchase_order spec at its PAPV gate.
func poSpec(t *testing.T) approvalSpec {
	t.Helper()
	for _, s := range approvalSpecs() {
		if s.Workflow == "purchase_order" {
			return s
		}
	}
	require.FailNow(t, "purchase_order spec missing")
	return approvalSpec{}
}

type apprPO struct {
	owner    *int
	status   string // lkp status code, default PAPV
	approval string // approval status column, default "pending"
	entered  time.Time
	deleted  bool
}

// po inserts a purchase order with a history row entering its status at p.entered (UTC wall clock).
func (f *arFixture) po(p apprPO) (uuid string, id int) {
	if p.status == "" {
		p.status = "PAPV"
	}
	if p.approval == "" {
		p.approval = "pending"
	}
	if p.approval == "rejected" {
		// Only in-place-reject modules allow 'rejected'; relax the check inside this rolled-back tx.
		_, err := f.tx.Exec(f.ctx, `ALTER TABLE purchase_order DROP CONSTRAINT IF EXISTS chk_po_approval_status`)
		require.NoError(f.t, err)
	}
	var vendorID int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_legal_name) VALUES ($1, $2, 'Acme Supply') RETURNING vendor_id`,
		f.typeID("VNDR"), f.statusID("VNDR", "ACT_")).Scan(&vendorID))
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO purchase_order (record_type, purchase_order_status, purchase_order_approval_status,
			purchase_order_vendor_id, purchase_order_owner_id, purchase_order_number)
		VALUES ($1, $2, $3, $4, $5, 'PORD-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING purchase_order_uuid::text, purchase_order_id`,
		f.typeID("PORD"), f.statusID("PORD", p.status), p.approval, vendorID, p.owner).Scan(&uuid, &id))
	f.enter(id, p.status, p.entered)
	if p.deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_deleted_at = now(), purchase_order_deleted_by = 1 WHERE purchase_order_id = $1`, id)
		require.NoError(f.t, err)
	}
	return uuid, id
}

// enter records a history row moving PO id into status at the given UTC wall-clock time.
func (f *arFixture) enter(id int, status string, at time.Time) {
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, to_status_id, at) VALUES ($1, $2, $3)`,
		id, f.statusID("PORD", status), at)
	require.NoError(f.t, err)
}

// approver configures employee as an active PORD/PAPV approver.
func (f *arFixture) approver(emp int) {
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO purchase_order_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.typeID("PORD"), f.statusID("PORD", "PAPV"), emp)
	require.NoError(f.t, err)
}

func (f *arFixture) signOff(poID, emp int) {
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO purchase_order_approval (purchase_order_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, poID, f.statusID("PORD", "PAPV"), emp)
	require.NoError(f.t, err)
}

// superAdmin creates an active user holding the super_admin role; returns the user id.
func (f *arFixture) superAdmin(email, status string) string {
	_, err := f.tx.Exec(f.ctx, `INSERT INTO roles (key, name, is_system) SELECT 'super_admin', 'Super Admin', TRUE
		WHERE NOT EXISTS (SELECT 1 FROM roles WHERE LOWER(key) = 'super_admin')`)
	require.NoError(f.t, err)
	return f.grantSuperAdmin(f.userOnly(email, status))
}

func (f *arFixture) userOnly(email, status string) string {
	var id string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO users (identity_id, email, status) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text`,
		email, status).Scan(&id))
	return id
}

func (f *arFixture) grantSuperAdmin(userID string) string {
	_, err := f.tx.Exec(f.ctx, `INSERT INTO user_roles (user_id, role_id)
		SELECT $1::uuid, id FROM roles WHERE LOWER(key) = 'super_admin'`, userID)
	require.NoError(f.t, err)
	return userID
}

func atDays(n int, hour int) time.Time {
	return arToday.AddDate(0, 0, -n).Add(time.Duration(hour) * time.Hour)
}

func uuids(recs []ApprovalRecord) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.UUID)
	}
	return out
}

func userIDs(rs []approvalRecipient) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.UserID)
	}
	return out
}
