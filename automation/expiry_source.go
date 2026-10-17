package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	reasonConverted = "converted"
	// recordStatusSent is the lkp_record_status code of a document that was sent to the customer.
	recordStatusSent = "SENT"
	// userStatusActive is users.status for a workspace user who may receive mail.
	userStatusActive = "active"
	// expiryLookbackDays / expiryLookaheadDays bound the candidate scan around valid_until.
	expiryLookbackDays  = 2
	expiryLookaheadDays = 3
)

// ExpiryDoc is one sent quote or estimate approaching its valid-until date.
// The Owner* fields are empty unless the owning employee resolves to an active user.
type ExpiryDoc struct {
	UUID            string
	Number          string
	CustomerName    string
	OwnerEmail      string
	OwnerUserID     string
	OwnerIdentityID string
	ValidUntil      time.Time
}

// expirySpec names the document table and record type an expiry rule scans.
// Values come only from the package-level specs below, never from input.
type expirySpec struct {
	RuleKey  string
	Resource string
	Table    string
	// TypeCode is the lkp_record_type code of the document.
	TypeCode string
}

var (
	quoteSpec    = expirySpec{RuleKey: RuleKeyQuoteExpiry, Resource: "quote", Table: "quote", TypeCode: "QUOT"}
	estimateSpec = expirySpec{RuleKey: RuleKeyEstimateExpiry, Resource: "estimate", Table: "estimate", TypeCode: "ESTM"}
)

// notConverted is the per-table "still open" predicate: a quote has no
// quote_conversion row; an estimate has no live quote derived from it.
func (s expirySpec) notConverted() string {
	if s.Table == "quote" {
		return `NOT EXISTS (SELECT 1 FROM quote_conversion qc WHERE qc.quote_id = d.quote_id)`
	}
	return `NOT EXISTS (SELECT 1 FROM quote q WHERE q.quote_estimate_id = d.estimate_id AND q.quote_deleted_at IS NULL)`
}

// selectSQL builds the shared projection and joins for one spec.
func (s expirySpec) selectSQL() string {
	t := s.Table
	r := strings.NewReplacer("{t}", t)
	return r.Replace(`
SELECT d.{t}_uuid::text, COALESCE(d.{t}_number, ''), c.customer_name,
       COALESCE(u.email, ''), COALESCE(u.id::text, ''), COALESCE(u.identity_id::text, ''),
       d.{t}_valid_until, rs.record_status_code, (d.{t}_deleted_at IS NOT NULL),
       COALESCE(u.status, ''), ` + s.notConverted() + `
FROM {t} d
JOIN customer c ON c.customer_id = d.{t}_customer_id
JOIN lkp_record_status rs ON rs.record_status_id = d.{t}_status
JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type AND rt.record_type_code = $1
LEFT JOIN employee e ON e.employee_id = d.{t}_owner_id AND e.employee_deleted_at IS NULL
LEFT JOIN users u ON u.id = e.employee_user_id AND u.status = '` + userStatusActive + `'
`)
}

// listExpiryCandidates returns sent, unconverted, live documents whose
// valid-until date falls within [today-2d, today+3d].
func listExpiryCandidates(ctx context.Context, q Querier, spec expirySpec, today time.Time) ([]ExpiryDoc, error) {
	t := spec.Table
	where := strings.NewReplacer("{t}", t).Replace(`
WHERE rs.record_status_code = $2
  AND d.{t}_valid_until IS NOT NULL
  AND d.{t}_valid_until BETWEEN $3::date - $4::int AND $3::date + $5::int
  AND d.{t}_deleted_at IS NULL
  AND c.customer_deleted_at IS NULL
  AND ` + spec.notConverted() + `
ORDER BY d.{t}_valid_until, d.{t}_id`)
	rows, err := q.Query(ctx, spec.selectSQL()+where,
		spec.TypeCode, recordStatusSent, today, expiryLookbackDays, expiryLookaheadDays)
	if err != nil {
		return nil, fmt.Errorf("list %s expiry candidates: %w", spec.Resource, err)
	}
	defer rows.Close()
	var out []ExpiryDoc
	for rows.Next() {
		var d ExpiryDoc
		var validUntil *time.Time
		var status, userStatus string
		var deleted, open bool
		if err := rows.Scan(&d.UUID, &d.Number, &d.CustomerName, &d.OwnerEmail, &d.OwnerUserID,
			&d.OwnerIdentityID, &validUntil, &status, &deleted, &userStatus, &open); err != nil {
			return nil, fmt.Errorf("scan %s expiry candidate: %w", spec.Resource, err)
		}
		if validUntil != nil {
			d.ValidUntil = *validUntil
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s expiry candidates: %w", spec.Resource, err)
	}
	return out, nil
}

// expiryEligible re-checks one document live at send time. reason "" means
// eligible; precedence: voided, converted, not_sent, anchor_changed, no_email.
func expiryEligible(ctx context.Context, q Querier, spec expirySpec, uuid string, plannedValidUntil time.Time) (ok bool, reason string, live ExpiryDoc, err error) {
	var status, userStatus string
	var deleted, open bool
	var validUntil *time.Time
	row := q.QueryRow(ctx, spec.selectSQL()+` WHERE d.`+spec.Table+`_uuid = $2::uuid`, spec.TypeCode, uuid)
	scanErr := row.Scan(&live.UUID, &live.Number, &live.CustomerName, &live.OwnerEmail, &live.OwnerUserID,
		&live.OwnerIdentityID, &validUntil, &status, &deleted, &userStatus, &open)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return false, reasonVoided, ExpiryDoc{}, nil
	}
	if scanErr != nil {
		return false, "", ExpiryDoc{}, fmt.Errorf("%s expiry eligibility %s: %w", spec.Resource, uuid, scanErr)
	}
	if validUntil != nil {
		live.ValidUntil = *validUntil
	}
	reason = expiryReason(deleted, open, status, validUntil, plannedValidUntil, live.OwnerEmail, userStatus)
	return reason == "", reason, live, nil
}

// expiryReason maps live facts to the first matching skip reason ("" = eligible).
func expiryReason(deleted, open bool, status string, validUntil *time.Time, planned time.Time, email, userStatus string) string {
	switch {
	case deleted:
		return reasonVoided
	case !open:
		return reasonConverted
	case status != recordStatusSent:
		return reasonNotSent
	case validUntil == nil || !sameDay(*validUntil, planned):
		return reasonAnchorChanged
	case email == "" || userStatus != userStatusActive:
		return reasonNoEmail
	}
	return ""
}
