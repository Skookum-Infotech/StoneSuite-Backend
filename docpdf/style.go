package docpdf

import "github.com/go-pdf/fpdf"

const (
	pageW       = 210.0 // A4 width, mm
	marginX     = 15.0
	contentW    = pageW - 2*marginX
	pageBottomY = 270.0 // A4 height 297mm minus footer zone
	pageFooterY = 282.0 // hairline above the running footer
	fontFace    = "Helvetica"
)

// sectionGap is the vertical space between page sections: below the letterhead,
// below the parties (before the line table), below the line table (before the
// totals and terms) and below those (before the payment details). It is one
// value on purpose, so the rhythm holds however long the table grows.
const sectionGap = 6.0

// colour is a 24-bit 0xRRGGBB value. Typed constants keep the palette
// immutable, which a package-level struct would not.
type colour uint32

// Palette shared with the app's exported PDFs (pdfBranding.ts in the frontend)
// and the branded emails (services/email_banner.go): the Tailwind "stone"
// neutrals plus the StoneSuite lime.
const (
	colInk      colour = 0x1c1917 // stone-900: masthead, table header, headline total
	colText     colour = 0x292524 // stone-800: body text
	colMuted    colour = 0x57534e // stone-600: secondary text
	colSubtle   colour = 0x78716c // stone-500: small labels
	colFaint    colour = 0xa8a29e // stone-400: meta labels
	colHairline colour = 0xe7e5e4 // stone-200: rules and borders
	colTint     colour = 0xf5f5f4 // stone-100: cards and zebra rows
	colWhite    colour = 0xffffff
	colLime     colour = 0xc2f589 // StoneSuite lime, for use on the dark ink
	colLimeDeep colour = 0x84cc16 // lime accent for use on white
)

// rgb splits the colour into its 8-bit channels.
func (c colour) rgb() (r, g, b int) {
	return int(c >> 16 & 0xff), int(c >> 8 & 0xff), int(c & 0xff)
}

// setText sets the text colour.
func setText(pdf *fpdf.Fpdf, c colour) {
	r, g, b := c.rgb()
	pdf.SetTextColor(r, g, b)
}

// setFill sets the fill colour.
func setFill(pdf *fpdf.Fpdf, c colour) {
	r, g, b := c.rgb()
	pdf.SetFillColor(r, g, b)
}

// setDraw sets the line/border colour.
func setDraw(pdf *fpdf.Fpdf, c colour) {
	r, g, b := c.rgb()
	pdf.SetDrawColor(r, g, b)
}
