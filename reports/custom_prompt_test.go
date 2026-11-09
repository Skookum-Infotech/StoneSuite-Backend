package reports

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"stonesuite-backend/authz"
)

var promptToday = time.Date(2026, time.November, 6, 9, 0, 0, 0, time.UTC)

func salesOnly(t *testing.T) []Source {
	t.Helper()
	return VisibleSources(func(r authz.Resource) bool { return r == authz.ResourceReportSales }, true)
}

func TestSourceChoiceSchemaListsOnlyVisibleSources(t *testing.T) {
	schema := string(SourceChoiceSchema(salesOnly(t)))
	if !strings.Contains(schema, `"invoice_lines"`) {
		t.Errorf("schema should allow invoice_lines: %s", schema)
	}
	if strings.Contains(schema, `"leads"`) || strings.Contains(schema, `"vendors"`) {
		t.Errorf("schema must not mention sources the caller cannot run: %s", schema)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
}

func TestSourceChoicePromptIsShort(t *testing.T) {
	prompt := SourceChoicePrompt(salesOnly(t))
	if !strings.Contains(prompt, "invoice_lines") || !strings.Contains(prompt, "Invoice lines") {
		t.Errorf("prompt should name each source: %s", prompt)
	}
	if strings.Contains(prompt, "net_amount") {
		t.Error("the source-choice prompt must not list fields: a CPU-only 3B model prefills ~20 tokens/s")
	}
	if n := len(prompt); n > 1200 {
		t.Errorf("source-choice prompt is %d bytes, keep it small", n)
	}
}

func TestParseSourceChoice(t *testing.T) {
	sources := salesOnly(t)
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"plain", `{"source":"invoice_lines"}`, "invoice_lines", false},
		{"wrapped in prose", "Sure! ```json\n{\"source\":\"payments\"}\n```", "payments", false},
		{"unknown source", `{"source":"leads"}`, "", true},
		{"not json", `no idea`, "", true},
		{"empty", ``, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSourceChoice(tc.raw, sources)
			if tc.wantErr {
				if !errors.Is(err, ErrUninterpretable) {
					t.Fatalf("want ErrUninterpretable, got %v", err)
				}
				return
			}
			if err != nil || got.Key != tc.want {
				t.Fatalf("got %q, %v; want %q", got.Key, err, tc.want)
			}
		})
	}
}

