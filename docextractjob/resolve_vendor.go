package docextractjob

import (
	"context"
	"fmt"

	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
	"stonesuite-backend/query"
)

// Vendor resolution constants.
const (
	partyKindVendor      = "vendor"
	vendorStatusActive   = "ACT_" // lkp_record_status code of a usable vendor (see vendors/store.go)
	recordTypeVendorCode = "VNDR"
)

// vendorNameSQL is a vendor's name as the duplicate check sees it (a Person's
// given + family name, an Organization's legal name). It mirrors
// vendors.vendorNameSQL; alias `v` is the vendor table.
const vendorNameSQL = `CASE WHEN v.vendor_type = 'Person'
	THEN concat_ws(' ', NULLIF(btrim(v.vendor_given_name), ''), NULLIF(btrim(v.vendor_family_name), ''))
	ELSE v.vendor_legal_name END`

// vendorSelect picks live vendor rows with their status; it is the vendor twin
// of customerSelect.
const vendorSelect = `
	SELECT v.vendor_uuid::text, ` + vendorNameSQL + `, rs.record_status_code, rs.record_status_name
	FROM vendor v
	JOIN lkp_record_type rt ON rt.record_type_id = v.record_type AND rt.record_type_code = '` + recordTypeVendorCode + `'
	JOIN lkp_record_status rs ON rs.record_status_id = v.vendor_status
	WHERE v.vendor_deleted_at IS NULL AND `

// vendorRow is one scanned vendorSelect row.
type vendorRow struct {
	uuid, name, statusCode, statusName string
}

// asParty converts the row to the shared party row. Only an Active vendor is
// usable, so any other status code is carried through to mark it inactive.
func (v vendorRow) asParty() customerRow {
	code := v.statusCode
	if code == vendorStatusActive {
		code = ""
	}
	return customerRow{uuid: v.uuid, name: v.name, statusCode: code, statusName: v.statusName}
}

// queryVendors runs vendorSelect with one extra condition.
func (r *Resolver) queryVendors(ctx context.Context, cond string, args ...any) ([]customerRow, error) {
	rows, err := r.db.Query(ctx, vendorSelect+cond, args...)
	if err != nil {
		return nil, fmt.Errorf("query vendors: %w", err)
	}
	defer rows.Close()
	var out []customerRow
	for rows.Next() {
		var v vendorRow
		if err := rows.Scan(&v.uuid, &v.name, &v.statusCode, &v.statusName); err != nil {
			return nil, fmt.Errorf("scan vendor: %w", err)
		}
		out = append(out, v.asParty())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendors: %w", err)
	}
	return out, nil
}

// ResolveVendor maps a document's vendor text onto a vendor: learned alias
// first, then an exact normalized-name match, then a prefiltered token-Dice
// ranking (top 3). An unmatched or own-company name returns an unresolved match.
func (r *Resolver) ResolveVendor(ctx context.Context, docName string) (PartyMatch, error) {
	none := PartyMatch{Confidence: docextract.ConfNotFound, Source: docextract.SourceDocument}
	key := duplicate.Key(docName)
	if key == "" || r.isOwnCompany(docName) {
		return none, nil
	}

	hit, err := r.queryVendors(ctx,
		`v.vendor_uuid = (SELECT party_uuid FROM document_party_alias WHERE party_kind = $1 AND alias_key = $2)`,
		partyKindVendor, key)
	if err != nil {
		return none, err
	}
	if len(hit) == 1 {
		return matchOf(hit[0], docextract.ConfHigh, docextract.SourceLearned), nil
	}

	exact, err := r.queryVendors(ctx,
		duplicate.NormalizeSQL(vendorNameSQL)+` = $1 ORDER BY v.vendor_id LIMIT $2`, key, maxExactMatches)
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
	cands, err := r.queryVendors(ctx,
		`(`+vendorNameSQL+` ILIKE $1 OR `+vendorNameSQL+` ILIKE $2) ORDER BY v.vendor_id LIMIT $3`,
		tok+"%", "% "+tok+"%", maxCustomerCandidates)
	if err != nil {
		return none, err
	}
	return r.ambiguous(docName, r.withoutOwnCompany(cands)), nil
}
