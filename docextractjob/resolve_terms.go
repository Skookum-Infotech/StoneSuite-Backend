package docextractjob

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/duplicate"
)

// maxTermsMatches bounds the terms lookup; more than one match is ambiguous.
const maxTermsMatches = 2

// ResolvePaymentTerms matches the document's terms text ("Net 30") to an
// active payment term by name, ignoring case and spacing. No match, or an
// ambiguous one, returns nil.
func (r *Resolver) ResolvePaymentTerms(ctx context.Context, docTerms string) (*TermsMatch, error) {
	key := duplicate.Key(docTerms)
	if key == "" {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT payment_terms_id, payment_terms_name, payment_terms_code
		FROM lkp_payment_terms
		WHERE payment_terms_deleted_at IS NULL AND payment_terms_is_active
		  AND (`+duplicate.NormalizeSQL("payment_terms_name")+` = $1
		       OR replace(lower(payment_terms_name), ' ', '') = $2)
		ORDER BY payment_terms_id LIMIT $3`,
		key, strings.ReplaceAll(key, " ", ""), maxTermsMatches)
	if err != nil {
		return nil, fmt.Errorf("query payment terms: %w", err)
	}
	defer rows.Close()
	var found []TermsMatch
	for rows.Next() {
		var t TermsMatch
		if err := rows.Scan(&t.ID, &t.Name, &t.Code); err != nil {
			return nil, fmt.Errorf("scan payment terms: %w", err)
		}
		found = append(found, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payment terms: %w", err)
	}
	if len(found) != 1 {
		return nil, nil
	}
	return &found[0], nil
}
