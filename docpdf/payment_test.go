package docpdf

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var streamRE = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)

// pdfText returns every stream of the PDF, inflated where it is compressed, so
// a test can look for text that was drawn. Binary streams (images) come back
// too; they never contain the plain-text markers the tests search for.
func pdfText(out []byte) string {
	var sb strings.Builder
	for _, m := range streamRE.FindAllSubmatch(out, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			sb.Write(m[1])
			continue
		}
		if b, err := io.ReadAll(zr); err == nil {
			sb.Write(b)
		}
	}
	return sb.String()
}

var samplePayment = PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"}

func TestPaymentFields(t *testing.T) {
	tests := []struct {
		name string
		in   PaymentDetails
		want []paymentField
	}{
		{
			"all three, in print order", samplePayment,
			[]paymentField{{"BANK NAME", "Chase Bank"}, {"ACCOUNT NUMBER", "000123456789"}, {"WIRE ROUTING NUMBER", "021000021"}},
		},
		{
			"blank fields are left out", PaymentDetails{BankName: "Chase Bank", RoutingNumber: "021000021"},
			[]paymentField{{"BANK NAME", "Chase Bank"}, {"WIRE ROUTING NUMBER", "021000021"}},
		},
		{
			"whitespace-only is blank and values are trimmed", PaymentDetails{BankName: "   ", AccountNumber: " 000123 "},
			[]paymentField{{"ACCOUNT NUMBER", "000123"}},
		},
		{"nothing set", PaymentDetails{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, paymentFields(tc.in))
		})
	}
}

func TestDrawPayment(t *testing.T) {
	tests := []struct {
		name     string
		show     bool
		payment  PaymentDetails
		wantDraw bool
	}{
		{"drawn when the document asks and details exist", true, samplePayment, true},
		{"hidden when the document does not ask", false, samplePayment, false},
		{"hidden when there are no details", true, PaymentDetails{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pdf := newDoc()
			pdf.SetY(100)
			d := sampleDoc()
			d.ShowPayment, d.Seller.Payment = tc.show, tc.payment

			drawPayment(pdf, d)

			want := 100.0
			if tc.wantDraw {
				want += sectionGap + paymentCardH(pdf, paymentFields(tc.payment))
			}
			assert.InDelta(t, want, pdf.GetY(), 0.001)
			assert.Equal(t, 1, pdf.PageNo())
		})
	}
}

func TestDrawPayment_LongValueGrowsTheCard(t *testing.T) {
	pdf := newDoc()
	short := paymentCardH(pdf, paymentFields(samplePayment))
	long := paymentCardH(pdf, paymentFields(PaymentDetails{
		BankName:      "First National Bank of the Greater Metropolitan Area and Surrounding Counties",
		AccountNumber: "GB29NWBK60161331926819",
	}))
	assert.Greater(t, long, short, "a wrapping value needs a taller card")
}

func TestDrawPayment_NewPageWhenItDoesNotFit(t *testing.T) {
	pdf := newDoc()
	pdf.SetY(pageBottomY - 10)
	d := sampleDoc()
	d.ShowPayment, d.Seller.Payment = true, samplePayment

	drawPayment(pdf, d)

	assert.Equal(t, 2, pdf.PageNo())
	assert.InDelta(t, pageMarginTop+paymentCardH(pdf, paymentFields(samplePayment)), pdf.GetY(), 0.001)
}

func TestRender_PaymentDetails(t *testing.T) {
	printed := []string{"PAYMENT DETAILS", "BANK NAME", "Chase Bank", "ACCOUNT NUMBER", "000123456789", "WIRE ROUTING NUMBER", "021000021"}
	tests := []struct {
		name    string
		show    bool
		payment PaymentDetails
		want    bool
	}{
		{"printed on a document that asks for it", true, samplePayment, true},
		{"not printed when the document does not ask", false, samplePayment, false},
		{"not printed when there are no details", true, PaymentDetails{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDoc()
			d.ShowPayment, d.Seller.Payment = tc.show, tc.payment
			out, err := Render(d)
			require.NoError(t, err)
			text := pdfText(out)
			for _, s := range printed {
				if tc.want {
					assert.Contains(t, text, s)
				} else {
					assert.NotContains(t, text, s)
				}
			}
		})
	}
}

func TestRender_PaymentDetailsAreEncodedForThePDF(t *testing.T) {
	d := sampleDoc()
	d.ShowPayment = true
	d.Seller.Payment = PaymentDetails{BankName: "Banco São Paulo", AccountNumber: "1"}
	out, err := Render(d)
	require.NoError(t, err)
	assert.Contains(t, pdfText(out), "Banco S\xe3o Paulo", "non-ASCII bank names are converted to Windows-1252 like the rest of the text")
}

func TestRender_LongDocumentStillEndsWithPaymentDetails(t *testing.T) {
	d := linesDoc(60)
	d.ShowPayment, d.Seller.Payment = true, samplePayment
	out, err := Render(d)
	require.NoError(t, err)
	assert.Contains(t, pdfText(out), "021000021")
	assert.GreaterOrEqual(t, len(pageObjRE.FindAll(out, -1)), 2)
}
