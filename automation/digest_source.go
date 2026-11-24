package automation

import (
	"context"
	"fmt"
	"time"

	"stonesuite-backend/approvalinbox"
	"stonesuite-backend/authz"
)

const (
	resourceDailyDigest = "daily_digest"
	// digestHorizonDays is how far ahead the digest looks for installations and bills coming due.
	digestHorizonDays = 3

	reasonDigestOff   = "digest_off"
	reasonEmptyDigest = "empty"
)

// DigestRecipient is a user who opted in to the daily digest, with what the
// digest needs to address and fill it. EmployeeID is 0 for a user with no
// employee record (no assignments, no approver seat).
type DigestRecipient struct {
	UserID     string
	IdentityID string
	Email      string
	Name       string
	EmployeeID int
	Prefs      DigestPrefs
}

// digestContent is what one user's digest reports.
type digestContent struct {
	Installs  []InstallRow
	Bills     []VendorBillDoc
	Approvals approvalinbox.Counts
}

// empty reports whether there is nothing to tell the user.
func (c digestContent) empty() bool {
	return len(c.Installs) == 0 && len(c.Bills) == 0 && c.Approvals.Total == 0
}

// digestApprovalResources are the only modules whose pending approvals the digest reports.
var digestApprovalResources = map[authz.Resource]bool{
	authz.ResourceQuote: true, authz.ResourceEstimate: true, authz.ResourceSalesOrder: true,
}

const digestRecipientSelect = `
SELECT u.id::text, u.identity_id::text, u.email, u.full_name, COALESCE(e.employee_id, 0),
       p.enabled, p.send_hour, p.include_installs, p.include_vendor_bills, p.include_approvals, p.skip_empty
FROM automation_digest_prefs p
JOIN users u ON u.id = p.user_id AND u.status = '` + userStatusActive + `'
LEFT JOIN employee e ON e.employee_user_id = u.id AND e.employee_deleted_at IS NULL
`

func scanDigestRecipients(ctx context.Context, q Querier, sql string, args ...any) ([]DigestRecipient, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query digest recipients: %w", err)
	}
	defer rows.Close()
	var out []DigestRecipient
	for rows.Next() {
		var r DigestRecipient
		if err := rows.Scan(&r.UserID, &r.IdentityID, &r.Email, &r.Name, &r.EmployeeID,
			&r.Prefs.Enabled, &r.Prefs.SendHour, &r.Prefs.IncludeInstalls, &r.Prefs.IncludeVendorBills,
			&r.Prefs.IncludeApprovals, &r.Prefs.SkipEmpty); err != nil {
			return nil, fmt.Errorf("scan digest recipient: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate digest recipients: %w", err)
	}
	return out, nil
}

// listDigestRecipients returns every active user whose digest is switched on.
func listDigestRecipients(ctx context.Context, q Querier) ([]DigestRecipient, error) {
	return scanDigestRecipients(ctx, q, digestRecipientSelect+` WHERE p.enabled ORDER BY u.email`)
}

// loadDigestRecipient returns one user's live recipient row (any enabled state),
// and false when the user is gone or deactivated.
func loadDigestRecipient(ctx context.Context, q Querier, userID string) (DigestRecipient, bool, error) {
	rs, err := scanDigestRecipients(ctx, q, digestRecipientSelect+` WHERE u.id = $1::uuid`, userID)
	if err != nil || len(rs) == 0 {
		return DigestRecipient{}, false, err
	}
	return rs[0], true, nil
}

// digestInstalls lists the scheduled installations from today through the horizon that the employee is
// assigned to: on the visit's crew, or the owner of the job.
func digestInstalls(ctx context.Context, q Querier, employeeID int, today time.Time) ([]InstallRow, error) {
	return scanInstallRows(ctx, q, installSelect+`
WHERE a.appointment_status = '`+installStatusScheduled+`'
  AND a.visit_type = '`+installVisitType+`'
  AND j.fabrication_job_deleted_at IS NULL
  AND js.record_status_code <> $1
  AND a.local_start_date BETWEEN $2::date AND $2::date + $3::int
  AND (j.job_owner_id = $4 OR EXISTS (
        SELECT 1 FROM install_appointment_crew c
        WHERE c.install_appointment_id = a.install_appointment_id AND c.employee_id = $4))
ORDER BY a.local_start_date, a.install_appointment_id`,
		jobStatusCancelled, today, digestHorizonDays, employeeID)
}

// digestVendorBills lists the bills due by the horizon (overdue included) that the user is responsible
// for, by the same rule as the bill reminders: the bill's owner when that is an active user, otherwise
// the bill's approvers.
func digestVendorBills(ctx context.Context, q Querier, userID string, today time.Time) ([]VendorBillDoc, error) {
	bills, err := listVendorBillsDueBy(ctx, q, today, digestHorizonDays)
	if err != nil || len(bills) == 0 {
		return nil, err
	}
	approvers, err := vendorBillApprovers(ctx, q)
	if err != nil {
		return nil, err
	}
	isApprover := false
	for _, a := range approvers {
		if a.UserID == userID {
			isApprover = true
		}
	}
	var out []VendorBillDoc
	for _, b := range bills {
		if b.OwnerUserID == userID || (b.OwnerUserID == "" && isApprover) {
			out = append(out, b)
		}
	}
	return out, nil
}

// buildDigestContent gathers the sections the user asked for and may see. A section is left empty when
// the user lacks read access to what it reports, and a user with no employee record has no assignments
// and no approver seat, so every section is empty.
func buildDigestContent(ctx context.Context, q ExecQuerier, r DigestRecipient, today time.Time) (digestContent, error) {
	var c digestContent
	if r.EmployeeID == 0 {
		return c, nil
	}
	check, err := approvalinbox.GrantsCheck(ctx, q, r.IdentityID)
	if err != nil {
		return digestContent{}, fmt.Errorf("digest permissions: %w", err)
	}
	if r.Prefs.IncludeInstalls && check(authz.ResourceInstallAppointment, authz.ActionRead).Allowed {
		if c.Installs, err = digestInstalls(ctx, q, r.EmployeeID, today); err != nil {
			return digestContent{}, err
		}
	}
	if r.Prefs.IncludeVendorBills && check(authz.ResourceVendorBill, authz.ActionRead).Allowed {
		if c.Bills, err = digestVendorBills(ctx, q, r.UserID, today); err != nil {
			return digestContent{}, err
		}
	}
	if r.Prefs.IncludeApprovals {
		only := func(res authz.Resource, a authz.Action) authz.Decision {
			if !digestApprovalResources[res] {
				return authz.Decision{}
			}
			return check(res, a)
		}
		c.Approvals, _, err = approvalinbox.CountWith(ctx, q, only, approvalinbox.Caller{IdentityID: r.IdentityID, EmployeeID: r.EmployeeID}, approvalinbox.ScopeMine)
		if err != nil {
			return digestContent{}, fmt.Errorf("digest approvals: %w", err)
		}
	}
	return c, nil
}
