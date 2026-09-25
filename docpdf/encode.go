package docpdf

import (
	"strings"
	"unicode/utf8"

	"github.com/go-pdf/fpdf"
)

// Windows-1252 bytes the layout emits itself. They are written after
// toCP1252 has run, so they must already be in the target encoding.
const (
	emDash = "\x97"
	enDash = "\x96"
)

// unmappedMark stands in for a character Windows-1252 cannot represent.
// fpdf's own translator substitutes "." for those, which silently reads as
// punctuation; a question mark at least looks like a missing glyph.
const unmappedMark = "?"

// cp1252 converts UTF-8 text to the Windows-1252 bytes fpdf's built-in fonts
// expect. Without it "Café" prints as "CafÃ©" and an em dash as "â€"".
type cp1252 struct {
	translate func(string) string
}

// str converts s; pure-ASCII strings (the common case) are returned as-is.
func (c cp1252) str(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch alt, ok := altText(r); {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case ok:
			b.WriteString(alt)
		default:
			t := c.translate(string(r))
			if t == "." { // fpdf's "not in the code page" substitute
				t = unmappedMark
			}
			b.WriteString(t)
		}
	}
	return b.String()
}

// altText spells out symbols that real documents use but Windows-1252 lacks.
func altText(r rune) (string, bool) {
	if r == '₹' {
		return "Rs.", true
	}
	return "", false
}

// address converts every field of an Address.
func (c cp1252) address(a Address) Address {
	return Address{
		Name:         c.str(a.Name),
		Attention:    c.str(a.Attention),
		Line1:        c.str(a.Line1),
		Line2:        c.str(a.Line2),
		CityStateZip: c.str(a.CityStateZip),
		Phone:        c.str(a.Phone),
		Email:        c.str(a.Email),
	}
}

// toCP1252 returns a copy of d with all its text converted for fpdf's
// built-in fonts. The caller's document (including its Lines slice) is left
// untouched. Everything text-valued is converted here, once, so the drawing
// code never has to think about encodings.
func toCP1252(pdf *fpdf.Fpdf, d PrintableDoc) PrintableDoc {
	c := cp1252{translate: pdf.UnicodeTranslatorFromDescriptor("")}
	d.Seller = Seller{
		Name:         c.str(d.Seller.Name),
		AddrLine1:    c.str(d.Seller.AddrLine1),
		AddrLine2:    c.str(d.Seller.AddrLine2),
		CityStateZip: c.str(d.Seller.CityStateZip),
		Phone:        c.str(d.Seller.Phone),
		Email:        c.str(d.Seller.Email),
		LogoPNG:      d.Seller.LogoPNG,
		Payment: PaymentDetails{
			BankName:      c.str(d.Seller.Payment.BankName),
			AccountNumber: c.str(d.Seller.Payment.AccountNumber),
			RoutingNumber: c.str(d.Seller.Payment.RoutingNumber),
		},
	}
	d.Kind = c.str(d.Kind)
	d.Number = c.str(d.Number)
	d.Status = c.str(d.Status)
	d.IssueDate = c.str(d.IssueDate)
	d.DueDate = c.str(d.DueDate)
	d.BillTo = c.address(d.BillTo)
	d.ShipTo = c.address(d.ShipTo)
	lines := make([]PrintLine, len(d.Lines))
	for i, ln := range d.Lines {
		ln.SKU = c.str(ln.SKU)
		ln.Name = c.str(ln.Name)
		ln.Description = c.str(ln.Description)
		ln.UnitCode = c.str(ln.UnitCode)
		lines[i] = ln
	}
	d.Lines = lines
	d.CurrencySymbol = c.str(d.CurrencySymbol)
	d.Terms = c.str(d.Terms)
	d.Notes = c.str(d.Notes)
	d.Memo = c.str(d.Memo)
	return d
}
