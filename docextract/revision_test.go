package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectRevision(t *testing.T) {
	tests := []struct {
		name string
		text []string
		want *Revision
	}{
		{"revision number", []string{"Revision 2"}, &Revision{Label: "Revision 2"}},
		{"rev dot letter", []string{"Rev. B"}, &Revision{Label: "Revision B"}},
		{"amended", []string{"AMENDED PURCHASE ORDER"}, &Revision{Label: "Amended"}},
		{"change order", []string{"Change Order # C-3"}, &Revision{Label: "Change Order C-3"}},
		{"supersedes", []string{"Revision 2", "Supersedes PO 4400"}, &Revision{Label: "Revision 2", ReferencedNumber: "4400"}},
		{"replaces without revision", []string{"Replaces order #5512"}, &Revision{Label: "Amended", ReferencedNumber: "5512"}},
		{"review is not rev", []string{"Review 2 of the terms"}, nil},
		{"plain", []string{"PO Number: 123"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rows []Row
			for i, s := range tt.text {
				rows = append(rows, trowSplit(float64(700-i*14), tcell{50, s}))
			}
			assert.Equal(t, tt.want, DetectRevision(pagesOf(rows...)))
		})
	}
}

func TestDetectQuoteRef(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Your Quote # Q-1001", "Q-1001"},
		{"Quotation No. 5588", "5588"},
		{"Estimate: E-77", "E-77"},
		{"Quote date 01/02/2026", ""},
		{"We appreciate your quote request", ""},
		{"Terms: Net 30", ""},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			assert.Equal(t, tt.want, DetectQuoteRef(pagesOf(trowSplit(700, tcell{50, tt.text}))))
		})
	}
}
