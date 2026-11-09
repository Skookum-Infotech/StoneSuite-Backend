package reports

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// totalSortKey is a sort on the Nth total: "total:0", "total:12", never "total:00".
var totalSortKey = regexp.MustCompile(`^total:(0|[1-9][0-9]*)$`)

// Custom Reports: a saved report is a Definition, never SQL. Validate checks a
// Definition against the fixed catalog (custom_catalog.go) and BuildQuery
// (custom_sql.go) compiles it, so nothing a user or the AI model types can
// reach the database as anything but a bind argument.

// FieldType is how the builder and the compiler treat a field.
type FieldType string

const (
	FieldText   FieldType = "text"
	FieldNumber FieldType = "number"
	FieldMoney  FieldType = "money"
	FieldDate   FieldType = "date"
	FieldStatus FieldType = "status"
)

// Operator is a filter comparison.
type Operator string

const (
	OpEq        Operator = "eq"
	OpNeq       Operator = "neq"
	OpContains  Operator = "contains"
	OpIn        Operator = "in"
	OpIsEmpty   Operator = "is_empty"
	OpGt        Operator = "gt"
	OpGte       Operator = "gte"
	OpLt        Operator = "lt"
	OpLte       Operator = "lte"
	OpBetween   Operator = "between"
	OpDateRange Operator = "date_range"
)

// AggFn is a total's aggregate function.
type AggFn string

const (
	AggSum   AggFn = "sum"
	AggCount AggFn = "count"
	AggAvg   AggFn = "avg"
	AggMin   AggFn = "min"
	AggMax   AggFn = "max"
)

// DateBucket is how a date group-by is rounded.
type DateBucket string

const (
	DateBucketDay     DateBucket = "day"
	DateBucketWeek    DateBucket = "week"
	DateBucketMonth   DateBucket = "month"
	DateBucketQuarter DateBucket = "quarter"
)

// Chart kinds a definition may ask for.
const (
	ChartBar  = "bar"
	ChartNone = "none"
)

// Limits shared by the validator, the compiler and the handlers.
const (
	MaxColumns      = 20
	MaxFilters      = 10
	MaxTotals       = 5
	MaxTopN         = 100
	PreviewRowLimit = 500
	ExportRowLimit  = 10000
	MaxReportName   = 120
)

// customDateLayout is the ISO date form a date filter's values use.
const customDateLayout = "2006-01-02"

// dateRangeValues is the number of values a date_range / between filter takes.
const rangeValues = 2

// knownPresets are the relative-range names the UI stores on a date filter so a
// saved report stays relative; the server only ever reads Values.
var knownPresets = map[string]bool{
	"": true, "this-month": true, "last-month": true, "this-quarter": true,
	"year-to-date": true, "last-12-months": true, "custom": true,
}

// FilterRule is one filter: a field, a comparison and its values.
type FilterRule struct {
	Field  string   `json:"field"`
	Op     Operator `json:"op"`
	Values []string `json:"values"`
	Preset string   `json:"preset,omitempty"`
}

// GroupBy groups the report on one field; a date field also needs a bucket.
type GroupBy struct {
	Field      string     `json:"field"`
	DateBucket DateBucket `json:"dateBucket,omitempty"`
}

// Total is one aggregate column of a grouped report.
type Total struct {
	Fn    AggFn  `json:"fn"`
	Field string `json:"field,omitempty"`
}

// SortSpec orders the rows. Key is a column's field key, "group", or
// "total:<index>".
type SortSpec struct {
	Key  string `json:"key"`
	Desc bool   `json:"desc"`
}

// Definition is a whole custom report.
type Definition struct {
	Source  string       `json:"source"`
	Columns []string     `json:"columns"`
	Filters []FilterRule `json:"filters"`
	GroupBy *GroupBy     `json:"groupBy,omitempty"`
	Totals  []Total      `json:"totals"`
	Sort    *SortSpec    `json:"sort,omitempty"`
	TopN    int          `json:"topN,omitempty"`
	Chart   string       `json:"chart"`
}

