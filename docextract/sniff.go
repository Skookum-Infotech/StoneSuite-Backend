package docextract

import (
	"archive/zip"
	"bytes"
	"fmt"
)

// FileKind is the sniffed container type.
type FileKind string

// Supported container kinds.
const (
	KindPDF  FileKind = "pdf"
	KindDOCX FileKind = "docx"
)

const (
	pdfMagic     = "%PDF-"
	zipMagic     = "PK\x03\x04"
	docxMainPart = "word/document.xml"
)

// Sniff decides the file kind from magic bytes, ignoring any claimed MIME or
// extension. Failures are *InputError.
func Sniff(b []byte) (FileKind, error) {
	switch {
	case len(b) == 0:
		return "", newInputError(FailEmpty, "file is empty")
	case bytes.HasPrefix(b, []byte(pdfMagic)):
		return KindPDF, nil
	case bytes.HasPrefix(b, []byte(zipMagic)):
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return "", newInputError(FailCorrupt, fmt.Sprintf("zip: %v", err))
		}
		for _, f := range zr.File {
			if f.Name == docxMainPart {
				return KindDOCX, nil
			}
		}
		return "", newInputError(FailUnsupportedType, "zip archive is not a Word document")
	default:
		return "", newInputError(FailUnsupportedType, "not a PDF or DOCX file")
	}
}
