package docextractjob

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docextract"
)

func TestClassifySalesOrders(t *testing.T) {
	open := soCandidate{uuid: "u1", number: "SORD-000088", po: "PO-4471", statusCode: "OPEN", statusName: "Open", totalCents: 12000, sameCustomer: true}
	closed := soCandidate{uuid: "u2", number: "SORD-000042", po: "INV-0042", statusCode: "FILL", statusName: "Filled", totalCents: 500, sameCustomer: true}
	otherCust := soCandidate{uuid: "u3", number: "SORD-000090", po: "4471", statusCode: "OPEN", statusName: "Open", sameCustomer: false}
	rev := &docextract.Revision{Label: "Revision 2", ReferencedNumber: "SORD-000088"}

	tests := []struct {
		name         string
		cands        []soCandidate
		in           DuplicateInput
		poKey, refKy string
		wantKinds    []string
		wantReason   string
	}{
		{"same po normalized", []soCandidate{open}, DuplicateInput{PONumber: "#4471"}, "4471", "",
			[]string{DupSamePO}, "Possible duplicate of SORD-000088 (same PO number)"},
		{"same po and total", []soCandidate{open}, DuplicateInput{PONumber: "4471", TotalCents: 12000, HasTotal: true}, "4471", "",
			[]string{DupSamePO}, "Possible duplicate of SORD-000088 (same PO number, same total)"},
		{"different total still flagged without total claim", []soCandidate{open}, DuplicateInput{PONumber: "4471", TotalCents: 1, HasTotal: true}, "4471", "",
			[]string{DupSamePO}, "Possible duplicate of SORD-000088 (same PO number)"},
		{"other customer never matches", []soCandidate{otherCust}, DuplicateInput{PONumber: "4471"}, "4471", "", nil, ""},
		{"different po", []soCandidate{open}, DuplicateInput{PONumber: "9999"}, "9999", "", nil, ""},
		{"revision of open order by referenced number", []soCandidate{open}, DuplicateInput{PONumber: "5000", Revision: rev}, "5000",
			docextract.NormalizeDocNumber("SORD-000088"), []string{DupRevision}, "This looks like Revision 2 of SORD-000088 (Open)"},
		{"revision matched by po is not also same_po", []soCandidate{open}, DuplicateInput{PONumber: "4471", Revision: rev}, "4471", "",
			[]string{DupRevision}, "This looks like Revision 2 of SORD-000088 (Open)"},
		{"revision of closed order says manual", []soCandidate{closed}, DuplicateInput{Revision: &docextract.Revision{Label: "Amended", ReferencedNumber: "SORD-000042"}},
			"", docextract.NormalizeDocNumber("SORD-000042"), []string{DupRevision},
			"This looks like Amended of SORD-000042 (Filled). The order is closed, so the revision must be handled manually (credit memo or change)"},
		{"referenced number matches any customer by order number", []soCandidate{otherCust}, DuplicateInput{Revision: &docextract.Revision{Label: "Amended", ReferencedNumber: "SORD-000090"}},
			"", docextract.NormalizeDocNumber("SORD-000090"), []string{DupRevision}, "This looks like Amended of SORD-000090 (Open)"},
		{"empty keys match nothing", []soCandidate{open}, DuplicateInput{}, "", "", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySalesOrders(tc.cands, tc.in, tc.poKey, tc.refKy)
			var kinds []string
			for _, d := range got {
				kinds = append(kinds, d.Kind)
			}
			assert.Equal(t, tc.wantKinds, kinds)
			if len(got) > 0 {
				assert.Equal(t, tc.wantReason, got[0].Reason)
				assert.NotEmpty(t, got[0].RecordUUID)
			}
		})
	}
}

func TestReferenceNumbers(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		quotes  []string
		estimts []string
	}{
		{"system format", "quot-000012", []string{"QUOT-000012"}, []string{"QUOT-000012"}},
		{"bare number expands", "1001", []string{"1001", "QUOT-001001"}, []string{"1001", "ESTM-001001"}},
		{"alnum kept", " Q-77 ", []string{"Q-77"}, []string{"Q-77"}},
		{"empty", "  ", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, e := referenceNumbers(tc.in)
			assert.Equal(t, tc.quotes, q)
			assert.Equal(t, tc.estimts, e)
		})
	}
}
