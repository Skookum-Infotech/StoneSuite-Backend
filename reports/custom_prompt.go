package reports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The plain-English side of Custom Reports. The model never writes SQL and
// never sees a user's data: it is asked, in two small calls sized for a
// CPU-only 3B model with a 4096-token window, (1) which source fits the
// request and (2) for a Definition over that one source's fields. Everything it
// returns is then repaired and re-validated here, so a wrong answer degrades to
// a smaller report, never to unsafe SQL.

// ErrUninterpretable means the model's reply could not be turned into a report.
var ErrUninterpretable = errors.New("could not turn the request into a report")

// maxSummaryRunes bounds the one-line reading shown to the user.
const maxSummaryRunes = 200

// defaultColumns is how many of a source's own fields an empty column list gets.
const defaultColumns = 3

// Presets the model may name for a date filter (the UI's presets).
var presetNames = []string{"this-month", "last-month", "this-quarter", "year-to-date", "last-12-months", "custom"}

// sourceHints tell the model what each source is for in a few words.
var sourceHints = map[string]string{
	"quotes":               "quotes sent to customers, quoted amounts",
	"estimates":            "estimates and their totals",
	"sales_orders":         "orders, order totals, delivery and install dates",
	"sales_order_lines":    "items sold on orders, ordered quantities",
	"invoices":             "invoices, amounts billed, balance due, paid, overdue",
	"invoice_lines":        "revenue and quantities sold by item, customer or material",
	"payments":             "payments received, payment methods",
	"leads":                "leads, lead sources, new leads",
	"prospects":            "prospects in the sales pipeline",
	"customers":            "customers, credit limits, AR status",
	"vendors":              "vendors and suppliers",
	"purchase_orders":      "purchase orders, open or received",
	"purchase_order_lines": "items bought, quantities ordered and received",
	"vendor_bills":         "bills from vendors, amounts payable, balance due",
	"items":                "inventory items, SKUs, prices",
	"stock":                "stock on hand and its value by location, reorder points",
	"slabs":                "individual slabs: serial, status, bin, size",
}

// obj is a JSON object that keeps its keys in the order given, so the model's
// constrained output is generated in a sensible order (summary first).
type obj []struct {
	k string
	v any
}

func (o obj) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, e := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(e.k)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(e.v)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func kv(k string, v any) struct {
	k string
	v any
} {
	return struct {
		k string
		v any
	}{k, v}
}

func enumOf(values []string) obj {
	return obj{kv("type", "string"), kv("enum", values)}
}

// ---- stage 1: choose the source -------------------------------------------

// SourceChoiceSchema constrains the model's reply to one visible source key.
func SourceChoiceSchema(sources []Source) []byte {
	keys := make([]string, len(sources))
	for i, s := range sources {
		keys[i] = s.Key
	}
	b, _ := json.Marshal(obj{
		kv("type", "object"),
		kv("properties", obj{kv("source", enumOf(keys))}),
		kv("required", []string{"source"}),
	})
	return b
}

// SourceChoicePrompt lists each visible source with a few words on what it is
// for. It deliberately carries no fields: prefill on the CPU-only model runs at
// roughly 20 tokens a second.
func SourceChoicePrompt(sources []Source) string {
	var b strings.Builder
	b.WriteString("Pick the ONE record type that best answers the user's request. ")
	b.WriteString(`Reply with JSON like {"source":"<key>"}.` + "\nRecord types:\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "- %s: %s (%s)\n", s.Key, s.Label, sourceHints[s.Key])
	}
	return b.String()
}

// extractJSON returns the outermost {...} in the model's reply, tolerating
// prose or a code fence around it.
func extractJSON(raw string) (string, bool) {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return "", false
	}
	return raw[start : end+1], true
}

// ParseSourceChoice reads stage 1's reply. The source must be one the caller
// may run.
func ParseSourceChoice(raw string, sources []Source) (Source, error) {
	body, ok := extractJSON(raw)
	if !ok {
		return Source{}, ErrUninterpretable
	}
	var out struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return Source{}, ErrUninterpretable
	}
	for _, s := range sources {
		if s.Key == out.Source {
			return s, nil
		}
	}
	return Source{}, ErrUninterpretable
}

// ---- stage 2: fill in the definition --------------------------------------

