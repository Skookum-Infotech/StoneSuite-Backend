package docpdf

import (
	"bytes"
	_ "embed" // stoneSuiteLogoWhite
	"math"

	"github.com/go-pdf/fpdf"
)

// stoneSuiteLogoWhite is the StoneSuite lockup (lime mark, light wordmark) for
// the dark masthead: the frontend's public/logo-white.png cropped to the
// lockup's visible bounds. A string, not a []byte, so it stays immutable.
//
//go:embed assets/stonesuite-logo-white.png
var stoneSuiteLogoWhite string

// fpdf image registry names.
const (
	imgStoneSuite = "stonesuite-logo"
	imgTenant     = "seller-logo"
)

// Masthead geometry, in mm. The band is full-bleed; everything in it is
// vertically centred. Both logos are drawn directly on the band -- no plate --
// so each is only as legible as its own artwork; the StoneSuite mark and the
// client's logo (see controllers.ClientLogoPNG) are both light-on-dark for
// this reason.
const (
	bandH   = 26.0
	accentH = 1.0 // lime rule under the band

	logoMaxH = 12.0 // both logos scale to fit this box, vertically centred
	logoMaxW = 50.0 // wide enough for a horizontal wordmark logo
	logoGap  = 10.0 // between the StoneSuite logo and the tenant's

	kindY       = 6.5
	kindH       = 8.0
	numberY     = 15.5
	numberH     = 5.0
	kindMaxPt   = 20.0
	kindMinPt   = 12.0
	numberMaxPt = 11.0
	numberMinPt = 8.0
	fontStepPt  = 0.5
	titleGap    = 8.0 // minimum space between the logos and the document title
)

// drawMasthead paints the full-width ink band across the top of the current
// page: the StoneSuite logo and, beside it, the tenant's own logo (when it has
// one), both drawn directly on the band, then the document kind and number at
// the right, with a lime rule underneath. It returns the y just below the
// band.
func drawMasthead(pdf *fpdf.Fpdf, d PrintableDoc) float64 {
	setFill(pdf, colInk)
	pdf.Rect(0, 0, pageW, bandH, "F")
	setFill(pdf, colLime)
	pdf.Rect(0, bandH, pageW, accentH, "F")

	logosEnd, ok := drawLogo(pdf, imgStoneSuite, []byte(stoneSuiteLogoWhite), marginX)
	if !ok {
		logosEnd = drawBrandWordmark(pdf)
	}
	if len(d.Seller.LogoPNG) > 0 {
		if end, ok := drawLogo(pdf, imgTenant, d.Seller.LogoPNG, logosEnd+logoGap); ok {
			logosEnd = end
		}
	}
	drawDocTitle(pdf, d, logosEnd)
	return bandH + accentH
}

// drawLogo draws the PNG directly on the band -- no plate -- its left edge at
// x, scaled to fit logoMaxW x logoMaxH and vertically centred on the band. It
// returns the x where it ends. The bool is false when the bytes are not a
// usable PNG; nothing is drawn then, so a bad upload can never break a PDF.
func drawLogo(pdf *fpdf.Fpdf, name string, data []byte, x float64) (float64, bool) {
	w, h, ok := fitImage(pdf, name, data, logoMaxW, logoMaxH)
	if !ok {
		return x, false
	}
	placeImage(pdf, name, x, (bandH-h)/2, w, h)
	return x + w, true
}

// drawBrandWordmark stands in for the StoneSuite logo, in plain white text on
// the band, and returns the x where it ends. It is drawn only when the embedded
// logo failed to decode -- a build defect, not runtime input -- so the band
// stays branded rather than blank.
func drawBrandWordmark(pdf *fpdf.Fpdf) float64 {
	const wordmark = "STONE SUITE"
	pdf.SetFont(fontFace, "B", 14)
	setText(pdf, colWhite)
	tw := pdf.GetStringWidth(wordmark)
	pdf.SetXY(marginX, (bandH-6)/2)
	pdf.CellFormat(tw, 6, wordmark, "", 0, "L", false, 0, "")
	return marginX + tw
}

// drawDocTitle right-aligns the document kind (white) and number (lime) in the
// band, shrinking each to fit the space the logos leave.
func drawDocTitle(pdf *fpdf.Fpdf, d PrintableDoc, logosEnd float64) {
	avail := pageW - marginX - logosEnd - titleGap

	pdf.SetFont(fontFace, "B", fitFontSize(pdf, d.Kind, "B", kindMaxPt, kindMinPt, avail))
	setText(pdf, colWhite)
	pdf.SetXY(marginX, kindY)
	pdf.CellFormat(contentW, kindH, d.Kind, "", 0, "R", false, 0, "")

	pdf.SetFont(fontFace, "B", fitFontSize(pdf, d.Number, "B", numberMaxPt, numberMinPt, avail))
	setText(pdf, colLime)
	pdf.SetXY(marginX, numberY)
	pdf.CellFormat(contentW, numberH, d.Number, "", 0, "R", false, 0, "")
}

// fitFontSize returns the largest size in [minPt, maxPt], stepping down by
// fontStepPt, at which text in the given style is no wider than maxW mm. It
// returns minPt when even that is too wide.
func fitFontSize(pdf *fpdf.Fpdf, text, style string, maxPt, minPt, maxW float64) float64 {
	for size := maxPt; size > minPt; size -= fontStepPt {
		pdf.SetFont(fontFace, style, size)
		if pdf.GetStringWidth(text) <= maxW {
			return size
		}
	}
	return minPt
}

// fitImage registers png under name and returns the size it draws at when
// scaled to fit within maxW x maxH mm, preserving aspect ratio. ok is false
// when the bytes are not a usable PNG; fpdf's sticky error state (it poisons
// every later call once set) is cleared so the caller can degrade instead.
func fitImage(pdf *fpdf.Fpdf, name string, data []byte, maxW, maxH float64) (w, h float64, ok bool) {
	info := pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(data))
	if pdf.Err() || info == nil {
		pdf.ClearError()
		return 0, 0, false
	}
	iw, ih := info.Extent()
	if iw <= 0 || ih <= 0 {
		return 0, 0, false
	}
	scale := math.Min(maxW/iw, maxH/ih)
	return iw * scale, ih * scale, true
}

// placeImage draws the registered image at (x, y) sized w x h, reporting
// whether it drew; on failure it clears fpdf's error state like fitImage.
func placeImage(pdf *fpdf.Fpdf, name string, x, y, w, h float64) bool {
	pdf.ImageOptions(name, x, y, w, h, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
	if pdf.Err() {
		pdf.ClearError()
		return false
	}
	return true
}
