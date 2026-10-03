package docextractjob

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docextract"
)

const (
	custA = "11111111-1111-4111-8111-111111111111"
	custB = "22222222-2222-4222-8222-222222222222"
	itemA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	itemB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// feedbackResult builds a ResultDoc with one resolved line (SKU "AB-1").
func feedbackResult(custUUID string, matchedBy string, itemUUID string) ResultDoc {
	var item *ItemInfo
	if itemUUID != "" {
		item = &ItemInfo{UUID: itemUUID}
	}
	return ResultDoc{
		Extracted: docextract.Result{
			Header: docextract.Header{
				PONumber:     docextract.Field{Value: "PO-1"},
				OrderDate:    docextract.Field{Value: "2026-01-02"},
				CustomerName: docextract.Field{Value: "ACME Stone"},
			},
			Lines: []docextract.Line{{
				Kind: docextract.KindProduct,
				SKU:  docextract.Field{Value: "AB-1"}, Description: docextract.Field{Value: "Slab one"},
			}},
			LayoutFingerprint: "fp",
		},
		Resolution: Resolution{
			Customer: CustomerMatch{UUID: custUUID},
			Lines:    []LineMatch{{Index: 0, Item: item, MatchedBy: matchedBy}},
		},
	}
}

func TestDiffFeedback(t *testing.T) {
	tests := []struct {
		name  string
		res   ResultDoc
		saved SavedValues
		want  []FeedbackRow
	}{
		{
			name: "nothing changed",
			res:  feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{CustomerUUID: custA, PONumber: "PO-1", OrderDate: "2026-01-02",
				Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: itemA}}},
			want: nil,
		},
		{
			name: "customer corrected",
			res:  feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{CustomerUUID: custB, PONumber: "PO-1", OrderDate: "2026-01-02",
				Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: itemA}}},
			want: []FeedbackRow{{PartyUUID: custB, Field: FieldCustomer, Extracted: custA, Final: custB}},
		},
		{
			name: "po and date corrected, whitespace ignored",
			res:  feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{CustomerUUID: custA, PONumber: " 4471 ", OrderDate: "2026-02-03",
				Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: itemA}}},
			want: []FeedbackRow{
				{PartyUUID: custA, Field: FieldPONumber, Extracted: "PO-1", Final: "4471"},
				{PartyUUID: custA, Field: FieldOrderDate, Extracted: "2026-01-02", Final: "2026-02-03"},
			},
		},
		{
			name: "line item picked for unmatched line",
			res:  feedbackResult(custA, "", ""),
			saved: SavedValues{CustomerUUID: custA, PONumber: "PO-1", OrderDate: "2026-01-02",
				Lines: []SavedLine{{DocSKU: "ab 1", ItemUUID: itemB}}},
			want: []FeedbackRow{{PartyUUID: custA, Field: FieldLineItem, Extracted: "", Final: itemB}},
		},
		{
			name: "line matched by description",
			res:  feedbackResult(custA, MatchName, itemA),
			saved: SavedValues{CustomerUUID: custA, PONumber: "PO-1", OrderDate: "2026-01-02",
				Lines: []SavedLine{{DocDescription: "  SLAB   one ", ItemUUID: itemB}}},
			want: []FeedbackRow{{PartyUUID: custA, Field: FieldLineItem, Extracted: itemA, Final: itemB}},
		},
		{
			name: "hand-added line is skipped",
			res:  feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{CustomerUUID: custA, PONumber: "PO-1", OrderDate: "2026-01-02",
				Lines: []SavedLine{{DocSKU: "ZZ-9", ItemUUID: itemB}}},
			want: nil,
		},
		{
			name: "party falls back to resolved customer",
			res:  feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{PONumber: "9", OrderDate: "2026-01-02",
				Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: itemA}}},
			want: []FeedbackRow{
				{PartyUUID: custA, Field: FieldCustomer, Extracted: custA, Final: ""},
				{PartyUUID: custA, Field: FieldPONumber, Extracted: "PO-1", Final: "9"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, DiffFeedback(tc.res, tc.saved)) })
	}
}

