// Package query builds scope-safe, parameterized SQL for filtering, sorting,
// and keyset-paginating workflow records. It is store-agnostic: each store
// supplies a FieldResolver that maps a logical field key to a concrete SQL
// expression, so the same filter grammar drives both the v1 JSONB store and
// the v2 relational store.
//
// Security contract: the predicate this package emits is always meant to be
// ANDed onto a caller's RBAC scope clause — a filter can only narrow a result
// set, never widen it. Field keys are validated against the resolver's
// whitelist (unknown key => error, never raw SQL) and every value is bound as
// a parameter ($n). See builder.go for the assembly rules.
package query

import "fmt"

// DataType is the value type of a filterable field. It mirrors
// workflow.DataType (same string values) but is defined here so the query
// package stays dependency-free and importable by the workflow store without
// an import cycle. Stores translate workflow.DataType into this via a plain
// string conversion.
type DataType string

const (
	TypeString DataType = "string"
	TypeNumber DataType = "number"
	TypeDate   DataType = "date"
	TypeBool   DataType = "bool"
	TypeEnum   DataType = "enum"
	TypeEmail  DataType = "email"
)

// Operator is a comparison applied to a single field.
type Operator string

const (
	OpEq         Operator = "eq"
	OpNeq        Operator = "neq"
	OpContains   Operator = "contains"   // substring, case-insensitive
	OpStartsWith Operator = "startswith" // prefix, case-insensitive
	OpIn         Operator = "in"         // value is a list
	OpGt         Operator = "gt"
	OpGte        Operator = "gte"
	OpLt         Operator = "lt"
	OpLte        Operator = "lte"
	OpBetween    Operator = "between" // value is a [lo, hi] list
	OpIsEmpty    Operator = "is_empty"
	OpIsNull     Operator = "is_null"
)

// Direction is a sort order.
type Direction string

const (
	DirAsc  Direction = "asc"
	DirDesc Direction = "desc"
)

// Pagination + safety limits.
const (
	DefaultLimit = 25
	MaxLimit     = 100
)

// Clause is a single field comparison. Clauses in a Request are ANDed together.
type Clause struct {
	Field string   `json:"field"` // logical key: "company_name", "status", "cf:budget"
	Op    Operator `json:"op"`
	Value any      `json:"value"`
}

// SortKey is one ordering directive.
type SortKey struct {
	Field string    `json:"field"`
	Dir   Direction `json:"dir"`
}

// Request is the full filter/sort/page payload from a client.
type Request struct {
	Filters []Clause  `json:"filters"`
	Sort    []SortKey `json:"sort"`
	Limit   int       `json:"limit"`
	Cursor  string    `json:"cursor"`
	Search  string    `json:"search"` // optional global search term (see SearchResolver)
}

// InvalidFilterError signals a client mistake (unknown field, wrong operator
// for the field's type, malformed value). Controllers map it to HTTP 400 and
// surface Field + Msg; it must never become a 500.
type InvalidFilterError struct {
	Field string
	Msg   string
}

func (e *InvalidFilterError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("invalid filter on %q: %s", e.Field, e.Msg)
	}
	return "invalid filter: " + e.Msg
}

func invalid(field, msg string) error { return &InvalidFilterError{Field: field, Msg: msg} }

// FieldResolver maps a logical field key to a concrete SQL expression and the
// field's data type. Returning ok=false means the key is not in the whitelist.
// Each store implements this against its own schema (real columns vs JSONB).
type FieldResolver interface {
	Resolve(key string) (sqlExpr string, dt DataType, ok bool)
}

// SortResolver is an optional interface a FieldResolver may also implement to
// declare additional sortable columns beyond the built-in created_at/updated_at/
// record_number. The returned expression MUST be NOT NULL so keyset pagination
// stays correct (the builder always appends the row id as a tiebreaker). dt is
// used to coerce the cursor value for the keyset comparison.
type SortResolver interface {
	SortExpr(key string) (expr string, dt DataType, ok bool)
}

// opsByType is the whitelist of operators permitted per data type. Anything
// not listed here is rejected for that type.
var opsByType = map[DataType]map[Operator]bool{
	TypeString: setOf(OpEq, OpNeq, OpContains, OpStartsWith, OpIn, OpIsEmpty, OpIsNull),
	TypeEmail:  setOf(OpEq, OpNeq, OpContains, OpStartsWith, OpIn, OpIsEmpty, OpIsNull),
	TypeEnum:   setOf(OpEq, OpNeq, OpIn, OpIsEmpty, OpIsNull),
	TypeNumber: setOf(OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpBetween, OpIn, OpIsNull),
	TypeDate:   setOf(OpEq, OpGt, OpGte, OpLt, OpLte, OpBetween, OpIsNull),
	TypeBool:   setOf(OpEq, OpIsNull),
}

func setOf(ops ...Operator) map[Operator]bool {
	m := make(map[Operator]bool, len(ops))
	for _, o := range ops {
		m[o] = true
	}
	return m
}

// opAllowed reports whether op is valid for the given data type.
func opAllowed(dt DataType, op Operator) bool {
	m, ok := opsByType[dt]
	if !ok {
		// Unknown/unmapped type: allow only equality + null checks.
		return op == OpEq || op == OpIsNull
	}
	return m[op]
}
