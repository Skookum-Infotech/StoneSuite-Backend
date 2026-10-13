package docpdf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kitLine(kit string) PrintLine {
	return PrintLine{Name: "Slab", Quantity: 1, UnitPrice: 10, LineTotal: 10, KitName: kit}
}

// endY draws the document's line table from a fixed start and returns where the
// cursor ends, so two documents can be compared by how much room they took.
func endY(d PrintableDoc) (y float64, pages int) {
	pdf := newDoc()
	pdf.SetY(40)
	drawLineTable(pdf, d)
	return pdf.GetY(), pdf.PageNo()
}

func TestDrawLineTable_KitHeadingAddsOneRowPerGroup(t *testing.T) {
	plain, _ := endY(PrintableDoc{Lines: []PrintLine{kitLine(""), kitLine(""), kitLine(""), kitLine("")}})
	kitted, _ := endY(PrintableDoc{Lines: []PrintLine{
		kitLine("Kitchen"), kitLine("Kitchen"), kitLine("Bathroom"), kitLine(""),
	}})

	assert.InDelta(t, plain+2*kitHeadH, kitted, 0.001, "two kits, two heading rows — and no row for the kit-less line")
}

func TestDrawLineTable_SameKitTwiceInARowIsOneGroup(t *testing.T) {
	one, _ := endY(PrintableDoc{Lines: []PrintLine{kitLine("Kitchen"), kitLine("Kitchen"), kitLine("Kitchen")}})
	none, _ := endY(PrintableDoc{Lines: []PrintLine{kitLine(""), kitLine(""), kitLine("")}})

	assert.InDelta(t, none+kitHeadH, one, 0.001)
}

func TestDrawLineTable_KitAfterPlainLinesStartsANewGroup(t *testing.T) {
	// plain, kit, plain, kit — the second kit line is a new group, not a
	// continuation, because a kit-less line came between.
	got, _ := endY(PrintableDoc{Lines: []PrintLine{kitLine(""), kitLine("Kitchen"), kitLine(""), kitLine("Kitchen")}})
	none, _ := endY(PrintableDoc{Lines: []PrintLine{kitLine(""), kitLine(""), kitLine(""), kitLine("")}})

	assert.InDelta(t, none+2*kitHeadH, got, 0.001)
}

// A heading is kept with the first line of its group: where the line alone would
// still fit on the page but the heading and the line would not, both move over.
func TestDrawLineTable_HeadingIsNeverStrandedAtThePageFoot(t *testing.T) {
	pdf := newDoc()
	h := rowHeight(pdf, kitLine(""))
	room := h + kitHeadH/2 // fits the line, not the heading as well
	require.Less(t, room, h+kitHeadH)

	// The table's own header is drawn first, so it comes off the room too.
	start := pageBottomY - tableHeadH - room
	plain := newDoc()
	plain.SetY(start)
	drawLineTable(plain, PrintableDoc{Lines: []PrintLine{kitLine("")}})
	assert.Equal(t, 1, plain.PageNo(), "a kit-less line fits where it started")

	kitted := newDoc()
	kitted.SetY(start)
	drawLineTable(kitted, PrintableDoc{Lines: []PrintLine{kitLine("Kitchen")}})
	assert.Equal(t, 2, kitted.PageNo(), "the heading takes its line to the next page with it")
}

func TestDrawLineTable_AKitRunningAcrossPagesStillRenders(t *testing.T) {
	lines := make([]PrintLine, 80)
	for i := range lines {
		lines[i] = kitLine("Kitchen")
	}
	d := PrintableDoc{Kind: "QUOTE", Number: "Q-1", CurrencySymbol: "$", Lines: lines}

	_, pages := endY(d)
	assert.GreaterOrEqual(t, pages, 2, "eighty lines run onto a second page")

	out, err := Render(d)
	require.NoError(t, err)
	assert.True(t, len(out) > 1000 && string(out[:5]) == "%PDF-", "the document still renders to a PDF")
}
