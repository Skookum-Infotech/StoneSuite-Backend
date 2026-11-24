package automation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/approvalinbox"
)

const (
	resourceDailyDigest = "daily_digest"
	// digestExpiryHorizonDays is how far ahead the digest looks for expiring quotes and estimates.
	digestExpiryHorizonDays = 7

	reasonDigestOff   = "digest_off"
	reasonEmptyDigest = "empty"
)

// DigestRecipient is a user who opted in to the daily digest, with what the
// digest needs to address and fill it. EmployeeID is 0 for a user with no
// employee record (no owned documents, no approver seat).
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
	Approvals         int
	OldestApproval    *time.Time
	OverdueInvoices   int
	OverdueTotal      float64
	ExpiringQuotes    int
	ExpiringEstimates int
}

// empty reports whether there is nothing to tell the user.
func (c digestContent) empty() bool {
	return c.Approvals == 0 && c.OverdueInvoices == 0 && c.ExpiringQuotes == 0 && c.ExpiringEstimates == 0
}

const digestRecipientSelect = `
SELECT u.id::text, u.identity_id::text, u.email, u.full_name, COALESCE(e.employee_id, 0),
       p.enabled, p.send_hour, p.include_approvals, p.include_invoices, p.include_expiring, p.skip_empty
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
			&r.Prefs.Enabled, &r.Prefs.SendHour, &r.Prefs.IncludeApprovals, &r.Prefs.IncludeInvoices,
			&r.Prefs.IncludeExpiring, &r.Prefs.SkipEmpty); err != nil {
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

// overdueInvoiceSummary counts the user's own overdue, still-open invoices and
// the balance outstanding on them.
func overdueInvoiceSummary(ctx context.Context, q Querier, employeeID int, today time.Time) (count int, total float64, err error) {
	err = q.QueryRow(ctx, `
SELECT COUNT(*), COALESCE(SUM(i.invoice_balance_due), 0)::float8
FROM invoice i
JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
WHERE rs.record_status_code = ANY($1::text[])
  AND i.invoice_owner_id = $2
  AND i.invoice_due_date < $3::date
  AND i.invoice_balance_due > 0
  AND i.invoice_deleted_at IS NULL`, outstandingARStatuses, employeeID, today).Scan(&count, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("digest overdue invoices: %w", err)
	}
	return count, total, nil
}

// expiringCount counts the user's own sent, unconverted documents of one spec
// that expire from today through the horizon.
func expiringCount(ctx context.Context, q Querier, spec expirySpec, employeeID int, today time.Time) (int, error) {
	t := spec.Table
	sql := strings.NewReplacer("{t}", t).Replace(`
SELECT COUNT(*)
FROM {t} d
JOIN lkp_record_status rs ON rs.record_status_id = d.{t}_status
JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type AND rt.record_type_code = $1
WHERE rs.record_status_code = $2
  AND d.{t}_owner_id = $3
  AND d.{t}_valid_until BETWEEN $4::date AND $4::date + $5::int
  AND d.{t}_deleted_at IS NULL
  AND ` + spec.notConverted())
	var n int
	if err := q.QueryRow(ctx, sql, spec.TypeCode, recordStatusSent, employeeID, today, digestExpiryHorizonDays).Scan(&n); err != nil {
		return 0, fmt.Errorf("digest expiring %s: %w", spec.Resource, err)
	}
	return n, nil
}

// buildDigestContent gathers the sections the user asked for. A user with no
// employee record has nothing to own and no approver seat, so every section is empty.
func buildDigestContent(ctx context.Context, q ExecQuerier, r DigestRecipient, today time.Time) (digestContent, error) {
	var c digestContent
	if r.EmployeeID == 0 {
		return c, nil
	}
	if r.Prefs.IncludeApprovals {
		counts, _, err := approvalinbox.Count(ctx, q, approvalinbox.Caller{IdentityID: r.IdentityID, EmployeeID: r.EmployeeID}, approvalinbox.ScopeMine)
		if err != nil {
			return digestContent{}, fmt.Errorf("digest approvals: %w", err)
		}
		c.Approvals, c.OldestApproval = counts.Total, counts.Oldest
	}
	if r.Prefs.IncludeInvoices {
		var err error
		if c.OverdueInvoices, c.OverdueTotal, err = overdueInvoiceSummary(ctx, q, r.EmployeeID, today); err != nil {
			return digestContent{}, err
		}
	}
	if r.Prefs.IncludeExpiring {
		var err error
		if c.ExpiringQuotes, err = expiringCount(ctx, q, quoteSpec, r.EmployeeID, today); err != nil {
			return digestContent{}, err
		}
		if c.ExpiringEstimates, err = expiringCount(ctx, q, estimateSpec, r.EmployeeID, today); err != nil {
			return digestContent{}, err
		}
	}
	return c, nil
}
