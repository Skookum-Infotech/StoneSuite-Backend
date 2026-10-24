package docextractjob

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"unicode"

	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
)

// Proposed entity kinds.
const (
	ProposedKindCustomer = "customer"
	ProposedKindVendor   = "vendor"
	ProposedKindItem     = "item"
)

// Needs tokens: fields the reviewer must supply before the entity can be created.
const (
	NeedContactEmail = "contactEmail"
	NeedSKU          = "sku"
)

// ProposedReasonNoMatch marks an entity that matched nothing in master data.
const ProposedReasonNoMatch = "no_match"

// Source.Field values of a proposed entity.
const (
	proposedFieldCustomer = "customerName"
	proposedFieldVendor   = "vendorName"
	proposedFieldLines    = "lines"
)

// Sanitization limits (runes) and key shape.
const (
	proposedMaxCustomerName = 100
	proposedMaxVendorName   = 150
	proposedMaxItemName     = 150
	proposedMaxAddress      = 300
	proposedMaxTaxID        = 50
	proposedMaxSKU          = 50
	proposedMaxDescription  = 500
	proposedMaxUoM          = 20

	proposedKeyPrefix   = "pe_"
	proposedKeyHexChars = 12
	proposedKeySep      = "|"
)

// ProposedSource points at where in the document a proposed entity came from.
// Lines are indexes into Extracted.Lines (items only).
type ProposedSource struct {
	Field string `json:"field"`
	Page  int    `json:"page,omitempty"`
	Row   int    `json:"row,omitempty"`
	Lines []int  `json:"lines,omitempty"`
}

// ProposedFields are the optional attributes extracted for a proposed entity.
type ProposedFields struct {
	Address        string `json:"address,omitempty"`
	TaxID          string `json:"taxId,omitempty"`
	SKU            string `json:"sku,omitempty"`
	Description    string `json:"description,omitempty"`
	UoM            string `json:"uom,omitempty"`
	UnitPriceCents int64  `json:"unitPriceCents,omitempty"`
}

// ProposedEntity is a master-data record the document names but the tenant
// does not have, offered to the reviewer for creation.
type ProposedEntity struct {
	Key              string                `json:"key"`
	Kind             string                `json:"kind"`
	Name             string                `json:"name"`
	Fields           ProposedFields        `json:"fields"`
	Confidence       docextract.Confidence `json:"confidence"`
	Source           ProposedSource        `json:"source"`
	UnresolvedReason string                `json:"unresolvedReason"`
	Needs            []string              `json:"needs,omitempty"`
	DocType          string                `json:"docType"`
}

// proposedKey is the stable entity key: "pe_" + 12 hex of sha256(kind|dedupeKey).
func proposedKey(kind, dedupeKey string) string {
	sum := sha256.Sum256([]byte(kind + proposedKeySep + dedupeKey))
	return proposedKeyPrefix + hex.EncodeToString(sum[:])[:proposedKeyHexChars]
}

// proposeWeakScore is the Dice score below which a resolver candidate is too
// dissimilar to be the same party ("Akhila N" for "Akhila Tailors Surat"), so
// the document's party still counts as new and may be proposed.
const proposeWeakScore = 0.5

// unmatchedParty reports whether m is a clean miss: nothing matched, nothing
// ambiguous (every candidate scores below proposeWeakScore), and so safe to
// propose as a new record.
func unmatchedParty(m PartyMatch) bool {
	if m.UUID != "" || m.Confidence != docextract.ConfNotFound {
		return false
	}
	for _, c := range m.Candidates {
		if c.Score >= proposeWeakScore {
			return false
		}
	}
	return true
}

// isOwnName reports whether key equals the key of one of the tenant's own names.
func isOwnName(key string, own []string) bool {
	for _, o := range own {
		if k := duplicate.Key(o); k != "" && k == key {
			return true
		}
	}
	return false
}

// contactLineRe matches a party "name" that is really a contact line
// ("Support: 1800-889-9991", "Tel 555-0100").
var contactLineRe = regexp.MustCompile(`(?i)^\s*(?:support|helpline|help\s*line|customer\s+care|tel|telephone|phone|ph|mobile|fax|e-?mail)\b|\d[\d\s().-]{6,}\d`)

// hasLetter reports whether s holds at least one letter.
func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// plausiblePartyName reports whether name could be a company or person: it has
// a letter and is not a phone or contact line.
func plausiblePartyName(name string) bool { return hasLetter(name) && !contactLineRe.MatchString(name) }

