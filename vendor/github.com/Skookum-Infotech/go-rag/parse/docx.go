package parse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// docxDocumentPart is the WordprocessingML part holding a .docx's visible
// body text — a .docx is a zip archive of XML parts, and this is the one
// that matters for plain-text extraction.
const docxDocumentPart = "word/document.xml"

// DOCXParser extracts word/document.xml's visible text from a .docx.
//
// Hardening, since a .docx is a zip archive of XML (the same attack surface
// XLSX has):
//   - Zip bomb: this package hand-rolls what excelize does for XLSX — sum
//     each entry's DECLARED uncompressed size (from the zip's central
//     directory, read without decompressing anything) and reject before
//     opening any entry if the running total exceeds MaxDecompressedBytes.
//     As a second, independent backstop against a header that understates
//     the true expansion, the actual decompression read is itself bounded
//     by readBounded — the real limit is bytes actually produced, not the
//     header's claim.
//   - Zip-slip: structurally not applicable. This parser only ever reads an
//     entry's bytes into memory by exact name match (docxDocumentPart); it
//     never derives a filesystem path from an entry's name, so there is no
//     destination for a "../../" entry name to slip into.
//   - Entity expansion ("billion laughs"): encoding/xml resolves only the
//     five standard XML entities (&lt; &amp; &gt; &apos; &quot;) plus
//     numeric character references — it does not process a DOCTYPE's
//     internal entity subset at all, so a document declaring recursive
//     custom entities fails to parse (an error) rather than expanding.
//     External entities are never resolved either, which separately rules
//     out classic XXE.
type DOCXParser struct {
	// MaxDecompressedBytes caps total uncompressed size. Zero uses
	// DefaultMaxDecompressedBytes.
	MaxDecompressedBytes int64
	// MaxBytes caps the raw (compressed) upload. Zero uses DefaultMaxBytes.
	MaxBytes int64
}

// NewDOCXParser builds a DOCXParser with the package defaults.
func NewDOCXParser() *DOCXParser {
	return &DOCXParser{MaxDecompressedBytes: DefaultMaxDecompressedBytes, MaxBytes: DefaultMaxBytes}
}

func (p *DOCXParser) maxDecompressed() int64 {
	if p.MaxDecompressedBytes > 0 {
		return p.MaxDecompressedBytes
	}
	return DefaultMaxDecompressedBytes
}

func (p *DOCXParser) maxBytes() int64 {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return DefaultMaxBytes
}

func (p *DOCXParser) Parse(ctx context.Context, r io.Reader) (Document, error) {
	raw, err := readBounded(r, p.maxBytes())
	if err != nil {
		return Document{}, err
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Document{}, fmt.Errorf("open docx: %w", err)
	}

	limit := p.maxDecompressed()
	var declaredTotal int64
	var docPart *zip.File
	for _, f := range zr.File {
		declaredTotal += int64(f.UncompressedSize64)
		if declaredTotal > limit {
			return Document{}, fmt.Errorf("docx declares more than the %d byte decompressed limit", limit)
		}
		if f.Name == docxDocumentPart {
			docPart = f
		}
	}
	if docPart == nil {
		return Document{}, fmt.Errorf("docx missing %s", docxDocumentPart)
	}

	rc, err := docPart.Open()
	if err != nil {
		return Document{}, fmt.Errorf("open %s: %w", docxDocumentPart, err)
	}
	defer func() { _ = rc.Close() }()

	xmlBytes, err := readBounded(rc, limit)
	if err != nil {
		return Document{}, fmt.Errorf("read %s: %w", docxDocumentPart, err)
	}

	text, err := extractWordprocessingText(xmlBytes)
	if err != nil {
		return Document{}, fmt.Errorf("parse %s: %w", docxDocumentPart, err)
	}
	return Document{Text: text}, nil
}

// extractWordprocessingText walks WordprocessingML tokens rather than
// unmarshaling into a struct, so memory use stays proportional to the
// output text, not to a parsed document tree — visible text lives in <w:t>
// elements; a <w:p> (paragraph) end becomes a newline. Tables, headers, and
// other structure are flattened to their text content in document order,
// same "plain extraction only, no layout" tradeoff PDFParser makes.
func extractWordprocessingText(xmlBytes []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var b strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}
