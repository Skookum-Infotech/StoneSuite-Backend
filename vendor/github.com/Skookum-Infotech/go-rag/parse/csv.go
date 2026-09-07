package parse

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
)

// CSVParser parses RFC 4180 CSV via the standard library. CSV has no
// container/compression layer, so the only limit that applies is the raw
// byte cap — see MaxBytes.
type CSVParser struct {
	// MaxBytes caps the raw input size. Zero uses DefaultMaxBytes.
	MaxBytes int64
}

// NewCSVParser builds a CSVParser with DefaultMaxBytes.
func NewCSVParser() *CSVParser { return &CSVParser{MaxBytes: DefaultMaxBytes} }

func (p *CSVParser) maxBytes() int64 {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return DefaultMaxBytes
}

// Parse reads the first row as headers and every subsequent row as data.
// Ragged rows (a data row with more or fewer fields than the header) are
// tolerated — encoding/csv's FieldsPerRecord check is disabled — since a
// human-edited spreadsheet export routinely has trailing blank cells
// trimmed; rowsFromTable's "shorter row omits missing keys" convention
// handles the rest.
func (p *CSVParser) Parse(ctx context.Context, r io.Reader) (Document, error) {
	raw, err := readBounded(r, p.maxBytes())
	if err != nil {
		return Document{}, err
	}

	cr := csv.NewReader(bytes.NewReader(raw))
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return Document{}, fmt.Errorf("parse csv: %w", err)
	}
	if len(rows) == 0 {
		return Document{}, fmt.Errorf("csv has no rows")
	}

	headers := rows[0]
	return Document{Headers: headers, Rows: rowsFromTable(headers, rows[1:])}, nil
}
