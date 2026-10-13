package docextract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract/internal/pdftest"
)

// ownName is the tenant's own company in every vendor-side test.
const ownName = "Elevation Stone"

// prow is one text row of a test document.
type prow struct {
	y     float64
	cells []pdftest.Cell
}

func at(y float64, cells ...pdftest.Cell) prow { return prow{y: y, cells: cells} }

func c(x float64, s string) pdftest.Cell { return pdftest.Cell{X: x, S: s} }

// tableRows is a one-product line table starting at y, then a Total row.
func tableRows(y float64, desc, amount string) []prow {
	return []prow{
		at(y, c(50, "Item"), c(110, "Description"), pdftest.Right(350, "Qty"), pdftest.Right(460, "Unit Price"), pdftest.Right(545, "Amount")),
		at(y-16, c(50, "A-1"), c(110, desc), pdftest.Right(350, "1"), pdftest.Right(460, amount), pdftest.Right(545, amount)),
	}
}

// docPDF renders rows as a one-page PDF.
func docPDF(groups ...[]prow) []byte {
	var p pdftest.Page
	for _, g := range groups {
		for _, r := range g {
			p.Add(r.y, r.cells...)
		}
	}
	return pdftest.Build([]pdftest.Page{p}, pdftest.Options{})
}

func rows(rs ...prow) []prow { return rs }

// extractDoc runs Extract and fails the test on an input error.
func extractDoc(t *testing.T, b []byte, dt DocType, llm LLM) Result {
	t.Helper()
	res, _, err := Extract(context.Background(), b, dt, Options{OwnCompanyNames: []string{ownName}, LLM: llm, ReconcileFn: noReconcile})
	require.NoError(t, err)
	return res
}

// ownBlockRows is our company as a three-line block at x, starting at y.
func ownBlockRows(x, y float64) []prow {
	return rows(at(y, c(x, ownName)), at(y-14, c(x, "2200 Foundry Road")), at(y-28, c(x, "Austin, TX 78702")))
}

func pdftestRight(rightX float64, s string) pdftest.Cell { return pdftest.Right(rightX, s) }
