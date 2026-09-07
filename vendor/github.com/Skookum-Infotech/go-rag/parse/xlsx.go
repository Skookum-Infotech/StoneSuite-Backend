package parse

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"
)

// XLSXParser parses the first sheet of an .xlsx workbook via excelize.
//
// Zip-bomb hardening: excelize.Options.UnzipSizeLimit rejects the file
// outright (an error, not silent truncation) once the CUMULATIVE declared
// uncompressed size of the zip's entries exceeds the limit — checked before
// decompressing each entry, so a bomb never actually gets expanded into
// memory. UnzipXMLSizeLimit is set to the SAME value deliberately: excelize
// spools an oversized worksheet/shared-strings XML part to a temp file on
// disk once its own size exceeds UnzipXMLSizeLimit, which would violate this
// package's in-memory-only contract — setting the two limits equal makes
// that path unreachable, because any single part large enough to trigger it
// would already have tripped the cumulative UnzipSizeLimit check first.
type XLSXParser struct {
	// MaxDecompressedBytes caps total uncompressed size. Zero uses
	// DefaultMaxDecompressedBytes.
	MaxDecompressedBytes int64
	// MaxBytes caps the raw (compressed) upload. Zero uses DefaultMaxBytes.
	MaxBytes int64
}

// NewXLSXParser builds an XLSXParser with the package defaults.
func NewXLSXParser() *XLSXParser {
	return &XLSXParser{MaxDecompressedBytes: DefaultMaxDecompressedBytes, MaxBytes: DefaultMaxBytes}
}

func (p *XLSXParser) maxDecompressed() int64 {
	if p.MaxDecompressedBytes > 0 {
		return p.MaxDecompressedBytes
	}
	return DefaultMaxDecompressedBytes
}

func (p *XLSXParser) maxBytes() int64 {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return DefaultMaxBytes
}

// Parse reads the workbook's first sheet, treating its first row as headers.
func (p *XLSXParser) Parse(ctx context.Context, r io.Reader) (Document, error) {
	raw, err := readBounded(r, p.maxBytes())
	if err != nil {
		return Document{}, err
	}

	limit := p.maxDecompressed()
	f, err := excelize.OpenReader(bytes.NewReader(raw), excelize.Options{
		UnzipSizeLimit:    limit,
		UnzipXMLSizeLimit: limit,
	})
	if err != nil {
		return Document{}, fmt.Errorf("open xlsx: %w", err)
	}
	defer func() { _ = f.Close() }()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return Document{}, fmt.Errorf("xlsx has no sheets")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return Document{}, fmt.Errorf("read xlsx rows: %w", err)
	}
	if len(rows) == 0 {
		return Document{}, fmt.Errorf("xlsx sheet %q has no rows", sheets[0])
	}

	headers := rows[0]
	return Document{Headers: headers, Rows: rowsFromTable(headers, rows[1:])}, nil
}
