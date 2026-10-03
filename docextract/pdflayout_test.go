package docextract

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract/internal/pdftest"
)

func textPage(texts ...pdftest.Cell) pdftest.Page {
	var p pdftest.Page
	for i, c := range texts {
		p.Add(700-float64(i)*20, c)
	}
	return p
}

func inputCode(t *testing.T, err error) FailureCode {
	t.Helper()
	var ie *InputError
	require.True(t, errors.As(err, &ie), "want InputError, got %v", err)
	return ie.Code
}

func TestParsePDF_RoundTrip(t *testing.T) {
	var p pdftest.Page
	p.Add(700, pdftest.Cell{X: 50, S: "PO Number: 4471"}, pdftest.Cell{X: 330, S: "Date: 01/02/2026"})
	p.Add(680, pdftest.Cell{X: 50, S: "Item"}, pdftest.Cell{X: 200, S: "Qty"}, pdftest.Right(400, "$1,234.56"))
	res, err := ParsePDF(context.Background(), pdftest.Build([]pdftest.Page{p}, pdftest.Options{}), Limits{})
	require.NoError(t, err)
	require.Len(t, res.Pages, 1)
	rows := res.Pages[0].Rows
	require.Len(t, rows, 2)
	assert.Greater(t, rows[0].Y, rows[1].Y, "top row first")
	assert.Equal(t, "PO Number: 4471 Date: 01/02/2026", rows[0].Text())
	assert.Equal(t, "Item Qty $1,234.56", rows[1].Text())
	assert.InDelta(t, 50, rows[1].Words[0].X, 0.5)
	assert.Greater(t, rows[1].Words[2].X, rows[1].Words[1].X)
	assert.False(t, res.Signed)
	assert.False(t, res.Restricted)
}

func TestParsePDF_Failures(t *testing.T) {
	good := pdftest.Build([]pdftest.Page{textPage(pdftest.Cell{X: 50, S: "Hello world 123"})}, pdftest.Options{})
	three := pdftest.Build([]pdftest.Page{textPage(pdftest.Cell{X: 50, S: "one 1"}), textPage(pdftest.Cell{X: 50, S: "two 2"}), textPage(pdftest.Cell{X: 50, S: "three 3"})}, pdftest.Options{})
	junk := string([]rune{1, 2, 3, 4, 5, 6, 7, 8, 9 + 0, 11, 12, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25})
	tests := []struct {
		name string
		in   []byte
		lim  Limits
		code FailureCode
	}{
		{"truncated file", good[:len(good)/2], Limits{}, FailCorrupt},
		{"garbage after header", append([]byte("%PDF-1.4\n"), make([]byte, 200)...), Limits{}, FailCorrupt},
		{"page cap enforced before reading", three, Limits{MaxPages: 2}, FailPageCap},
		{"no text layer is scanned", pdftest.Build([]pdftest.Page{{}, {}}, pdftest.Options{}), Limits{}, FailScanned},
		{"user password required", pdftest.Build([]pdftest.Page{textPage(pdftest.Cell{X: 50, S: "Secret 1"})}, pdftest.Options{Encrypt: pdftest.EncryptUserPassword}), Limits{}, FailPasswordProtected},
		{"unsupported cipher", pdftest.Build([]pdftest.Page{textPage(pdftest.Cell{X: 50, S: "Secret 1"})}, pdftest.Options{Encrypt: pdftest.EncryptUnsupported}), Limits{}, FailUnsupportedEncryption},
		{"mojibake text layer", pdftest.Build([]pdftest.Page{textPage(pdftest.Cell{X: 50, S: junk}, pdftest.Cell{X: 50, S: junk})}, pdftest.Options{}), Limits{}, FailUnreadableText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePDF(context.Background(), tt.in, tt.lim)
			require.Error(t, err)
			assert.Equal(t, tt.code, inputCode(t, err))
		})
	}
}

func TestParsePDF_RestrictedAndSigned(t *testing.T) {
	pages := []pdftest.Page{textPage(pdftest.Cell{X: 50, S: "PO Number: 4471"})}
	t.Run("owner restricted decrypts with empty password", func(t *testing.T) {
		res, err := ParsePDF(context.Background(), pdftest.Build(pages, pdftest.Options{Encrypt: pdftest.EncryptRestricted}), Limits{})
		require.NoError(t, err)
		assert.True(t, res.Restricted)
		require.Len(t, res.Pages, 1)
		assert.Equal(t, "PO Number: 4471", res.Pages[0].Rows[0].Text())
	})
	t.Run("signature detected and bytes untouched", func(t *testing.T) {
		b := pdftest.Build(pages, pdftest.Options{Signed: true})
		orig := append([]byte(nil), b...)
		res, err := ParsePDF(context.Background(), b, Limits{})
		require.NoError(t, err)
		assert.True(t, res.Signed)
		assert.Equal(t, orig, b)
	})
	t.Run("plain pdf is neither", func(t *testing.T) {
		res, err := ParsePDF(context.Background(), pdftest.Build(pages, pdftest.Options{}), Limits{})
		require.NoError(t, err)
		assert.False(t, res.Signed)
		assert.False(t, res.Restricted)
	})
}

func TestParsePDF_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := pdftest.Build([]pdftest.Page{textPage(pdftest.Cell{X: 50, S: "hello 1"})}, pdftest.Options{})
	_, err := ParsePDF(ctx, b, Limits{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestIsJunkRune(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'a', false}, {' ', false}, {'\n', false}, {'\t', false},
		{0xFFFD, true}, {0xE001, true}, {0x01, true}, {0x7f, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isJunkRune(tt.r), "%U", tt.r)
	}
}