// partyNameLimit is the column width of the party name for kind.
func partyNameLimit(kind string) int {
	if kind == ProposedKindCustomer {
		return proposedMaxCustomerName
	}
	return proposedMaxVendorName
}

// proposeParty builds a customer/vendor proposal, or false when none applies.
func proposeParty(kind, field string, f docextract.Field, m PartyMatch, own []string, docType docextract.DocType, needs []string) (ProposedEntity, bool) {
	if !f.Found() || !unmatchedParty(m) {
		return ProposedEntity{}, false
	}
	name := docextract.Clean(f.Value, partyNameLimit(kind))
	key := duplicate.Key(name)
	if name == "" || key == "" || !plausiblePartyName(name) || isOwnName(key, own) {
		return ProposedEntity{}, false
	}
	return ProposedEntity{
		Key: proposedKey(kind, key), Kind: kind, Name: name,
		Confidence: f.Confidence, Source: ProposedSource{Field: field, Page: f.Page, Row: f.Row},
		UnresolvedReason: ProposedReasonNoMatch, Needs: needs, DocType: string(docType),
	}, true
}

// DeriveProposedEntities lists the customer/vendor/items a document names that
// master data lacks. Matched (even inactive) and ambiguous parties or items,
// and the tenant's own names (own), are never proposed. Pure: no I/O.
func DeriveProposedEntities(extracted docextract.Result, res Resolution, own []string) []ProposedEntity {
	out := []ProposedEntity{}
	switch extracted.DocType {
	case docextract.DocTypeSalesOrder:
		if e, ok := proposeParty(ProposedKindCustomer, proposedFieldCustomer, extracted.Header.CustomerName,
			res.Customer, own, extracted.DocType, []string{NeedContactEmail}); ok {
			out = append(out, e)
		}
	case docextract.DocTypePurchaseOrder, docextract.DocTypeVendorBill:
		if e, ok := proposeParty(ProposedKindVendor, proposedFieldVendor, extracted.Header.VendorName,
			res.Vendor, own, extracted.DocType, nil); ok {
			out = append(out, e)
		}
	}
	return append(out, proposeItems(extracted, res)...)
}

// proposeItems proposes one item per distinct unmatched, unambiguous line.
func proposeItems(extracted docextract.Result, res Resolution) []ProposedEntity {
	matches := make(map[int]LineMatch, len(res.Lines))
	for _, m := range res.Lines {
		matches[m.Index] = m
	}
	var out []ProposedEntity
	byKey := map[string]int{}
	for i, l := range extracted.Lines {
		if !isItemLine(l) {
			continue
		}
		m, ok := matches[i]
		if !ok || m.Item != nil || !hasLineFlag(m.Flags, LineFlagItemUnmatched) || hasLineFlag(m.Flags, LineFlagItemAmbiguous) {
			continue
		}
		sku := docextract.Clean(l.SKU.Value, proposedMaxSKU)
		desc := docextract.Clean(l.Description.Value, proposedMaxDescription)
		if sku == "" && desc == "" {
			continue
		}
		dedupe := aliasPrefixDesc + duplicate.Key(desc)
		if k := NormalizeSKU(sku); k != "" {
			dedupe = aliasPrefixSKU + k
		}
		key := proposedKey(ProposedKindItem, dedupe)
		if at, seen := byKey[key]; seen {
			out[at].Source.Lines = append(out[at].Source.Lines, i)
			continue
		}
		name, conf, src := docextract.Clean(desc, proposedMaxItemName), l.Description.Confidence, l.Description
		if name == "" {
			name, conf, src = sku, l.SKU.Confidence, l.SKU
		}
		if !hasLetter(name) {
			continue // "996819" is an HSN code, not a product name
		}
		e := ProposedEntity{
			Key: key, Kind: ProposedKindItem, Name: name, Confidence: conf,
			Fields: ProposedFields{
				SKU: sku, Description: desc, UoM: docextract.Clean(l.UoM.Value, proposedMaxUoM),
				UnitPriceCents: int64(l.UnitPriceCents),
			},
			Source:           ProposedSource{Field: proposedFieldLines, Page: src.Page, Row: src.Row, Lines: []int{i}},
			UnresolvedReason: ProposedReasonNoMatch, DocType: string(extracted.DocType),
		}
		if sku == "" {
			e.Needs = []string{NeedSKU}
		}
		byKey[key] = len(out)
		out = append(out, e)
	}
	if out == nil {
		return []ProposedEntity{}
	}
	return out
}
