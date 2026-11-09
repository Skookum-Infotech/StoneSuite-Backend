package reports

import "stonesuite-backend/authz"

// The custom-report catalog: the only tables, columns and joins a custom report
// can ever reach. Every expr below is hand-written SQL; a Definition only
// *names* fields by key, so nothing a user or the AI model types becomes SQL.

// Field is one selectable, filterable, groupable value of a source.
type Field struct {
	Key   string    `json:"key"`   // "customer.name"
	Label string    `json:"label"` // "Customer name"
	Group string    `json:"group"` // "" = the source itself, else the linked record ("Customer")
	Type  FieldType `json:"type"`
	// StatusRecordType names the status list a FieldStatus field draws its
	// options from: an lkp_record_type code (INVC, PORD, ...), or for CRM
	// sources the stage code (LEAD, PROS, CUST).
	StatusRecordType string `json:"-"`
	// Options is a fixed option list for a FieldStatus field whose values are
	// not in a lookup table (slab status).
	Options []string `json:"-"`
	expr    string
	join    string // key into Source.joins, "" for base columns
}

// joinSpec is one LEFT JOIN a field may need, plus the joins it hangs off.
type joinSpec struct {
	sql   string
	needs []string
}

// Source is one record type a custom report can run on.
type Source struct {
	Key, Label, Module string
	Resource           authz.Resource // the module's report permission
	RequiresV2         bool           // CRM sources run on the relational design only
	from               string
	where              []string
	ownerExpr          string // "" = tenant-global
	joins              map[string]joinSpec
	joinOrder          []string // emit order, dependencies first
	Fields             []Field
}

// Field returns the field with the given key.
func (s Source) Field(key string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Sources lists every custom-report source in menu order.
func Sources() []Source {
	var out []Source
	out = append(out, salesSources()...)
	out = append(out, crmSources()...)
	out = append(out, purchaseSources()...)
	out = append(out, inventorySources()...)
	return out
}

// SourceByKey finds a source by its key.
func SourceByKey(key string) (Source, bool) {
	for _, s := range Sources() {
		if s.Key == key {
			return s, true
		}
	}
	return Source{}, false
}

// VisibleSources keeps the sources the caller may run: their module's report
// grant, and (for CRM) a relational-design tenant.
func VisibleSources(allowed func(authz.Resource) bool, designV2 bool) []Source {
	var out []Source
	for _, s := range Sources() {
		if s.RequiresV2 && !designV2 {
			continue
		}
		if allowed(s.Resource) {
			out = append(out, s)
		}
	}
	return out
}

// joinDef is the ordered form buildJoins turns into a Source's join maps.
type joinDef struct {
	name, sql string
	needs     []string
}

func buildJoins(defs ...joinDef) (map[string]joinSpec, []string) {
	m := make(map[string]joinSpec, len(defs))
	order := make([]string, 0, len(defs))
	for _, d := range defs {
		m[d.name] = joinSpec{sql: d.sql, needs: d.needs}
		order = append(order, d.name)
	}
	return m, order
}

// Field constructors keep the per-source lists readable.
func fld(t FieldType, key, label, expr, join string) Field {
	return Field{Key: key, Label: label, Type: t, expr: expr, join: join}
}
func text(key, label, expr, join string) Field   { return fld(FieldText, key, label, expr, join) }
func number(key, label, expr, join string) Field { return fld(FieldNumber, key, label, expr, join) }
func money(key, label, expr, join string) Field  { return fld(FieldMoney, key, label, expr, join) }
func date(key, label, expr, join string) Field   { return fld(FieldDate, key, label, expr, join) }
func status(key, label, expr, join, recordType string) Field {
	f := fld(FieldStatus, key, label, expr, join)
	f.StatusRecordType = recordType
	return f
}

// linked marks fields as belonging to a linked record, e.g. "Customer".
func linked(group string, fields ...Field) []Field {
	for i := range fields {
		fields[i].Group = group
	}
	return fields
}

func concat(parts ...[]Field) []Field {
	var out []Field
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// personName is an employee's full name, NULL when blank.
func personName(alias string) string {
	return "NULLIF(TRIM(" + alias + ".employee_first_name || ' ' || " + alias + ".employee_last_name), '')"
}
