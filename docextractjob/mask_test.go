package docextractjob

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docextract"
)

func TestMaskExtracted(t *testing.T) {
	const card = "card 4111 1111 1111 1111"
	in := docextract.Result{
		Header: docextract.Header{
			PONumber: docextract.Field{Value: "20481937", Snippet: "PO 20481937 " + card},
			Total:    docextract.Field{Value: "12345.67", Snippet: "Total $12,345.67"},
			Notes:    docextract.Field{Value: "Routing: 021000021", Snippet: "Routing: 021000021"},
			BillTo:   docextract.Field{Value: "Acme " + card},
		},
		Lines: []docextract.Line{{
			SKU:    docextract.Field{Value: "1234567890123", Snippet: "1234567890123"},
			Qty:    docextract.Field{Value: "1250"},
			Detail: "pay " + card,
		}},
		Pages: []docextract.PageRows{{Page: 1, Rows: []docextract.Row{{Words: []docextract.Word{
			{Text: "4111111111111111"}, {Text: "20481937"},
		}}}}},
	}
	out := maskExtracted(in)

	assert.Equal(t, "20481937", out.Header.PONumber.Value)
	assert.Equal(t, "12345.67", out.Header.Total.Value)
	assert.Equal(t, "1234567890123", out.Lines[0].SKU.Value)
	assert.Equal(t, "1234567890123", out.Lines[0].SKU.Snippet)
	assert.Equal(t, "1250", out.Lines[0].Qty.Value)
	assert.Equal(t, "PO 20481937 card [masked]", out.Header.PONumber.Snippet)
	assert.Equal(t, "Routing: [masked]", out.Header.Notes.Value)
	assert.Equal(t, "Acme card [masked]", out.Header.BillTo.Value)
	assert.Equal(t, "pay card [masked]", out.Lines[0].Detail)
	assert.Equal(t, "[masked]", out.Pages[0].Rows[0].Words[0].Text)
	assert.Equal(t, "20481937", out.Pages[0].Rows[0].Words[1].Text)

	// input (cache) must not be mutated
	assert.Equal(t, "4111111111111111", in.Pages[0].Rows[0].Words[0].Text)
	assert.Equal(t, "pay "+card, in.Lines[0].Detail)
}