func TestDiffFeedbackClipsValues(t *testing.T) {
	res := feedbackResult(custA, MatchSKU, itemA)
	long := strings.Repeat("x", docextract.MaxSnippetLen+50)
	rows := DiffFeedback(res, SavedValues{CustomerUUID: custA, PONumber: long, OrderDate: "2026-01-02"})
	assert.Len(t, rows, 1)
	assert.Len(t, rows[0].Final, docextract.MaxSnippetLen)
}

func TestLearnCustomer(t *testing.T) {
	learned := feedbackResult(custA, "", "")
	learned.Resolution.Customer.Source = docextract.SourceLearned
	fuzzy := feedbackResult(custA, "", "")
	fuzzy.Resolution.Customer.Confidence = docextract.ConfCheck
	noName := feedbackResult(custA, "", "")
	noName.Extracted.Header.CustomerName.Value = ""
	tests := []struct {
		name  string
		res   ResultDoc
		saved SavedValues
		want  bool
	}{
		{"exact auto-match teaches nothing", feedbackResult(custA, "", ""), SavedValues{CustomerUUID: custA}, false},
		{"correction is learned", feedbackResult(custA, "", ""), SavedValues{CustomerUUID: custB}, true},
		{"unresolved then picked", feedbackResult("", "", ""), SavedValues{CustomerUUID: custB}, true},
		{"learned alias bumps hits", learned, SavedValues{CustomerUUID: custA}, true},
		{"confirmed fuzzy pick is learned", fuzzy, SavedValues{CustomerUUID: custA}, true},
		{"no document name", noName, SavedValues{CustomerUUID: custB}, false},
		{"invalid saved uuid", feedbackResult("", "", ""), SavedValues{CustomerUUID: "nope"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, learnCustomer(tc.res, tc.saved)) })
	}
}

func TestLearnItems(t *testing.T) {
	tests := []struct {
		name  string
		res   ResultDoc
		saved SavedValues
		want  []aliasEntry
	}{
		{
			name:  "exact sku match teaches nothing",
			res:   feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{Lines: []SavedLine{{DocSKU: "AB-1", DocDescription: "Slab one", ItemUUID: itemA}}},
			want:  nil,
		},
		{
			name:  "correction learns sku and description",
			res:   feedbackResult(custA, MatchSKU, itemA),
			saved: SavedValues{Lines: []SavedLine{{DocSKU: "AB-1", DocDescription: "Slab one", ItemUUID: itemB}}},
			want:  []aliasEntry{{key: "sku:ab1", itemUUID: itemB}, {key: "desc:slab one", itemUUID: itemB}},
		},
		{
			name:  "learned alias bumps hits",
			res:   feedbackResult(custA, MatchAlias, itemA),
			saved: SavedValues{Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: itemA}}},
			want:  []aliasEntry{{key: "sku:ab1", itemUUID: itemA}},
		},
		{
			name:  "description only",
			res:   feedbackResult(custA, "", ""),
			saved: SavedValues{Lines: []SavedLine{{DocDescription: "Slab one", ItemUUID: itemB}}},
			want:  []aliasEntry{{key: "desc:slab one", itemUUID: itemB}},
		},
		{
			name: "duplicate key last wins",
			res:  feedbackResult(custA, "", ""),
			saved: SavedValues{Lines: []SavedLine{
				{DocSKU: "AB-1", ItemUUID: itemA}, {DocSKU: "ab 1", ItemUUID: itemB}}},
			want: []aliasEntry{{key: "sku:ab1", itemUUID: itemB}},
		},
		{
			name:  "invalid item uuid skipped",
			res:   feedbackResult(custA, "", ""),
			saved: SavedValues{Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: "x"}}},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, learnItems(tc.res, tc.saved)) })
	}
}
