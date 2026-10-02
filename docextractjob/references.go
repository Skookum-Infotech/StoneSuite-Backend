package docextractjob

import (
	"context"
	"fmt"
	"strings"
)

// Quote / estimate number formats ('QUOT-000001' / 'ESTM-000001').
const (
	quoteNumberFormat    = "QUOT-%06d"
	estimateNumberFormat = "ESTM-%06d"
	maxReferenceMatches  = 3
	digitsOnlyMaxLen     = 9
)

// referenceNumbers expands a number quoted on a customer PO into the upper-case
// candidate quote and estimate numbers it could mean. A bare number
// ("1001") also expands to the zero-padded system format.
func referenceNumbers(ref string) (quotes, estimates []string) {
	up := strings.ToUpper(strings.TrimSpace(ref))
	if up == "" {
		return nil, nil
	}
	quotes, estimates = []string{up}, []string{up}
	if len(up) <= digitsOnlyMaxLen && strings.Trim(up, "0123456789") == "" {
		var n int
		if _, err := fmt.Sscanf(up, "%d", &n); err == nil {
			quotes = append(quotes, fmt.Sprintf(quoteNumberFormat, n))
			estimates = append(estimates, fmt.Sprintf(estimateNumberFormat, n))
		}
	}
	return quotes, estimates
}

// References looks up the quote and estimate a customer PO refers to by number
// and returns link-only findings ("open the quote to convert instead?").
func (c *Checker) References(ctx context.Context, ref string) ([]Duplicate, error) {
	quotes, estimates := referenceNumbers(ref)
	if len(quotes) == 0 {
		return nil, nil
	}
	var out []Duplicate
	for _, t := range []struct {
		kind, table, prefix string
		numbers             []string
	}{
		{DupQuoteRef, "quote", "quote", quotes},
		{DupEstimateRef, "estimate", "estimate", estimates},
	} {
		found, err := c.lookupReference(ctx, t.kind, t.table, t.prefix, t.numbers)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

// lookupReference queries one document table (quote or estimate) by number.
// table and prefix are package constants, never request data.
func (c *Checker) lookupReference(ctx context.Context, kind, table, prefix string, numbers []string) ([]Duplicate, error) {
	rows, err := c.db.Query(ctx, fmt.Sprintf(`
		SELECT d.%[1]s_uuid::text, COALESCE(d.%[1]s_number, ''), rs.record_status_name, COALESCE(ou.id::text, '')
		FROM %[2]s d
		JOIN lkp_record_status rs ON rs.record_status_id = d.%[1]s_status
		LEFT JOIN employee oe ON oe.employee_id = d.%[1]s_owner_id
		LEFT JOIN users ou ON ou.id = oe.employee_user_id
		WHERE d.%[1]s_deleted_at IS NULL AND upper(d.%[1]s_number) = ANY($1)
		ORDER BY d.%[1]s_id LIMIT $2`, prefix, table), numbers, maxReferenceMatches)
	if err != nil {
		return nil, fmt.Errorf("query %s reference: %w", table, err)
	}
	defer rows.Close()
	var out []Duplicate
	for rows.Next() {
		var d Duplicate
		if err := rows.Scan(&d.RecordUUID, &d.Number, &d.Status, &d.OwnerUserID); err != nil {
			return nil, fmt.Errorf("scan %s reference: %w", table, err)
		}
		d.Kind = kind
		d.Reason = fmt.Sprintf(reasonReference, d.Number, d.Status)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s references: %w", table, err)
	}
	return out, nil
}
