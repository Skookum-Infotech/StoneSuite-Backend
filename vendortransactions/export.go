package vendortransactions

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dateonly"
)

// ExportMaxRows caps one CSV export; a larger result must be narrowed by filters.
const ExportMaxRows = 10000

// ErrExportTooLarge is returned when the filtered set exceeds ExportMaxRows;
// handlers map it to 413.
var ErrExportTooLarge = errors.New("export exceeds the row limit")

// csvHeader is the export column order.
var csvHeader = []string{"Type", "Number", "Status", "Date", "Due Date", "Amount", "Balance", "Open", "Overdue"}

// formulaPrefixes are the leading characters a spreadsheet treats as a formula.
const formulaPrefixes = "=+-@\t\r"

// moneyDecimals is the fraction digits written for amounts.
const moneyDecimals = 2

// ExportRows returns every transaction matching p (no cursor, newest-created
// first) through the same scope and filter path as List, or ErrExportTooLarge
// when more than ExportMaxRows match. Denied mirrors Page.Denied.
func ExportRows(ctx context.Context, pool *pgxpool.Pool, p Params) (rows []Row, denied bool, err error) {
	p.Cursor = ""
	f, err := parse(p)
	if err != nil {
		return nil, false, err
	}
	f.limit = ExportMaxRows
	f.after = nil
	rows, denied, err = queryRows(ctx, pool, p, f)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > ExportMaxRows {
		return nil, false, ErrExportTooLarge
	}
	return rows, denied, nil
}

// csvSafe neutralises spreadsheet formula injection by prefixing a single quote
// to any cell that starts with = + - @ tab or CR.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune(formulaPrefixes, rune(s[0])) {
		return "'" + s
	}
	return s
}

// WriteCSV writes rows as CSV with a header, formula-escaping every text cell.
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	for _, r := range rows {
		rec := []string{
			csvSafe(r.TypeLabel), csvSafe(r.Number), csvSafe(r.Status), r.Date.String(),
			optDate(r.DueDate), optMoney(r.Amount), optMoney(r.Balance),
			strconv.FormatBool(r.IsOpen), strconv.FormatBool(r.IsOverdue),
		}
		if err := cw.Write(rec); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}
	return nil
}

// optDate formats an optional date; empty when nil.
func optDate(d *dateonly.Date) string {
	if d == nil {
		return ""
	}
	return d.String()
}

// optMoney formats an optional amount; empty when nil. Negative amounts are
// numbers, not text, so they are not formula-escaped.
func optMoney(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', moneyDecimals, 64)
}
