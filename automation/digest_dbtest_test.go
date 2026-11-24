//go:build dbtest

package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *arFixture) optIn(userID string, mutate func(*DigestPrefs)) DigestPrefs {
	p := DefaultDigestPrefs()
	p.Enabled = true
	if mutate != nil {
		mutate(&p)
	}
	require.NoError(f.t, SaveDigestPrefs(f.ctx, f.tx, userID, p))
	return p
}

func TestDigestPrefsRoundTrip(t *testing.T) {
	f := newARFixture(t)
	_, userID := f.employeeWithUser("prefs@x.io", "active")

	got, err := LoadDigestPrefs(f.ctx, f.tx, userID)
	require.NoError(t, err)
	assert.Equal(t, DefaultDigestPrefs(), got, "a user with no row has the defaults, digest off")

	want := DigestPrefs{Enabled: true, SendHour: 14, IncludeInstalls: true, IncludeVendorBills: false, IncludeApprovals: true, SkipEmpty: false}
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, userID, want))
	got, err = LoadDigestPrefs(f.ctx, f.tx, userID)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	want.SendHour = 9
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, userID, want), "saving again updates the same row")
	got, err = LoadDigestPrefs(f.ctx, f.tx, userID)
	require.NoError(t, err)
	assert.Equal(t, 9, got.SendHour)

	var rows int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT COUNT(*) FROM automation_digest_prefs WHERE user_id = $1::uuid`, userID).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestDigestPrefsAreRefusedBeforeTheDatabase(t *testing.T) {
	f := newARFixture(t)
	_, userID := f.employeeWithUser("bad@x.io", "active")
	bad := DefaultDigestPrefs()
	bad.SendHour = 3
	assert.ErrorIs(t, SaveDigestPrefs(f.ctx, f.tx, userID, bad), ErrInvalidDigestPrefs)
	var rows int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT COUNT(*) FROM automation_digest_prefs WHERE user_id = $1::uuid`, userID).Scan(&rows))
	assert.Zero(t, rows, "an invalid save must write nothing")
}

func TestDigestHourConstraintBacksUpTheCode(t *testing.T) {
	f := newARFixture(t)
	_, userID := f.employeeWithUser("constraint@x.io", "active")
	for _, hour := range []int{7, 18, -1, 24} {
		_, err := f.tx.Exec(f.ctx, `SAVEPOINT s`)
		require.NoError(t, err)
		_, err = f.tx.Exec(f.ctx, `INSERT INTO automation_digest_prefs (user_id, send_hour) VALUES ($1::uuid, $2)`, userID, hour)
		assert.Error(t, err, "hour %d must be refused by the table itself", hour)
		_, err = f.tx.Exec(f.ctx, `ROLLBACK TO SAVEPOINT s`)
		require.NoError(t, err)
	}
}

func TestListDigestRecipients(t *testing.T) {
	f := newARFixture(t)
	empOn, userOn := f.employeeWithUser("on@x.io", "active")
	_, userOff := f.employeeWithUser("off@x.io", "active")
	_, userDisabled := f.employeeWithUser("disabled@x.io", "disabled")
	var userNoEmp string
	require.NoError(t, f.tx.QueryRow(f.ctx, `INSERT INTO users (identity_id, email, status) VALUES (gen_random_uuid(), 'noemp@x.io', 'active') RETURNING id::text`).Scan(&userNoEmp))

	f.optIn(userOn, func(p *DigestPrefs) { p.SendHour = 11 })
	f.optIn(userDisabled, nil)
	f.optIn(userNoEmp, nil)
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, userOff, DefaultDigestPrefs()), "a saved but switched-off row")

	got, err := listDigestRecipients(f.ctx, f.tx)
	require.NoError(t, err)
	byEmail := map[string]DigestRecipient{}
	for _, r := range got {
		byEmail[r.Email] = r
	}
	require.Len(t, byEmail, 2, "only opted-in, active users are listed")
	assert.Equal(t, empOn, byEmail["on@x.io"].EmployeeID)
	assert.Equal(t, 11, byEmail["on@x.io"].Prefs.SendHour)
	assert.Zero(t, byEmail["noemp@x.io"].EmployeeID, "a user with no employee record is still listed, with employee 0")

	one, found, err := loadDigestRecipient(f.ctx, f.tx, userOn)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "on@x.io", one.Email)
	off, found, err := loadDigestRecipient(f.ctx, f.tx, userOff)
	require.NoError(t, err)
	require.True(t, found)
	assert.False(t, off.Prefs.Enabled, "the live load returns a switched-off user so the renderer can drop the digest")
	_, found, err = loadDigestRecipient(f.ctx, f.tx, userDisabled)
	require.NoError(t, err)
	assert.False(t, found, "a deactivated user is gone")
}

