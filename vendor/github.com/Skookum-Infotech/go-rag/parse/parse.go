// Package parse turns uploaded document files (CSV, XLSX, DOCX, PDF) into a
// generic Document a caller can either row-map (tabular formats) or feed to
// an LLM structured-extraction step (free-form formats) — the two paths a
// document importer needs (see the go-rag architecture plan's Phase 5).
//
// Every parser here is written to survive an adversarial upload: bounded
// decompression instead of trusting a declared size, in-memory-only reading
// (there is no destination path to zip-slip into), and panic recovery at the
// Run boundary so a malformed file fails its own parse job, never the
// process. See each parser's doc comment for its specific hardening.
package parse

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// DefaultMaxBytes bounds how many raw (compressed, for zip-based formats)
// bytes a parser will read from its input before giving up — the outermost
// cap, matching the upload size limit callers enforce before a file ever
// reaches this package (see controllers/attachments.go's maxFileSizeBytes in
// StoneSuite). Formats built on a zip container (XLSX, DOCX) additionally
// enforce a DECOMPRESSED cap — see DefaultMaxDecompressedBytes — since a zip
// bomb's whole point is a small compressed size hiding a huge expansion.
const DefaultMaxBytes = 25 * 1024 * 1024

// DefaultMaxDecompressedBytes bounds total decompressed bytes for a
// zip-container format. Generous for any real business document, small next
// to a bomb's multi-GB expansion, and — as importantly — small enough that
// even a full worst-case parse stays well inside a 1GB worker, never mind a
// spike large enough to look like a leak.
const DefaultMaxDecompressedBytes = 200 * 1024 * 1024

// Document is one parsed file, holding EITHER tabular rows (CSV, XLSX) OR
// free-form text (DOCX, PDF) — never both; a caller switches on which is
// populated to decide whether to go straight to column-mapping or through an
// LLM structured-extraction step.
type Document struct {
	// Headers is the tabular source's column order (its first row). Empty
	// for a free-form Document.
	Headers []string
	// Rows holds one map per data row for tabular sources: column header ->
	// cell text. A row shorter than Headers simply omits the missing keys
	// rather than padding with empty strings, so a caller can distinguish
	// "blank cell" from "row didn't have this column" if it cares to. Nil
	// for a free-form Document.
	Rows []map[string]string
	// Text holds extracted plain text for free-form sources. Empty for a
	// tabular Document.
	Text string
}

// Parser turns raw file bytes into a Document. Implementations MUST NOT
// write anything to disk (parse in memory only) and must bound their own
// memory use rather than trusting the input's claimed size — see each
// concrete parser's doc comment. A Parser is allowed to panic on
// sufficiently malformed input from a misbehaving third-party library; Run
// is the safety net every caller should use instead of calling Parse
// directly.
type Parser interface {
	Parse(ctx context.Context, r io.Reader) (Document, error)
}

// Run executes p.Parse with panic recovery: a third-party parsing library
// panicking on adversarial or merely corrupt input becomes an error, never a
// process crash. This is the boundary the architecture plan calls for
// ("a bad upload must fail the job, not the process") — callers should
// always go through Run, never call a Parser's Parse method directly.
func Run(ctx context.Context, p Parser, r io.Reader) (doc Document, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("parser panicked: %v", rec)
		}
	}()
	return p.Parse(ctx, r)
}

// ForExt returns the Parser registered for a file extension (with or
// without a leading dot, case-insensitive) and true, or (nil, false) for
// anything unrecognized. An unrecognized extension is a caller error —
// reject the upload — never a guess at how to parse it.
func ForExt(ext string) (Parser, bool) {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "csv":
		return NewCSVParser(), true
	case "xlsx":
		return NewXLSXParser(), true
	case "docx":
		return NewDOCXParser(), true
	case "pdf":
		return NewPDFParser(), true
	default:
		return nil, false
	}
}

// readBounded reads at most limit+1 bytes from r and errors if that many
// were actually available — i.e. the true input exceeded limit — rather
// than silently truncating. Shared by every parser that needs to cap raw
// input before handing it to a format-specific decoder.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("input exceeds the %d byte limit", limit)
	}
	return raw, nil
}

// rowsFromTable turns a header row + data rows (as returned by encoding/csv
// or excelize's GetRows) into Document.Rows, shared by the CSV and XLSX
// parsers so the "shorter row omits missing keys" convention (see Document's
// doc comment) is defined in exactly one place.
func rowsFromTable(headers []string, dataRows [][]string) []map[string]string {
	out := make([]map[string]string, 0, len(dataRows))
	for _, row := range dataRows {
		m := make(map[string]string, len(headers))
		for i, h := range headers {
			if i < len(row) {
				m[h] = row[i]
			}
		}
		out = append(out, m)
	}
	return out
}