func TestDefinitionSchemaConstrainsFieldKeys(t *testing.T) {
	inv, _ := SourceByKey("invoice_lines")
	schema := string(DefinitionSchema(inv))
	for _, want := range []string{`"customer.name"`, `"net_amount"`, `"this-quarter"`, `"date_range"`, `"quarter"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema should contain %s", want)
		}
	}
	if strings.Contains(schema, `"vendor.name"`) {
		t.Error("another source's field leaked into the schema")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
}

func TestDefinitionPrompt(t *testing.T) {
	inv, _ := SourceByKey("invoice_lines")
	prompt := DefinitionPrompt(inv, promptToday, nil)
	for _, want := range []string{"2026-11-06", "this-quarter", "net_amount", "customer.name", "Invoice lines"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt should contain %q", want)
		}
	}
	if n := len(prompt); n > 6000 {
		t.Errorf("definition prompt is %d bytes; a 4096-token window on a slow CPU box needs it lean", n)
	}

	cur := &Definition{Source: "invoice_lines", Columns: []string{"customer.name"}, Chart: ChartNone}
	withCurrent := DefinitionPrompt(inv, promptToday, cur)
	if !strings.Contains(withCurrent, `"columns":["customer.name"]`) {
		t.Errorf("an edit request must show the current report:\n%s", withCurrent)
	}
}

func TestResolvePreset(t *testing.T) {
	tests := []struct {
		preset, from, to string
		ok               bool
	}{
		{"this-month", "2026-11-01", "2026-11-06", true},
		{"last-month", "2026-10-01", "2026-10-31", true},
		{"this-quarter", "2026-10-01", "2026-11-06", true},
		{"year-to-date", "2026-01-01", "2026-11-06", true},
		{"last-12-months", "2025-12-01", "2026-11-06", true},
		{"custom", "", "", false},
		{"", "", "", false},
		{"someday", "", "", false},
	}
	for _, tc := range tests {
		from, to, ok := ResolvePreset(tc.preset, promptToday)
		if ok != tc.ok || from != tc.from || to != tc.to {
			t.Errorf("%q: got %q..%q ok=%v, want %q..%q ok=%v", tc.preset, from, to, ok, tc.from, tc.to, tc.ok)
		}
	}
}

func TestParseInterpretation(t *testing.T) {
	inv, _ := SourceByKey("invoice_lines")
	tests := []struct {
		name        string
		raw         string
		check       func(t *testing.T, got Interpretation)
		wantDropped []string
	}{
		{
			name: "a clean grouped report is kept as the model gave it",
			raw: `{"summary":"Revenue by customer","source":"invoice_lines","columns":[],"filters":[],
				"groupBy":{"field":"customer.name","dateBucket":""},"totals":[{"fn":"sum","field":"net_amount"}],
				"sort":{"key":"total:0","desc":true},"topN":10,"chart":"bar"}`,
			check: func(t *testing.T, got Interpretation) {
				want := Definition{
					Source: "invoice_lines", Columns: []string{}, Filters: []FilterRule{},
					GroupBy: &GroupBy{Field: "customer.name"}, Totals: []Total{{Fn: AggSum, Field: "net_amount"}},
					Sort: &SortSpec{Key: "total:0", Desc: true}, TopN: 10, Chart: ChartBar,
				}
				if !reflect.DeepEqual(got.Definition, want) {
					t.Errorf("definition = %+v\nwant %+v", got.Definition, want)
				}
				if got.Summary != "Revenue by customer" {
					t.Errorf("summary = %q", got.Summary)
				}
			},
		},
		{
			name: "unknown columns and filter fields are dropped and listed",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number","nope"],
				"filters":[{"field":"ghost","op":"eq","values":["1"]}],"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if !reflect.DeepEqual(got.Definition.Columns, []string{"invoice.number"}) || len(got.Definition.Filters) != 0 {
					t.Errorf("definition = %+v", got.Definition)
				}
			},
			wantDropped: []string{"nope", "ghost"},
		},
		{
			name: "a date preset is resolved to dates and kept",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],
				"filters":[{"field":"invoice.date","op":"date_range","values":[],"preset":"this-quarter"}],"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				f := got.Definition.Filters
				if len(f) != 1 || f[0].Preset != "this-quarter" || !reflect.DeepEqual(f[0].Values, []string{"2026-10-01", "2026-11-06"}) {
					t.Errorf("filters = %+v", f)
				}
			},
		},
		{
			name: "a custom range keeps the model's own dates",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],
				"filters":[{"field":"invoice.date","op":"date_range","values":["2026-01-01","2026-01-31"],"preset":"custom"}],"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if !reflect.DeepEqual(got.Definition.Filters[0].Values, []string{"2026-01-01", "2026-01-31"}) {
					t.Errorf("filters = %+v", got.Definition.Filters)
				}
			},
		},
		{
			name: "status names are kept as typed",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],
				"filters":[{"field":"invoice.status","op":"in","values":["Paid","Sent"]}],"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if !reflect.DeepEqual(got.Definition.Filters[0].Values, []string{"Paid", "Sent"}) {
					t.Errorf("filters = %+v", got.Definition.Filters)
				}
			},
		},
		{
			name: "thousands separators are stripped from number filters",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],
				"filters":[{"field":"net_amount","op":"gt","values":["1,000"]}],"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if !reflect.DeepEqual(got.Definition.Filters[0].Values, []string{"1000"}) {
					t.Errorf("filters = %+v", got.Definition.Filters)
				}
			},
		},
		{
			name: "an operator the field does not support drops the filter",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],
				"filters":[{"field":"customer.name","op":"gt","values":["a"]}],"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if len(got.Definition.Filters) != 0 {
					t.Errorf("filters = %+v", got.Definition.Filters)
				}
			},
			wantDropped: []string{"customer.name"},
		},
		{
			name: "a grouped report with no totals gets a count",
			raw: `{"summary":"x","source":"invoice_lines","columns":[],"filters":[],
				"groupBy":{"field":"customer.name"},"totals":[],"chart":"bar"}`,
			check: func(t *testing.T, got Interpretation) {
				if !reflect.DeepEqual(got.Definition.Totals, []Total{{Fn: AggCount}}) {
					t.Errorf("totals = %+v", got.Definition.Totals)
				}
			},
		},
		{
			name: "a bar chart without a group becomes no chart",
			raw:  `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],"filters":[],"totals":[],"chart":"bar"}`,
			check: func(t *testing.T, got Interpretation) {
				if got.Definition.Chart != ChartNone {
					t.Errorf("chart = %q", got.Definition.Chart)
				}
			},
		},
		{
			name: "grouping by a date without a bucket defaults to month",
			raw: `{"summary":"x","source":"invoice_lines","columns":[],"filters":[],
				"groupBy":{"field":"invoice.date","dateBucket":""},"totals":[{"fn":"count"}],"chart":"bar"}`,
			check: func(t *testing.T, got Interpretation) {
				if got.Definition.GroupBy == nil || got.Definition.GroupBy.DateBucket != DateBucketMonth {
					t.Errorf("groupBy = %+v", got.Definition.GroupBy)
				}
			},
		},
		{
			name: "an empty group field means no grouping, and an empty column list gets defaults",
			raw: `{"summary":"x","source":"invoice_lines","columns":[],"filters":[],
				"groupBy":{"field":"","dateBucket":""},"totals":[],"sort":{"key":"","desc":false},"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if got.Definition.GroupBy != nil || got.Definition.Sort != nil {
					t.Errorf("definition = %+v", got.Definition)
				}
				if len(got.Definition.Columns) == 0 {
					t.Error("an ungrouped report needs columns, so defaults are filled in")
				}
			},
		},
		{
			name: "top n is clamped",
			raw: `{"summary":"x","source":"invoice_lines","columns":[],"filters":[],
				"groupBy":{"field":"customer.name"},"totals":[{"fn":"count"}],"topN":500,"chart":"bar"}`,
			check: func(t *testing.T, got Interpretation) {
				if got.Definition.TopN != MaxTopN {
					t.Errorf("topN = %d", got.Definition.TopN)
				}
			},
		},
		{
			name: "an invalid sort is dropped",
			raw: `{"summary":"x","source":"invoice_lines","columns":["invoice.number"],"filters":[],
				"sort":{"key":"total:3","desc":true},"totals":[],"chart":"none"}`,
			check: func(t *testing.T, got Interpretation) {
				if got.Definition.Sort != nil {
					t.Errorf("sort = %+v", got.Definition.Sort)
				}
			},
		},
		{
			name: "an empty summary falls back to a generated one",
			raw: `{"summary":"","source":"invoice_lines","columns":[],"filters":[],
				"groupBy":{"field":"customer.name"},"totals":[{"fn":"sum","field":"net_amount"}],"chart":"bar"}`,
			check: func(t *testing.T, got Interpretation) {
				if got.Summary != "Invoice lines grouped by Customer name · Sum of Net amount" {
					t.Errorf("summary = %q", got.Summary)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseInterpretation(tc.raw, inv, nil, promptToday)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if verr := Validate(got.Definition, inv); verr != nil {
				t.Fatalf("the result must always pass Validate: %v", verr)
			}
			tc.check(t, got)
			if !reflect.DeepEqual(got.Dropped, normalizeDropped(tc.wantDropped)) {
				t.Errorf("dropped = %v, want %v", got.Dropped, tc.wantDropped)
			}
		})
	}
}