// ValidationError is a user-facing problem with a definition.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(field, format string, args ...any) *ValidationError {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// OperatorsFor lists the comparisons a field of type t supports, in the order
// the builder shows them.
func OperatorsFor(t FieldType) []Operator {
	switch t {
	case FieldText:
		return []Operator{OpEq, OpNeq, OpContains, OpIn, OpIsEmpty}
	case FieldNumber, FieldMoney:
		return []Operator{OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpBetween, OpIsEmpty}
	case FieldDate:
		return []Operator{OpDateRange, OpIsEmpty}
	case FieldStatus:
		return []Operator{OpIn}
	}
	return nil
}

func operatorAllowed(t FieldType, op Operator) bool {
	for _, o := range OperatorsFor(t) {
		if o == op {
			return true
		}
	}
	return false
}

// Validate checks def against src and returns the first problem as a
// *ValidationError, or nil when def is safe to compile.
func Validate(def Definition, src Source) error {
	if def.Source != src.Key {
		return invalid("source", "Unknown source.")
	}
	if def.Chart != "" && def.Chart != ChartBar && def.Chart != ChartNone {
		return invalid("chart", "The chart must be a bar chart or none.")
	}
	grouped := def.GroupBy != nil
	if err := validateColumns(def, src, grouped); err != nil {
		return err
	}
	if err := validateFilters(def.Filters, src); err != nil {
		return err
	}
	if grouped {
		if err := validateGroupBy(def.GroupBy, src); err != nil {
			return err
		}
	}
	if err := validateTotals(def.Totals, src, grouped); err != nil {
		return err
	}
	if def.Chart == ChartBar && (!grouped || len(def.Totals) == 0) {
		return invalid("chart", "A bar chart needs a group by field and at least one total.")
	}
	if def.TopN != 0 && (def.TopN < 1 || def.TopN > MaxTopN) {
		return invalid("topN", "Top N must be between 1 and %d.", MaxTopN)
	}
	return validateSort(def, grouped)
}

func validateColumns(def Definition, src Source, grouped bool) error {
	if grouped {
		return nil // a grouped report shows the group and its totals, not columns
	}
	if len(def.Columns) == 0 {
		return invalid("columns", "Pick at least one column.")
	}
	if len(def.Columns) > MaxColumns {
		return invalid("columns", "A report can show at most %d columns.", MaxColumns)
	}
	for _, key := range def.Columns {
		if _, ok := src.Field(key); !ok {
			return invalid(key, "%q is no longer available.", key)
		}
	}
	return nil
}

func validateFilters(filters []FilterRule, src Source) error {
	if len(filters) > MaxFilters {
		return invalid("filters", "A report can have at most %d filters.", MaxFilters)
	}
	for _, f := range filters {
		field, ok := src.Field(f.Field)
		if !ok {
			return invalid(f.Field, "%q is no longer available.", f.Field)
		}
		if !knownPresets[f.Preset] {
			return invalid(f.Field, "%s has an unknown date preset.", field.Label)
		}
		if !operatorAllowed(field.Type, f.Op) {
			return invalid(f.Field, "%s cannot be filtered with %q.", field.Label, f.Op)
		}
		if err := validateFilterValues(field, f); err != nil {
			return err
		}
	}
	return nil
}

