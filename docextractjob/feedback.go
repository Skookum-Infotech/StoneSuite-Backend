package docextractjob

import (
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
)

// Feedback field names (document_extraction_feedback.field).
const (
	FieldCustomer  = "customer"
	FieldPONumber  = "po_number"
	FieldOrderDate = "order_date"
	FieldLineItem  = "line_item"
)

// SavedLine is one saved sales-order line, carrying the document text it came from.
type SavedLine struct {
	DocSKU         string `json:"docSku"`
	DocDescription string `json:"docDescription"`
	ItemUUID       string `json:"itemUuid"`
}

// SavedValues are the values the user actually saved, sent on /complete.
type SavedValues struct {
	CustomerUUID string      `json:"customerUuid"`
	PONumber     string      `json:"poNumber"`
	OrderDate    string      `json:"orderDate"`
	Lines        []SavedLine `json:"lines"`
}

// FeedbackRow is one extracted-vs-final correction. Values are field values
// (ids, a PO number, a date), never document text.
type FeedbackRow struct {
	PartyUUID string
	Field     string
	Extracted string
	Final     string
}

// DiffFeedback lists the fields where the saved values differ from what the
// extraction resolved. Lines the user added by hand (no matching document
// line) are skipped.
func DiffFeedback(res ResultDoc, saved SavedValues) []FeedbackRow {
	party := saved.CustomerUUID
	if party == "" {
		party = res.Resolution.Customer.UUID
	}
	var out []FeedbackRow
	add := func(field, extracted, final string) {
		extracted, final = clip(strings.TrimSpace(extracted)), clip(strings.TrimSpace(final))
		if extracted != final {
			out = append(out, FeedbackRow{PartyUUID: party, Field: field, Extracted: extracted, Final: final})
		}
	}
	add(FieldCustomer, res.Resolution.Customer.UUID, saved.CustomerUUID)
	add(FieldPONumber, res.Extracted.Header.PONumber.Value, saved.PONumber)
	add(FieldOrderDate, res.Extracted.Header.OrderDate.Value, saved.OrderDate)
	for _, sl := range saved.Lines {
		m := findLineMatch(res, sl)
		if m == nil {
			continue
		}
		add(FieldLineItem, matchedItemUUID(m), sl.ItemUUID)
	}
	return out
}

// clip bounds a stored value to docextract.MaxSnippetLen runes.
func clip(s string) string {
	r := []rune(s)
	if len(r) > docextract.MaxSnippetLen {
		return string(r[:docextract.MaxSnippetLen])
	}
	return s
}

// matchedItemUUID is the item a line resolved to, or "".
func matchedItemUUID(m *LineMatch) string {
	if m.Item == nil {
		return ""
	}
	return m.Item.UUID
}

// findLineMatch finds the resolution of the document line a saved line came
// from, by normalized SKU first and then by normalized description.
func findLineMatch(res ResultDoc, sl SavedLine) *LineMatch {
	sku, desc := NormalizeSKU(sl.DocSKU), duplicate.Key(sl.DocDescription)
	if sku == "" && desc == "" {
		return nil
	}
	var byDesc *LineMatch
	for i := range res.Resolution.Lines {
		m := &res.Resolution.Lines[i]
		if m.Index < 0 || m.Index >= len(res.Extracted.Lines) {
			continue
		}
		l := res.Extracted.Lines[m.Index]
		if sku != "" && NormalizeSKU(l.SKU.Value) == sku {
			return m
		}
		if byDesc == nil && desc != "" && duplicate.Key(l.Description.Value) == desc {
			byDesc = m
		}
	}
	return byDesc
}

// learnCustomer reports whether the saved customer is worth remembering as an
// alias of the document's customer text: a correction, a mapping that was
// already learned (to bump its hit count), or a confirmed fuzzy pre-pick. An
// exact auto-match teaches nothing.
func learnCustomer(res ResultDoc, saved SavedValues) bool {
	if !validUUID(saved.CustomerUUID) || duplicate.Key(res.Extracted.Header.CustomerName.Value) == "" {
		return false
	}
	c := res.Resolution.Customer
	// Learn a correction, refresh a learned alias, and remember a fuzzy pre-pick
	// the reviewer confirmed, so the same document is a sure match next time.
	return c.UUID != saved.CustomerUUID || c.Source == docextract.SourceLearned || c.Confidence == docextract.ConfCheck
}

// aliasEntry is one item alias to upsert.
type aliasEntry struct{ key, itemUUID string }

// learnItems returns the item aliases worth remembering: lines whose saved
// item differs from what resolved (a correction or a first-time mapping), or
// that already resolved through an alias (to bump hits). Later lines win a
// duplicate key.
func learnItems(res ResultDoc, saved SavedValues) []aliasEntry {
	index := map[string]int{}
	var out []aliasEntry
	put := func(key, item string) {
		if i, ok := index[key]; ok {
			out[i].itemUUID = item
			return
		}
		index[key] = len(out)
		out = append(out, aliasEntry{key: key, itemUUID: item})
	}
	for _, sl := range saved.Lines {
		if !validUUID(sl.ItemUUID) {
			continue
		}
		if m := findLineMatch(res, sl); m != nil && matchedItemUUID(m) == sl.ItemUUID && m.MatchedBy != MatchAlias {
			continue
		}
		if k := NormalizeSKU(sl.DocSKU); k != "" {
			put(aliasPrefixSKU+k, sl.ItemUUID)
		}
		if k := duplicate.Key(sl.DocDescription); k != "" {
			put(aliasPrefixDesc+k, sl.ItemUUID)
		}
	}
	return out
}
