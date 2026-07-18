package salesorder

import (
	"fmt"
	"strings"
)

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix, e.g. "::date") so an INSERT/UPDATE's column list and argument list
// are always built from the same slice — never two hand-aligned lists that
// can silently drift out of position against each other.
type colVal struct {
	col  string
	val  any
	cast string
}

// addrColVals returns the 12 (column, value) pairs for a billing/shipping
// address block, in the exact column order the schema declares (state before
// zip — see sales_order_bill_addr_state/_zip in schema.sql). prefix is
// "sales_order_bill" or "sales_order_ship".
func addrColVals(prefix string, a AddressInput) []colVal {
	return []colVal{
		{prefix + "_customer_name", a.CustomerName, ""},
		{prefix + "_attention", a.Attention, ""},
		{prefix + "_addr_line1", a.AddrLine1, ""},
		{prefix + "_addr_line2", a.AddrLine2, ""},
		{prefix + "_addr_suitenum", a.SuiteUnit, ""},
		{prefix + "_addr_city", a.City, ""},
		{prefix + "_addr_state", a.StateID, ""},
		{prefix + "_addr_zip", a.Zip, ""},
		{prefix + "_addr_country", a.CountryID, ""},
		{prefix + "_phone", a.Phone, ""},
		{prefix + "_fax", a.Fax, ""},
		{prefix + "_email", a.Email, ""},
	}
}

// buildInsert renders an INSERT ... VALUES (...) RETURNING statement from
// column/value pairs, numbering placeholders by position so cols and args
// can never drift apart.
func buildInsert(table string, cv []colVal, returning string) (string, []any) {
	cols := make([]string, len(cv))
	phs := make([]string, len(cv))
	args := make([]any, len(cv))
	for i, c := range cv {
		cols[i] = c.col
		args[i] = c.val
		phs[i] = fmt.Sprintf("$%d%s", i+1, c.cast)
	}
	sql := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// buildUpdateSet renders an "UPDATE ... SET col=$n, ... WHERE <where>"
// statement. leadingArgs are bound first (e.g. the WHERE clause's own
// placeholders, referenced as $1.. in where); cv's placeholders continue
// after them.
func buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any) {
	sets := make([]string, 0, len(cv)+len(extraSets))
	args := make([]any, 0, len(leadingArgs)+len(cv))
	args = append(args, leadingArgs...)
	for _, c := range cv {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", c.col, len(args), c.cast))
	}
	sets = append(sets, extraSets...)
	sql := "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	return sql, args
}