func validateFilterValues(field Field, f FilterRule) error {
	for _, v := range f.Values {
		// A NUL byte is valid UTF-8 but Postgres refuses it, which would be a 500.
		if strings.ContainsRune(v, 0) {
			return invalid(f.Field, "%s contains characters that cannot be used.", field.Label)
		}
	}
	n := len(f.Values)
	switch f.Op {
	case OpIsEmpty:
		return nil
	case OpIn:
		if n < 1 {
			return invalid(f.Field, "%s needs at least one value.", field.Label)
		}
		return nil
	case OpBetween:
		if n != rangeValues {
			return invalid(f.Field, "%s needs two values.", field.Label)
		}
	case OpDateRange:
		return validateDateRange(field, f.Values)
	default:
		if n != 1 {
			return invalid(f.Field, "%s needs one value.", field.Label)
		}
	}
	if field.Type == FieldNumber || field.Type == FieldMoney {
		for _, v := range f.Values {
			if _, err := parseNumber(v); err != nil {
				return invalid(f.Field, "%s must be a number.", field.Label)
			}
		}
	}
	return nil
}

func validateDateRange(field Field, values []string) error {
	if len(values) != rangeValues {
		return invalid(field.Key, "%s needs two dates.", field.Label)
	}
	from, errFrom := time.Parse(customDateLayout, values[0])
	to, errTo := time.Parse(customDateLayout, values[1])
	if errFrom != nil || errTo != nil {
		return invalid(field.Key, "%s needs two dates.", field.Label)
	}
	if to.Before(from) {
		return invalid(field.Key, "%s end date must not be before the start date.", field.Label)
	}
	return nil
}

// plainDecimal is what Postgres' ::numeric accepts from the UI: digits with an
// optional sign, fraction and exponent. It excludes Go-only forms such as hex
// floats and underscores, which ParseFloat takes but Postgres rejects.
var plainDecimal = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

// parseNumber reads a plain decimal; thousands separators, hex floats, NaN and
// Inf are rejected so a stray value can never reach the database as a number.
func parseNumber(raw string) (float64, error) {
	trimmed := strings.TrimSpace(raw)
	if !plainDecimal.MatchString(trimmed) {
		return 0, fmt.Errorf("not a plain number")
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("not a finite number")
	}
	return v, nil
}

func validateGroupBy(g *GroupBy, src Source) error {
	field, ok := src.Field(g.Field)
	if !ok {
		return invalid(g.Field, "%q is no longer available.", g.Field)
	}
	if field.Type != FieldDate {
		return nil
	}
	switch g.DateBucket {
	case DateBucketDay, DateBucketWeek, DateBucketMonth, DateBucketQuarter:
		return nil
	}
	return invalid(g.Field, "Choose day, week, month or quarter for %s.", field.Label)
}

func validateTotals(totals []Total, src Source, grouped bool) error {
	if len(totals) > MaxTotals {
		return invalid("totals", "A report can have at most %d totals.", MaxTotals)
	}
	if grouped && len(totals) == 0 {
		return invalid("totals", "Add at least one total.")
	}
	for _, t := range totals {
		switch t.Fn {
		case AggCount:
			continue
		case AggSum, AggAvg, AggMin, AggMax:
		default:
			return invalid("totals", "Unknown total %q.", t.Fn)
		}
		if t.Field == "" {
			return invalid("totals", "Choose a field to %s.", t.Fn)
		}
		field, ok := src.Field(t.Field)
		if !ok {
			return invalid(t.Field, "%q is no longer available.", t.Field)
		}
		if field.Type != FieldNumber && field.Type != FieldMoney {
			return invalid(t.Field, "%s can only total number fields.", strings.ToUpper(string(t.Fn[:1]))+string(t.Fn[1:]))
		}
	}
	return nil
}

func validateSort(def Definition, grouped bool) error {
	if def.Sort == nil {
		return nil
	}
	key := def.Sort.Key
	if grouped {
		if key == "group" {
			return nil
		}
		// Strict: Sscanf would accept "total:0x" or "total:00", which the
		// compiler cannot find and turns into ORDER BY 0.
		if m := totalSortKey.FindStringSubmatch(key); m != nil {
			if idx, err := strconv.Atoi(m[1]); err == nil && idx < len(def.Totals) {
				return nil
			}
		}
	} else {
		for _, c := range def.Columns {
			if c == key {
				return nil
			}
		}
	}
	return invalid("sort", "Sort by something that is in the report.")
}