func fieldKeys(src Source, keep func(Field) bool) []string {
	keys := make([]string, 0, len(src.Fields))
	for _, f := range src.Fields {
		if keep == nil || keep(f) {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

func isNumeric(f Field) bool { return f.Type == FieldNumber || f.Type == FieldMoney }

func allOperators() []string {
	return []string{"eq", "neq", "contains", "in", "is_empty", "gt", "gte", "lt", "lte", "between", "date_range"}
}

// DefinitionSchema constrains stage 2's reply to this source's own field keys.
func DefinitionSchema(src Source) []byte {
	keys := fieldKeys(src, nil)
	withEmpty := func(in []string) []string { return append([]string{""}, in...) }
	schema := obj{
		kv("type", "object"),
		kv("properties", obj{
			kv("summary", obj{kv("type", "string")}),
			kv("source", enumOf([]string{src.Key})),
			kv("columns", obj{kv("type", "array"), kv("items", enumOf(keys))}),
			kv("filters", obj{kv("type", "array"), kv("items", obj{
				kv("type", "object"),
				kv("properties", obj{
					kv("field", enumOf(keys)),
					kv("op", enumOf(allOperators())),
					kv("values", obj{kv("type", "array"), kv("items", obj{kv("type", "string")})}),
					kv("preset", enumOf(presetNames)),
				}),
				kv("required", []string{"field", "op", "values"}),
			})}),
			kv("groupBy", obj{
				kv("type", "object"),
				kv("properties", obj{
					kv("field", enumOf(withEmpty(keys))),
					kv("dateBucket", enumOf([]string{"", "day", "week", "month", "quarter"})),
				}),
				kv("required", []string{"field"}),
			}),
			kv("totals", obj{kv("type", "array"), kv("items", obj{
				kv("type", "object"),
				kv("properties", obj{
					kv("fn", enumOf([]string{"sum", "avg", "min", "max", "count"})),
					kv("field", enumOf(withEmpty(fieldKeys(src, isNumeric)))),
				}),
				kv("required", []string{"fn"}),
			})}),
			kv("sort", obj{
				kv("type", "object"),
				kv("properties", obj{kv("key", obj{kv("type", "string")}), kv("desc", obj{kv("type", "boolean")})}),
				kv("required", []string{"key"}),
			}),
			kv("topN", obj{kv("type", "integer")}),
			kv("chart", enumOf([]string{ChartBar, ChartNone})),
		}),
		kv("required", []string{"summary", "source", "columns", "filters", "totals", "chart"}),
	}
	b, _ := json.Marshal(schema)
	return b
}

const definitionRules = `Rules:
- Use ONLY the field keys listed below (key:type).
- columns: fields to list when NOT grouping; use [] when grouping.
- filters: {"field","op","values"}. text: eq|neq|contains|in|is_empty. number/money: eq|neq|gt|gte|lt|lte|between(2 values)|is_empty. date: date_range. status: in (values are status names such as Paid).
- Dates: use op date_range with a preset (this-month|last-month|this-quarter|year-to-date|last-12-months) and values []. Only for exact dates use preset "custom" and values ["YYYY-MM-DD","YYYY-MM-DD"].
- groupBy: the field to group by (a date field also needs dateBucket day|week|month|quarter). No grouping: field "".
- totals (only when grouping): sum|avg|min|max of a number/money field, or count with no field.
- "top 10 X by Y": groupBy X, totals [sum Y], sort {"key":"total:0","desc":true}, topN 10.
- chart: "bar" when grouped, otherwise "none".
- summary: one short sentence saying what you built.`

// DefinitionPrompt is stage 2's system prompt: the rules plus only this
// source's fields, and the current report when the user is changing one.
func DefinitionPrompt(src Source, today time.Time, current *Definition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You turn a business user's request into a report definition over %s. Reply with JSON only.\n", src.Label)
	fmt.Fprintf(&b, "Today is %s.\n", today.Format(customDateLayout))
	b.WriteString(definitionRules + "\nFields:\n")
	for i, f := range src.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s:%s", f.Key, f.Type)
	}
	b.WriteString("\n")
	if current != nil {
		raw, _ := json.Marshal(current)
		fmt.Fprintf(&b, "Current report (the user wants to CHANGE it; return the full updated definition): %s\n", raw)
	}
	return b.String()
}

// ResolvePreset turns a relative range into inclusive ISO dates, matching the
// UI's presets. "custom" and unknown names have no window of their own.
func ResolvePreset(preset string, now time.Time) (from, to string, ok bool) {
	y, m, _ := now.Date()
	loc := now.Location()
	first := func(year int, month time.Month) time.Time { return time.Date(year, month, 1, 0, 0, 0, 0, loc) }
	today := now.Format(customDateLayout)
	switch preset {
	case "this-month":
		return first(y, m).Format(customDateLayout), today, true
	case "last-month":
		// Day 0 of this month is the last day of the previous one.
		return first(y, m-1).Format(customDateLayout), time.Date(y, m, 0, 0, 0, 0, 0, loc).Format(customDateLayout), true
	case "this-quarter":
		quarterStart := time.Month((int(m)-1)/3*3 + 1)
		return first(y, quarterStart).Format(customDateLayout), today, true
	case "year-to-date":
		return first(y, time.January).Format(customDateLayout), today, true
	case "last-12-months":
		return first(y, m-11).Format(customDateLayout), today, true
	}
	return "", "", false
}

// Interpretation is a Definition the model proposed, already repaired.
type Interpretation struct {
	Definition Definition `json:"definition"`
	Summary    string     `json:"summary"`
	Dropped    []string   `json:"dropped"` // parts of the reply that were left out as unusable
}

type modelOutput struct {
	Summary string       `json:"summary"`
	Columns []string     `json:"columns"`
	Filters []FilterRule `json:"filters"`
	GroupBy *GroupBy     `json:"groupBy"`
	Totals  []Total      `json:"totals"`
	Sort    *SortSpec    `json:"sort"`
	TopN    int          `json:"topN"`
	Chart   string       `json:"chart"`
}

// ParseInterpretation reads stage 2's reply for src, drops whatever is not a
// real field/operator/value, fills in sensible defaults and guarantees the
// result passes Validate. current (optional) supplies columns when an edit
// names none.
func ParseInterpretation(raw string, src Source, current *Definition, today time.Time) (Interpretation, error) {
	body, ok := extractJSON(raw)
	if !ok {
		return Interpretation{}, ErrUninterpretable
	}
	var out modelOutput
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return Interpretation{}, ErrUninterpretable
	}
	p := &repair{src: src, today: today, dropped: []string{}}
	def := Definition{Source: src.Key, Columns: []string{}, Filters: []FilterRule{}, Totals: []Total{}}

	def.GroupBy = p.groupBy(out.GroupBy)
	if def.GroupBy == nil {
		def.Columns = p.columns(out.Columns, current)
	} else {
		def.Totals = p.totals(out.Totals)
		if len(def.Totals) == 0 {
			def.Totals = []Total{{Fn: AggCount}}
		}
	}
	for _, f := range out.Filters {
		if rule, ok := p.filter(f); ok && len(def.Filters) < MaxFilters {
			def.Filters = append(def.Filters, rule)
		}
	}
	def.TopN = min(max(out.TopN, 0), MaxTopN)
	def.Chart = ChartNone
	if out.Chart == ChartBar && def.GroupBy != nil {
		def.Chart = ChartBar
	}
	if out.Sort != nil && out.Sort.Key != "" {
		def.Sort = out.Sort
		if validateSort(def, def.GroupBy != nil) != nil {
			def.Sort = nil
		}
	}
	if err := Validate(def, src); err != nil {
		return Interpretation{}, ErrUninterpretable
	}
	summary := strings.TrimSpace(out.Summary)
	if summary == "" {
		summary = DescribeDefinition(def, src)
	}
	if utf8.RuneCountInString(summary) > maxSummaryRunes {
		summary = string([]rune(summary)[:maxSummaryRunes])
	}
	return Interpretation{Definition: def, Summary: summary, Dropped: p.dropped}, nil
}

// repair keeps the usable parts of a model reply and remembers what it dropped.
type repair struct {
	src     Source
	today   time.Time
	dropped []string
}

func (p *repair) drop(what string) {
	for _, d := range p.dropped {
		if d == what {
			return
		}
	}
	p.dropped = append(p.dropped, what)
}

func (p *repair) columns(in []string, current *Definition) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, key := range in {
		if _, ok := p.src.Field(key); !ok {
			p.drop(key)
			continue
		}
		if !seen[key] && len(out) < MaxColumns {
			seen[key] = true
			out = append(out, key)
		}
	}
	if len(out) > 0 {
		return out
	}
	if current != nil && current.Source == p.src.Key {
		for _, key := range current.Columns {
			if _, ok := p.src.Field(key); ok {
				out = append(out, key)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, f := range p.src.Fields {
		if f.Group == "" && len(out) < defaultColumns {
			out = append(out, f.Key)
		}
	}
	return out
}

func (p *repair) groupBy(g *GroupBy) *GroupBy {
	if g == nil || g.Field == "" {
		return nil
	}
	field, ok := p.src.Field(g.Field)
	if !ok {
		p.drop(g.Field)
		return nil
	}
	if field.Type != FieldDate {
		return &GroupBy{Field: g.Field}
	}
	switch g.DateBucket {
	case DateBucketDay, DateBucketWeek, DateBucketMonth, DateBucketQuarter:
		return &GroupBy{Field: g.Field, DateBucket: g.DateBucket}
	}
	return &GroupBy{Field: g.Field, DateBucket: DateBucketMonth}
}

func (p *repair) totals(in []Total) []Total {
	out := []Total{}
	for _, t := range in {
		if len(out) >= MaxTotals {
			break
		}
		if t.Fn == AggCount {
			out = append(out, Total{Fn: AggCount})
			continue
		}
		field, ok := p.src.Field(t.Field)
		if !ok || !isNumeric(field) || (t.Fn != AggSum && t.Fn != AggAvg && t.Fn != AggMin && t.Fn != AggMax) {
			p.drop(firstNonEmpty(t.Field, string(t.Fn)))
			continue
		}
		out = append(out, t)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (p *repair) filter(f FilterRule) (FilterRule, bool) {
	field, ok := p.src.Field(f.Field)
	if !ok || !operatorAllowed(field.Type, f.Op) {
		p.drop(f.Field)
		return FilterRule{}, false
	}
	values := make([]string, 0, len(f.Values))
	for _, v := range f.Values {
		if isNumeric(field) {
			v = strings.TrimSpace(strings.ReplaceAll(v, ",", ""))
		}
		values = append(values, v)
	}
	preset := f.Preset
	f.Values, f.Preset = values, ""
	if f.Op == OpDateRange {
		f = p.withDateWindow(f, preset)
	}
	if err := validateFilterValues(field, f); err != nil {
		p.drop(f.Field)
		return FilterRule{}, false
	}
	return f, true
}

// withDateWindow fills a relative preset's window from today; any other date
// range keeps the model's own dates and is marked custom.
func (p *repair) withDateWindow(f FilterRule, preset string) FilterRule {
	if from, to, ok := ResolvePreset(preset, p.today); ok {
		f.Values, f.Preset = []string{from, to}, preset
		return f
	}
	f.Preset = "custom"
	return f
}

// DescribeDefinition is a one-line reading of a report, e.g. "Invoice lines
// grouped by Customer name · Sum of Net amount".
func DescribeDefinition(def Definition, src Source) string {
	parts := []string{src.Label}
	if def.GroupBy != nil {
		label := def.GroupBy.Field
		if f, ok := src.Field(def.GroupBy.Field); ok {
			label = f.Label
		}
		group := "grouped by " + label
		if def.GroupBy.DateBucket != "" {
			group += fmt.Sprintf(" (%s)", def.GroupBy.DateBucket)
		}
		parts[0] += " " + group
		if len(def.Totals) > 0 {
			parts = append(parts, strings.Join(totalLabels(def.Totals, src), ", "))
		}
	} else {
		parts = append(parts, plural(len(def.Columns), "column"))
	}
	if n := len(def.Filters); n > 0 {
		parts = append(parts, plural(n, "filter"))
	}
	if def.TopN > 0 {
		parts = append(parts, fmt.Sprintf("top %d", def.TopN))
	}
	return strings.Join(parts, " · ")
}

func totalLabels(totals []Total, src Source) []string {
	out := make([]string, 0, len(totals))
	for _, t := range totals {
		if t.Fn == AggCount {
			out = append(out, "Count")
			continue
		}
		label := t.Field
		if f, ok := src.Field(t.Field); ok {
			label = f.Label
		}
		out = append(out, fmt.Sprintf("%s of %s", aggTitles[t.Fn], label))
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
