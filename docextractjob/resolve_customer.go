package docextractjob

import (
	"context"
	"fmt"

	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// Customer resolution tuning.
const (
	maxCustomerCandidates = 50   // prefilter LIMIT
	topCandidates         = 3    // candidates returned to the reviewer
	fuzzyPickScore        = 0.85 // top Dice score needed to pre-select a fuzzy match
	fuzzyPickGap          = 0.15 // and its lead over the runner-up
	maxExactMatches       = 5
	partyKindCustomer     = "customer"
	recordTypeCustomer    = "CUST"
)

// customerSelect picks the customer rows (CUST stage only, live) with the
// status needed to decide whether the customer is usable on a new record.
const customerSelect = `
	SELECT c.customer_uuid::text, c.customer_name, COALESCE(cs.crm_status_code, ''), COALESCE(cs.crm_status_name, '')
	FROM customer c
	JOIN lkp_record_type rt ON rt.record_type_id = c.record_type AND rt.record_type_code = '` + recordTypeCustomer + `'
	LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status
	WHERE c.customer_deleted_at IS NULL AND `

// customerRow is one scanned customerSelect row.
type customerRow struct {
	uuid, name, statusCode, statusName string
}

// active mirrors workflow.CustomerNotUsableByUUID: no status or Active is usable.
func (c customerRow) active() bool {
	return c.statusCode == "" || c.statusCode == workflow.CustomerStatusActive
}

// queryCustomers runs customerSelect with one extra condition.
func (r *Resolver) queryCustomers(ctx context.Context, cond string, args ...any) ([]customerRow, error) {
	rows, err := r.db.Query(ctx, customerSelect+cond, args...)
	if err != nil {
		return nil, fmt.Errorf("query customers: %w", err)
	}
	defer rows.Close()
	var out []customerRow
	for rows.Next() {
		var c customerRow
		if err := rows.Scan(&c.uuid, &c.name, &c.statusCode, &c.statusName); err != nil {
			return nil, fmt.Errorf("scan customer: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate customers: %w", err)
	}
	return out, nil
}

// matchOf builds a resolved CustomerMatch.
func matchOf(c customerRow, conf docextract.Confidence, src docextract.Source) CustomerMatch {
	return CustomerMatch{UUID: c.uuid, Name: c.name, Active: c.active(), StatusName: c.statusName, Confidence: conf, Source: src}
}

// ResolveCustomer maps a document's customer text onto a customer: learned
// alias first, then an exact normalized-name match, then a prefiltered
// token-Dice ranking (top 3). An unmatched or own-company name returns an
// unresolved match.
func (r *Resolver) ResolveCustomer(ctx context.Context, docName string) (CustomerMatch, error) {
	none := CustomerMatch{Confidence: docextract.ConfNotFound, Source: docextract.SourceDocument}
	key := duplicate.Key(docName)
	if key == "" || r.isOwnCompany(docName) {
		return none, nil
	}

	hit, err := r.queryCustomers(ctx,
		`c.customer_uuid = (SELECT party_uuid FROM document_party_alias WHERE party_kind = $1 AND alias_key = $2)`,
		partyKindCustomer, key)
	if err != nil {
		return none, err
	}
	if len(hit) == 1 {
		return matchOf(hit[0], docextract.ConfHigh, docextract.SourceLearned), nil
	}

	exact, err := r.queryCustomers(ctx,
		duplicate.NormalizeSQL("c.customer_name")+` = $1 ORDER BY c.customer_id LIMIT $2`, key, maxExactMatches)
	if err != nil {
		return none, err
	}
	exact = r.withoutOwnCompany(exact)
	if len(exact) == 1 {
		return matchOf(exact[0], docextract.ConfHigh, docextract.SourceDocument), nil
	}
	if len(exact) > 1 {
		return r.ambiguous(docName, exact), nil
	}

	tok := query.StripLikeMeta(prefilterToken(docName))
	if tok == "" {
		return none, nil
	}
	cands, err := r.queryCustomers(ctx,
		`(c.customer_name ILIKE $1 OR c.customer_name ILIKE $2) ORDER BY c.customer_id LIMIT $3`,
		tok+"%", "% "+tok+"%", maxCustomerCandidates)
	if err != nil {
		return none, err
	}
	cands = r.withoutOwnCompany(cands)
	return r.ambiguous(docName, cands), nil
}

// withoutOwnCompany drops rows named like the tenant itself.
func (r *Resolver) withoutOwnCompany(rows []customerRow) []customerRow {
	out := rows[:0:0]
	for _, c := range rows {
		if !r.isOwnCompany(c.name) {
			out = append(out, c)
		}
	}
	return out
}

// ambiguous ranks rows against the document text and either pre-selects a
// clear winner (confidence "check") or returns the top candidates unpicked.
func (r *Resolver) ambiguous(docName string, rows []customerRow) CustomerMatch {
	out := CustomerMatch{Confidence: docextract.ConfNotFound, Source: docextract.SourceDocument}
	names := make([]string, len(rows))
	for i, c := range rows {
		names[i] = c.name
	}
	ranked := RankByDice(docName, names, topCandidates)
	for _, s := range ranked {
		c := rows[s.Index]
		out.Candidates = append(out.Candidates, Candidate{UUID: c.uuid, Name: c.name, Active: c.active(), Score: s.Score})
	}
	if len(ranked) == 0 {
		return out
	}
	top := ranked[0]
	lead := top.Score
	if len(ranked) > 1 {
		lead = top.Score - ranked[1].Score
	}
	if top.Score >= fuzzyPickScore && lead >= fuzzyPickGap {
		c := rows[top.Index]
		picked := matchOf(c, docextract.ConfCheck, docextract.SourceDocument)
		picked.Candidates = out.Candidates
		return picked
	}
	return out
}
