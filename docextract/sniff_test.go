package docextract

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract/internal/pdftest"
)

// zipWith builds a zip containing the named empty-ish entries.
func zipWith(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		require.NoError(t, err)
		_, err = w.Write([]byte("<x/>"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestSniff(t *testing.T) {
	pdfBytes := pdftest.Build([]pdftest.Page{{}}, pdftest.Options{})
	docx := pdftest.BuildDocx([]pdftest.DocxBlock{{Para: "hi"}})
	tests := []struct {
		name     string
		in       []byte
		wantKind FileKind
		wantCode FailureCode
	}{
		{name: "pdf", in: pdfBytes, wantKind: KindPDF},
		{name: "docx", in: docx, wantKind: KindDOCX},
		{name: "empty", in: nil, wantCode: FailEmpty},
		{name: "zero length slice", in: []byte{}, wantCode: FailEmpty},
		{name: "spoofed pdf that is a plain zip", in: zipWith(t, "a.txt"), wantCode: FailUnsupportedType},
		{name: "text file", in: []byte("hello"), wantCode: FailUnsupportedType},
		{name: "png", in: []byte("\x89PNG\r\n\x1a\n"), wantCode: FailUnsupportedType},
		{name: "truncated zip", in: docx[:len(docx)/2], wantCode: FailCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, err := Sniff(tt.in)
			if tt.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.wantKind, kind)
				return
			}
			var ie *InputError
			require.True(t, errors.As(err, &ie), "want InputError, got %v", err)
			assert.Equal(t, tt.wantCode, ie.Code)
		})
	}
}
