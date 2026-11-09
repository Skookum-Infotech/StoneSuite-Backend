package reports

import (
	"fmt"
	"strings"
)

// BlankGroupLabel is what a NULL or empty group value reads as.
const BlankGroupLabel = "(blank)"

const (
	moneyDecimals  = 2
	numberDecimals = 4
	dateOutput     = "YYYY-MM-DD"
)

// ScopeArgs is the caller's row scope, already resolved to an employee id.
type ScopeArgs struct {
	OwnerEmployeeID int
	Restricted      bool // false = every row; true = only rows the employee owns
}

// ResultColumn describes one output column.
type ResultColumn struct {
	Key   string    `json:"key"` // a field key, "group", or "total:<index>"
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
}

// Compiled is a Definition turned into bind-parameter SQL.
type Compiled struct {
	SQL       string
	TotalsSQL string // COUNT(*) first, then one value per Column (NULL where none applies); same Args
	Args      []any
	Columns   []ResultColumn
	Limit     int  // rows to keep; SQL asks for Limit+1 so the caller can tell it was cut
	TopN      bool // Limit came from the report's own top-N, so being cut is not truncation
}

type compiler struct {
	src  Source
	args []any
	used map[string]bool
}

// BuildQuery validates def and compiles it for src. Every user-supplied value
// becomes a bind argument; the only text interpolated into SQL is catalog
// SQL and fixed keywords.
func BuildQuery(src Source, def Definition, scope ScopeArgs, limit int) (Compiled, error) {
	if err := Validate(def, src); err != nil {
		return Compiled{}, err
	}
	c := &compiler{src: src, used: map[string]bool{}}
	where, err := c.where(def, scope)
	if err != nil {
		return Compiled{}, err
	}

	var out Compiled
	var selects, totals []string
	var orderPos map[string]int
	if def.GroupBy != nil {
		selects, totals, out.Columns, orderPos = c.groupedSelect(def)
	} else {
		selects, totals, out.Columns, orderPos = c.listSelect(def)
	}
	order := c.orderBy(def, orderPos)

	out.Limit, out.TopN = limit, false
	if def.TopN > 0 && def.TopN < limit {
		out.Limit, out.TopN = def.TopN, true
	}
	from := c.src.from
	for _, name := range c.src.joinOrder {
		if c.used[name] {
			from += " " + c.src.joins[name].sql
		}
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	groupSQL := ""
	if def.GroupBy != nil {
		groupSQL = " GROUP BY 1"
	}
	out.SQL = fmt.Sprintf("SELECT %s FROM %s%s%s ORDER BY %s LIMIT %d",
		strings.Join(selects, ", "), from, whereSQL, groupSQL, order, out.Limit+1)
	out.TotalsSQL = fmt.Sprintf("SELECT COUNT(*)::float8, %s FROM %s%s",
		strings.Join(totals, ", "), from, whereSQL)
	out.Args = c.args
	return out, nil
}

func (c *compiler) needJoin(name string) {
	if name == "" || c.used[name] {
		return
	}
	c.used[name] = true
	for _, dep := range c.src.joins[name].needs {
		c.needJoin(dep)
	}
}

// field looks up a key Validate already vetted and registers its join.
func (c *compiler) field(key string) Field {
	f, _ := c.src.Field(key)
	c.needJoin(f.join)
	return f
}

func (c *compiler) bind(v any) string {
	c.args = append(c.args, v)
	return fmt.Sprintf("$%d", len(c.args))
}

func (c *compiler) where(def Definition, scope ScopeArgs) ([]string, error) {
	where := append([]string{}, c.src.where...)
	if scope.Restricted && c.src.ownerExpr != "" {
		where = append(where, fmt.Sprintf("%s = %s", c.src.ownerExpr, c.bind(scope.OwnerEmployeeID)))
	}
	for _, f := range def.Filters {
		clause, err := c.filter(f)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	return where, nil
}

func (c *compiler) filter(f FilterRule) (string, error) {
	field := c.field(f.Field)
	expr := field.expr
	numeric := field.Type == FieldNumber || field.Type == FieldMoney
	val := func(i int) string {
		v := strings.TrimSpace(f.Values[i])
		if numeric {
			return c.bind(v) + "::numeric"
		}
		return c.bind(f.Values[i])
	}
	switch f.Op {
	case OpEq:
		return fmt.Sprintf("%s = %s", expr, val(0)), nil
	case OpNeq:
		return fmt.Sprintf("%s IS DISTINCT FROM %s", expr, val(0)), nil
	case OpGt:
		return fmt.Sprintf("%s > %s", expr, val(0)), nil
	case OpGte:
		return fmt.Sprintf("%s >= %s", expr, val(0)), nil
	case OpLt:
		return fmt.Sprintf("%s < %s", expr, val(0)), nil
	case OpLte:
		return fmt.Sprintf("%s <= %s", expr, val(0)), nil
	case OpBetween:
		lo := val(0)
		return fmt.Sprintf("%s BETWEEN %s AND %s", expr, lo, val(1)), nil
	case OpContains:
		return fmt.Sprintf("(%s)::text ILIKE '%%' || %s || '%%' ESCAPE '\\'", expr, c.bind(escapeLike(f.Values[0]))), nil
	case OpIn:
		return fmt.Sprintf("%s = ANY(%s)", expr, c.bind(append([]string{}, f.Values...))), nil
	case OpIsEmpty:
		return fmt.Sprintf("(%s IS NULL OR (%s)::text = '')", expr, expr), nil
	case OpDateRange:
		from := c.bind(f.Values[0])
		to := c.bind(f.Values[1])
		return fmt.Sprintf("(%s >= %s::date AND %s < %s::date + 1)", expr, from, expr, to), nil
	}
	return "", invalid(f.Field, "%s cannot be filtered with %q.", field.Label, f.Op)
}

// escapeLike makes a user's text match literally inside an ILIKE.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// selectExpr is how a field is read back: text, float8 or an ISO date string,
// so the runner never deals in driver-specific types.
func selectExpr(f Field) string {
	switch f.Type {
	case FieldNumber:
		return fmt.Sprintf("(%s)::float8", f.expr)
	case FieldMoney:
		return fmt.Sprintf("ROUND((%s)::numeric, %d)::float8", f.expr, moneyDecimals)
	case FieldDate:
		return fmt.Sprintf("to_char(%s, '%s')", f.expr, dateOutput)
	}
	return fmt.Sprintf("(%s)::text", f.expr)
}

func outputType(t FieldType) FieldType {
	if t == FieldStatus {
		return FieldText
	}
	return t
}

func (c *compiler) listSelect(def Definition) (selects, totals []string, cols []ResultColumn, pos map[string]int) {
	pos = map[string]int{}
	for i, key := range def.Columns {
		f := c.field(key)
		selects = append(selects, selectExpr(f))
		cols = append(cols, ResultColumn{Key: key, Label: f.Label, Type: outputType(f.Type)})
		pos[key] = i + 1
		totals = append(totals, sumOrNull(f))
	}
	return selects, totals, cols, pos
}

// sumOrNull totals a number/money column and leaves every other column empty.
func sumOrNull(f Field) string {
	switch f.Type {
	case FieldMoney:
		return fmt.Sprintf("ROUND(SUM(%s)::numeric, %d)::float8", f.expr, moneyDecimals)
	case FieldNumber:
		return fmt.Sprintf("ROUND(SUM(%s)::numeric, %d)::float8", f.expr, numberDecimals)
	}
	return "NULL::float8"
}

func (c *compiler) groupedSelect(def Definition) (selects, totals []string, cols []ResultColumn, pos map[string]int) {
	g := c.field(def.GroupBy.Field)
	groupExpr := fmt.Sprintf("COALESCE(NULLIF((%s)::text, ''), '%s')", g.expr, BlankGroupLabel)
	label, gtype := g.Label, FieldText
	if g.Type == FieldDate {
		gtype = FieldDate
		label = fmt.Sprintf("%s (by %s)", g.Label, def.GroupBy.DateBucket)
		groupExpr = fmt.Sprintf("COALESCE(to_char(date_trunc('%s', (%s)::timestamp), '%s'), '%s')",
			def.GroupBy.DateBucket, g.expr, dateOutput, BlankGroupLabel)
	}
	selects = []string{groupExpr}
	totals = []string{"NULL::float8"}
	cols = []ResultColumn{{Key: "group", Label: label, Type: gtype}}
	pos = map[string]int{"group": 1}
	for i, t := range def.Totals {
		expr, col := c.aggregate(t, i)
		selects = append(selects, expr)
		totals = append(totals, expr)
		cols = append(cols, col)
		pos[col.Key] = i + 2
	}
	return selects, totals, cols, pos
}

var aggTitles = map[AggFn]string{AggSum: "Sum", AggAvg: "Average", AggMin: "Min", AggMax: "Max"}

func (c *compiler) aggregate(t Total, index int) (string, ResultColumn) {
	key := fmt.Sprintf("total:%d", index)
	if t.Fn == AggCount {
		return "COUNT(*)::float8", ResultColumn{Key: key, Label: "Count", Type: FieldNumber}
	}
	f := c.field(t.Field)
	decimals := numberDecimals
	if f.Type == FieldMoney {
		decimals = moneyDecimals
	}
	fn := strings.ToUpper(string(t.Fn))
	expr := fmt.Sprintf("ROUND(%s(%s)::numeric, %d)::float8", fn, f.expr, decimals)
	return expr, ResultColumn{Key: key, Label: fmt.Sprintf("%s of %s", aggTitles[t.Fn], f.Label), Type: f.Type}
}

func (c *compiler) orderBy(def Definition, pos map[string]int) string {
	grouped := def.GroupBy != nil
	key, desc := "", false
	switch {
	case def.Sort != nil:
		key, desc = def.Sort.Key, def.Sort.Desc
	case grouped && def.GroupBy.DateBucket != "":
		key = "group" // a date group reads best oldest first
	case grouped:
		key, desc = "total:0", true // biggest first
	default:
		key = def.Columns[0]
	}
	p := pos[key]
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	order := fmt.Sprintf("%d %s NULLS LAST", p, dir)
	if grouped && p != 1 {
		order += ", 1"
	}
	return order
}