func normalizeDropped(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func TestParseInterpretationRejectsGarbage(t *testing.T) {
	inv, _ := SourceByKey("invoice_lines")
	for _, raw := range []string{"", "not json", `{"source":"invoice_lines"`, "[]"} {
		if _, err := ParseInterpretation(raw, inv, nil, promptToday); !errors.Is(err, ErrUninterpretable) {
			t.Errorf("%q: want ErrUninterpretable, got %v", raw, err)
		}
	}
}

func TestParseInterpretationKeepsCurrentColumnsOnEdit(t *testing.T) {
	inv, _ := SourceByKey("invoice_lines")
	current := &Definition{Source: "invoice_lines", Columns: []string{"customer.name", "net_amount"}, Chart: ChartNone}
	raw := `{"summary":"only paid","source":"invoice_lines","columns":[],"filters":[{"field":"invoice.status","op":"in","values":["Paid"]}],"totals":[],"chart":"none"}`
	got, err := ParseInterpretation(raw, inv, current, promptToday)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Definition.Columns, current.Columns) {
		t.Errorf("an edit that names no columns keeps the current ones, got %v", got.Definition.Columns)
	}
}

func TestDescribeDefinition(t *testing.T) {
	inv, _ := SourceByKey("invoice_lines")
	grouped := Definition{Source: "invoice_lines", GroupBy: &GroupBy{Field: "customer.name"}, Totals: []Total{{Fn: AggSum, Field: "net_amount"}}}
	if got := DescribeDefinition(grouped, inv); got != "Invoice lines grouped by Customer name · Sum of Net amount" {
		t.Errorf("got %q", got)
	}
	list := Definition{Source: "invoice_lines", Columns: []string{"customer.name", "net_amount"}, Filters: []FilterRule{{Field: "customer.name", Op: OpEq}}}
	if got := DescribeDefinition(list, inv); got != "Invoice lines · 2 columns · 1 filter" {
		t.Errorf("got %q", got)
	}
}
