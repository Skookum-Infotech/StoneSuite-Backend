package docextractjob

import (
	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
)

// partyLearning is the party side of a completed document: which kind of party
// it is, what the document called it, what the extraction resolved it to and
// what the user saved.
type partyLearning struct {
	kind      string // document_party_alias.party_kind
	field     string // feedback field name
	docName   string
	resolved  PartyMatch
	savedUUID string
}

// partyOf picks the party of the extraction's document type: the vendor for a
// purchase order or vendor bill, the customer otherwise. A malformed saved uuid
// is dropped so it never reaches a ::uuid cast.
func partyOf(res ResultDoc, saved SavedValues) partyLearning {
	p := partyLearning{
		kind: partyKindCustomer, field: FieldCustomer,
		docName: res.Extracted.Header.CustomerName.Value, resolved: res.Resolution.Customer,
		savedUUID: saved.CustomerUUID,
	}
	switch res.Extracted.DocType {
	case docextract.DocTypePurchaseOrder, docextract.DocTypeVendorBill:
		p = partyLearning{
			kind: partyKindVendor, field: FieldVendor,
			docName: res.Extracted.Header.VendorName.Value, resolved: res.Resolution.Vendor,
			savedUUID: saved.VendorUUID,
		}
	}
	if !validUUID(p.savedUUID) {
		p.savedUUID = ""
	}
	return p
}

// learnParty reports whether the saved party is worth remembering as an alias
// of the document's party text: a correction, a mapping that was already
// learned (to bump its hit count), or a confirmed fuzzy pre-pick. An exact
// auto-match teaches nothing.
func learnParty(p partyLearning) bool {
	if !validUUID(p.savedUUID) || duplicate.Key(p.docName) == "" {
		return false
	}
	// Learn a correction, refresh a learned alias, and remember a fuzzy pre-pick
	// the reviewer confirmed, so the same document is a sure match next time.
	c := p.resolved
	return c.UUID != p.savedUUID || c.Source == docextract.SourceLearned || c.Confidence == docextract.ConfCheck
}