// grantRole gives the user a role holding each (resource, action) pair at the given scope.
func (f *arFixture) grantRole(userID string, scope string, pairs ...[2]string) {
	var roleID string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `INSERT INTO roles (key, name, is_system) VALUES ('digest-' || gen_random_uuid()::text, 'Digest test', FALSE) RETURNING id::text`).Scan(&roleID))
	for _, p := range pairs {
		_, err := f.tx.Exec(f.ctx, `INSERT INTO role_permissions (role_id, resource, action, scope) VALUES ($1::uuid, $2, $3, $4)`, roleID, p[0], p[1], scope)
		require.NoError(f.t, err)
	}
	_, err := f.tx.Exec(f.ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1::uuid, $2::uuid)`, userID, roleID)
	require.NoError(f.t, err)
}

// crew puts the employee on the visit crew.
func (f *arFixture) crew(apptUUID string, emp int) {
	_, err := f.tx.Exec(f.ctx, `INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by)
		SELECT install_appointment_id, $2, 'lead', $2 FROM install_appointment WHERE install_appointment_uuid = $1::uuid`, apptUUID, emp)
	require.NoError(f.t, err)
}

func TestDigestInstalls(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	crewEmp, _ := f.employeeWithUser("crew@x.io", "active")
	ownerEmp, _ := f.employeeWithUser("jobowner@x.io", "active")
	other, _ := f.employeeWithUser("elsewhere@x.io", "active")

	job := f.instJob(cust, "INST", false)
	mine := f.instAppointment(instAppt{job: job, day: dayOffset(1)})
	f.crew(mine, crewEmp)
	today := f.instAppointment(instAppt{job: job, day: dayOffset(0)})
	f.crew(today, crewEmp)
	f.instAppointment(instAppt{job: job, day: dayOffset(4)})                      // beyond the horizon
	f.instAppointment(instAppt{job: job, day: dayOffset(-1)})                     // already past
	f.instAppointment(instAppt{job: job, day: dayOffset(2), status: "cancelled"}) // cancelled
	f.instAppointment(instAppt{job: job, day: dayOffset(2), visitType: "template"})
	cancelledJob := f.instJob(cust, "CANC", false)
	c := f.instAppointment(instAppt{job: cancelledJob, day: dayOffset(1)})
	f.crew(c, crewEmp)
	_, err := f.tx.Exec(f.ctx, `UPDATE fabrication_job SET job_owner_id = $2 WHERE fabrication_job_id = $1`, job, ownerEmp)
	require.NoError(t, err)

	got, err := digestInstalls(f.ctx, f.tx, crewEmp, arToday)
	require.NoError(t, err)
	require.Len(t, got, 2, "scheduled install visits from today to +3 days on live jobs")
	assert.Equal(t, today, got[0].UUID, "soonest first")
	assert.Equal(t, mine, got[1].UUID)

	got, err = digestInstalls(f.ctx, f.tx, ownerEmp, arToday)
	require.NoError(t, err)
	assert.Len(t, got, 2, "the owner of the job is assigned to its visits (the in-range, scheduled install ones)")

	got, err = digestInstalls(f.ctx, f.tx, other, arToday)
	require.NoError(t, err)
	assert.Empty(t, got, "someone neither on the crew nor the owner sees nothing")
}

func TestDigestVendorBills(t *testing.T) {
	f := newARFixture(t)
	vendor := f.vendor()
	owner, ownerUser := f.employeeWithUser("vb-own@x.io", "active")
	approver, approverUser := f.employeeWithUser("vb-appr@x.io", "active")
	_, bystander := f.employeeWithUser("vb-by@x.io", "active")
	f.vbApprover(approver)

	owned := f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(2), total: "100", paid: "0", credit: "0", owner: &owner})
	unowned := f.vbRowInsert(vbRow{vendor: vendor, status: "PART", due: dayOffset(-9), total: "50", paid: "10", credit: "0"})
	f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(4), total: "100", paid: "0", credit: "0", owner: &owner}) // too far out
	f.vbRowInsert(vbRow{vendor: vendor, status: "PAID", due: dayOffset(1), total: "100", paid: "100", credit: "0", owner: &owner})
	f.vbRowInsert(vbRow{vendor: vendor, status: "PINI", due: dayOffset(1), total: "100", paid: "0", credit: "0", owner: &owner})

	ids := func(bs []VendorBillDoc) []string {
		var out []string
		for _, b := range bs {
			out = append(out, b.UUID)
		}
		return out
	}
	got, err := digestVendorBills(f.ctx, f.tx, ownerUser, arToday)
	require.NoError(t, err)
	assert.Equal(t, []string{owned}, ids(got), "the owner sees their own bill, not the unowned one")

	got, err = digestVendorBills(f.ctx, f.tx, approverUser, arToday)
	require.NoError(t, err)
	assert.Equal(t, []string{unowned}, ids(got), "an approver sees bills with no owner, overdue ones included")

	got, err = digestVendorBills(f.ctx, f.tx, bystander, arToday)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestBuildDigestContent(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	vendor := f.vendor()
	emp, userID := f.employeeWithUser("content@x.io", "active")
	job := f.instJob(cust, "INST", false)
	f.crew(f.instAppointment(instAppt{job: job, day: dayOffset(1)}), emp)
	f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(1), total: "80", paid: "0", credit: "0", owner: &emp})
	for _, d := range salesDocs { // one waiting on the user in each of the three modules
		f.salesDocAtGate(d, cust, emp, emp, atDays(2, 12))
	}
	f.optIn(userID, nil)
	rc, found, err := loadDigestRecipient(f.ctx, f.tx, userID)
	require.NoError(t, err)
	require.True(t, found)

	c, err := buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err, "a user with no grants must not make the content fail")
	assert.True(t, c.empty(), "without read permission nothing is reported: the digest follows what the user may see")

	f.grantRole(userID, "all",
		[2]string{"install_appointment", "read"}, [2]string{"vendor_bill", "read"},
		[2]string{"quote", "read"}, [2]string{"estimate", "read"}, [2]string{"sales_order", "read"},
		[2]string{"quote", "transition"}, [2]string{"estimate", "transition"}, [2]string{"sales_order", "transition"})
	c, err = buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err)
	assert.Len(t, c.Installs, 1)
	assert.Len(t, c.Bills, 1)
	assert.Equal(t, 3, c.Approvals.Total, "one quote, one estimate, one sales order")
	for _, m := range c.Approvals.Modules {
		assert.Contains(t, []string{"quote", "estimate", "sales_order"}, m.WorkflowKey, "only those three modules are reported")
	}
	assert.False(t, c.empty())

	rc.Prefs.IncludeInstalls, rc.Prefs.IncludeVendorBills, rc.Prefs.IncludeApprovals = false, false, false
	c, err = buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err)
	assert.True(t, c.empty(), "a section the user switched off is never queried or shown")

	rc.EmployeeID = 0
	rc.Prefs = DefaultDigestPrefs()
	c, err = buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err)
	assert.True(t, c.empty(), "a user with no employee record has no assignments")
}
