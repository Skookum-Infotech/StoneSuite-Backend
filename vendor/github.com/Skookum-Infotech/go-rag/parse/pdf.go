package parse

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/ledongthuc/pdf"
)

// PDFParser extracts digital (embedded) text from a PDF via a pure-Go
// reader — no CGO, no MuPDF/poppler dependency, which is why this is
// meaningfully weaker than a native PDF engine on multi-column layouts and
// tables (text can interleave out of reading order). No OCR: a scanned
// (image-only) PDF yields empty text, not an error, since nothing here is
// actually wrong with the file. Behind the Parser interface so an OCR/
// layout sidecar can replace or augment this later without reworking
// anything upstream.
//
// Malformed PDFs are a known way to crash a naive parser (this library
// included) — that risk is why every caller MUST go through Run, which
// recovers a panic into an error rather than letting it escape.
type PDFParser struct {
	// MaxBytes caps the raw input size. Zero uses DefaultMaxBytes.
	MaxBytes int64
}

// NewPDFParser builds a PDFParser with DefaultMaxBytes.
func NewPDFParser() *PDFParser { return &PDFParser{MaxBytes: DefaultMaxBytes} }

func (p *PDFParser) maxBytes() int64 {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return DefaultMaxBytes
}

func (p *PDFParser) Parse(ctx context.Context, r io.Reader) (Document, error) {
	raw, err := readBounded(r, p.maxBytes())
	if err != nil {
		return Document{}, err
	}
	if len(raw) == 0 {
		return Document{}, fmt.Errorf("pdf is empty")
	}

	reader, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Document{}, fmt.Errorf("open pdf: %w", err)
	}

	textReader, err := reader.GetPlainText()
	if err != nil {
		return Document{}, fmt.Errorf("extract pdf text: %w", err)
	}
	text, err := io.ReadAll(textReader)
	if err != nil {
		return Document{}, fmt.Errorf("read pdf text: %w", err)
	}
	return Document{Text: string(text)}, nil
}
