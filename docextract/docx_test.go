package docextract

import (
	"archive/zip"
	"bytes"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract/internal/pdftest"
)

func TestParseDOCX_ParagraphsAndTable(t *testing.T) {
	b := pdftest.BuildDocx([]pdftest.DocxBlock{
		{Para: "PO Number: 7001"},
		{Table: [][][]string{
			{{"Item"}, {"Description"}, {"Qty"}},
			{{"A-1"}, {"Granite", "second paragraph"}, {"4"}},
		}},
		{Para: "Total $10.00"},
	})
	pages, err := ParseDOCX(b)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	var got []string
	for _, r := range pages[0].Rows {
		got = append(got, r.Text())
	}
	assert.Equal(t, []string{"PO Number: 7001", "Item Description Qty", "A-1 Granite 4", "second paragraph", "Total $10.00"}, got)
	hdr := pages[0].Rows[1]
	assert.Equal(t, 0.0, hdr.Words[0].X)
	assert.Equal(t, docxColumnWidth, hdr.Words[1].X)
	assert.Equal(t, 2*docxColumnWidth, hdr.Words[2].X)
	assert.Greater(t, pages[0].Rows[0].Y, pages[0].Rows[1].Y)
}

func TestParseDOCX_Limits(t *testing.T) {
	mk := func(entries map[string]int, count int) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, size := range entries {
			w, err := zw.Create(name)
			require.NoError(t, err)
			_, err = w.Write(bytes.Repeat([]byte("a"), size))
			require.NoError(t, err)
		}
		for i := 0; i < count; i++ {
			_, err := zw.Create("pad/" + strconv.Itoa(i))
			require.NoError(t, err)
		}
		require.NoError(t, zw.Close())
		return buf.Bytes()
	}
	tests := []struct {
		name string
		in   []byte
		code FailureCode
	}{
		{"too many entries", mk(map[string]int{docxMainPart: 10}, maxDocxEntries+1), FailTooLarge},
		{"document.xml expands beyond cap", mk(map[string]int{docxMainPart: maxDocxXMLBytes + 1024}, 0), FailTooLarge},
		{"total uncompressed beyond cap", mk(map[string]int{docxMainPart: 1024, "big1": maxDocxUncompressed / 2, "big2": maxDocxUncompressed / 2}, 0), FailTooLarge},
		{"missing main part", mk(map[string]int{"other.xml": 10}, 0), FailUnsupportedType},
		{"not a zip", []byte("PK\x03\x04garbage"), FailCorrupt},
		{"malformed xml", rawDocx(t, "<a><b></a>"), FailCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseDOCX(tt.in)
			require.Error(t, err)
			assert.Equal(t, tt.code, inputCode(t, err))
		})
	}
}

func TestParseDOCX_EmptyDocument(t *testing.T) {
	_, err := ParseDOCX(pdftest.BuildDocx(nil))
	require.Error(t, err)
	assert.Equal(t, FailEmpty, inputCode(t, err))
}

// rawDocx builds a zip whose word/document.xml holds the given bytes.
func rawDocx(t *testing.T, xmlBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(docxMainPart)
	require.NoError(t, err)
	_, err = w.Write([]byte(xmlBody))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
