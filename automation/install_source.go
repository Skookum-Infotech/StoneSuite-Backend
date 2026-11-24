package automation

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// resourceInstallAppointment is automation_deliveries.resource for install reminders.
	resourceInstallAppointment = "install_appointment"
	// installVisitType is the visit a customer is reminded about. Template (measuring) and service
	// visits are not installations.
	installVisitType       = "install"
	installStatusScheduled = "scheduled"
	installStatusCancelled = "cancelled"
	installStatusCompleted = "completed"
	// jobStatusCancelled is the lkp_record_status code of a cancelled fabrication job.
	jobStatusCancelled = "CANC"
	// installLookbackDays / installLookaheadDays bound the candidate scan around local_start_date;
	// a visit that already started is never reminded about.
	installLookbackDays  = 0
	installLookaheadDays = 2

	reasonCancelled = "cancelled"
	reasonCompleted = "completed"
	// reasonCustomerGone drops a reminder whose customer was deleted.
	reasonCustomerGone = "customer_deleted"
)

// InstallRow is one scheduled installation with the customer it is for.
type InstallRow struct {
	UUID            string
	JobNumber       string
	SalesOrderUUID  string
	SalesOrderNo    string
	CustomerUUID    string
	CustomerName    string
	ContactEmail    string
	DoNotContact    bool
	CustomerDeleted bool
	StartsAt        time.Time
	AllDay          bool
	Timezone        string
	LocalStart      time.Time
	Status          string
	JobDeleted      bool
	JobCancelled    bool
	// Address is the job site address as one line ("123 Main St, Austin, TX 78701"); empty when none is set.
	Address string
}

const installSelect = `
SELECT a.install_appointment_uuid::text, COALESCE(j.fabrication_job_number, ''),
       so.sales_order_uuid::text, COALESCE(so.sales_order_number, ''),
       c.customer_uuid::text, c.customer_name, COALESCE(c.customer_contact_email, ''), c.customer_do_not_contact,
       (c.customer_deleted_at IS NOT NULL),
       a.starts_at, a.all_day, a.timezone, a.local_start_date, a.appointment_status,
       (j.fabrication_job_deleted_at IS NOT NULL), (js.record_status_code = $1),
       COALESCE(j.job_site_addr_line1, ''), COALESCE(j.job_site_addr_line2, ''), COALESCE(j.job_site_addr_city, ''),
       COALESCE(st.state_code, ''), COALESCE(j.job_site_addr_zip, '')
FROM install_appointment a
JOIN fabrication_job j ON j.fabrication_job_id = a.fabrication_job_id
JOIN lkp_record_status js ON js.record_status_id = j.fabrication_job_status
JOIN sales_order so ON so.sales_order_id = j.sales_order_id
JOIN customer c ON c.customer_id = j.fabrication_job_customer_id
LEFT JOIN lkp_state st ON st.state_id = j.job_site_addr_state
`

// formatInstallAddress joins the non-empty parts of a job site address into one line.
func formatInstallAddress(line1, line2, city, state, zip string) string {
	stateZip := strings.TrimSpace(state + " " + zip)
	var parts []string
	for _, p := range []string{line1, line2, city, stateZip} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// scanInstallRows runs a query built on installSelect and decodes every row.
func scanInstallRows(ctx context.Context, q Querier, sql string, args ...any) ([]InstallRow, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query install rows: %w", err)
	}
	defer rows.Close()
	var out []InstallRow
	for rows.Next() {
		var r InstallRow
		var l1, l2, city, state, zip string
		if err := rows.Scan(&r.UUID, &r.JobNumber, &r.SalesOrderUUID, &r.SalesOrderNo, &r.CustomerUUID, &r.CustomerName,
			&r.ContactEmail, &r.DoNotContact, &r.CustomerDeleted, &r.StartsAt, &r.AllDay, &r.Timezone, &r.LocalStart,
			&r.Status, &r.JobDeleted, &r.JobCancelled, &l1, &l2, &city, &state, &zip); err != nil {
			return nil, fmt.Errorf("scan install row: %w", err)
		}
		r.ContactEmail = strings.ToLower(strings.TrimSpace(r.ContactEmail))
		r.Address = formatInstallAddress(l1, l2, city, state, zip)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate install rows: %w", err)
	}
	return out, nil
}

// listInstallCandidates returns every scheduled installation on a live job whose local start date is
// within [today, today+2d], soonest first.
func listInstallCandidates(ctx context.Context, q Querier, today time.Time) ([]InstallRow, error) {
	return scanInstallRows(ctx, q, installSelect+`
WHERE a.appointment_status = '`+installStatusScheduled+`'
  AND a.visit_type = '`+installVisitType+`'
  AND j.fabrication_job_deleted_at IS NULL
  AND js.record_status_code <> $1
  AND a.local_start_date BETWEEN $2::date - $3::int AND $2::date + $4::int
ORDER BY a.local_start_date, a.install_appointment_id`,
		jobStatusCancelled, today, installLookbackDays, installLookaheadDays)
}

// loadInstall re-reads one installation by uuid; found is false when it no longer exists.
func loadInstall(ctx context.Context, q Querier, uuid string) (row InstallRow, found bool, err error) {
	rows, err := scanInstallRows(ctx, q, installSelect+` WHERE a.install_appointment_uuid = $2::uuid`, jobStatusCancelled, uuid)
	if err != nil {
		return InstallRow{}, false, fmt.Errorf("load install %s: %w", uuid, err)
	}
	if len(rows) == 0 {
		return InstallRow{}, false, nil
	}
	return rows[0], true, nil
}

// installReason maps live facts to the first matching skip reason ("" = eligible). plannedEmail is
// the address the delivery was planned for: a customer whose contact email changed since is a
// different recipient, so the old delivery is dropped rather than sent to the new address.
func installReason(r InstallRow, planned time.Time, plannedEmail string) string {
	switch {
	case r.JobDeleted:
		return reasonVoided
	case r.Status == installStatusCancelled || r.JobCancelled:
		return reasonCancelled
	case r.Status == installStatusCompleted:
		return reasonCompleted
	case !sameDay(r.LocalStart, planned):
		return reasonAnchorChanged
	case r.CustomerDeleted:
		return reasonCustomerGone
	case r.DoNotContact:
		return reasonDoNotContact
	case r.ContactEmail == "":
		return reasonNoEmail
	case plannedEmail != "" && !strings.EqualFold(r.ContactEmail, plannedEmail):
		return reasonRecipientChanged
	}
	return ""
}
