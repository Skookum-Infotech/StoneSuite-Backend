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
// and the branded emails (services/templates/email.html): the Tailwind "stone"
// neutrals plus the StoneSuite lime.
const (
	colInk      colour = 0x1c1917 // stone-900: table header, headline total
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

// mastheadGradientStops are the masthead band's background, left to right:
// the same four-stop teal-to-ink gradient as the app's own top nav bar and
// the email banner (services/templates/email.html's linear-gradient(135deg,
// #001219 0%,#005f73 15%,#0a2943 52%,#050d1a 100%)), reduced to a horizontal
// blend since a PDF page has no diagonal to run one across.
var mastheadGradientStops = []struct {
	pos    float64 // 0..1 across the band's width
	colour colour
}{
	{0, 0x001219},
	{0.15, 0x005f73},
	{0.52, 0x0a2943},
	{1, 0x050d1a},
}

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

// drawMastheadGradient paints the masthead band's background with
// mastheadGradientStops, one fpdf.LinearGradient per pair of consecutive
// stops (fpdf only blends two colours at a time) so the printed band matches
// the app nav bar and email banner it shares a palette with.
func drawMastheadGradient(pdf *fpdf.Fpdf, w, h float64) {
	for i := 0; i+1 < len(mastheadGradientStops); i++ {
		from, to := mastheadGradientStops[i], mastheadGradientStops[i+1]
		x, segW := from.pos*w, (to.pos-from.pos)*w
		if segW <= 0 {
			continue
		}
		r1, g1, b1 := from.colour.rgb()
		r2, g2, b2 := to.colour.rgb()
		pdf.LinearGradient(x, 0, segW, h, r1, g1, b1, r2, g2, b2, 0, 0.5, 1, 0.5)
	}
}

// setDraw sets the line/border colour.
func setDraw(pdf *fpdf.Fpdf, c colour) {
	r, g, b := c.rgb()
	pdf.SetDrawColor(r, g, b)
}
