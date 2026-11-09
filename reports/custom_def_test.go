package reports

import (
	"errors"
	"strings"
	"testing"
)

// defTestSource is a tiny stand-in catalog source: enough field types to hit
// every validation rule without depending on the real catalog's contents.
func defTestSource() Source {
	return Source{
		Key:   "invoice_lines",
		Label: "Invoice lines",
		Fields: []Field{
			{Key: "customer.name", Label: "Customer name", Type: FieldText},
			{Key: "net_amount", Label: "Net amount", Type: FieldMoney},
			{Key: "quantity", Label: "Quantity", Type: FieldNumber},
			{Key: "invoice_date", Label: "Invoice date", Type: FieldDate},
			{Key: "status", Label: "Status", Type: FieldStatus, StatusRecordType: "INV"},
			{Key: "memo", Label: "Memo", Type: FieldText},
		},
	}
}

func validGrouped() Definition {
	return Definition{
		Source:  "invoice_lines",
		GroupBy: &GroupBy{Field: "customer.name"},
		Totals:  []Total{{Fn: AggSum, Field: "net_amount"}, {Fn: AggCount}},
		Chart:   "bar",
	}
}

func TestValidate(t *testing.T) {
	manyColumns := make([]string, 21)
	for i := range manyColumns {
		manyColumns[i] = "memo"
	}
	tests := []struct {
		name    string
		mutate  func(d *Definition)
		wantErr string // "" = valid
	}{
		{"valid grouped", func(d *Definition) {}, ""},
		{"valid ungrouped list", func(d *Definition) {
			d.GroupBy, d.Totals, d.Chart = nil, nil, "none"
			d.Columns = []string{"customer.name", "net_amount"}
		}, ""},
		{"unknown column", func(d *Definition) {
			d.GroupBy, d.Totals, d.Chart = nil, nil, "none"
			d.Columns = []string{"nope"}
		}, `"nope" is no longer available`},
		{"ungrouped without columns", func(d *Definition) {
			d.GroupBy, d.Totals, d.Chart = nil, nil, "none"
		}, "Pick at least one column"},
		{"grouped without totals", func(d *Definition) { d.Totals = nil }, "Add at least one total"},
		{"bar chart without group", func(d *Definition) {
			d.GroupBy, d.Totals = nil, nil
			d.Columns = []string{"memo"}
		}, "A bar chart needs a group"},
		{"between with one value", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "net_amount", Op: OpBetween, Values: []string{"5"}}}
		}, "needs two values"},
		{"number value with thousands separator", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "net_amount", Op: OpGt, Values: []string{"1,000"}}}
		}, "must be a number"},
		{"number value NaN", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "quantity", Op: OpEq, Values: []string{"NaN"}}}
		}, "must be a number"},
		{"hostile text value is accepted (it only ever travels as an arg)", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "memo", Op: OpEq, Values: []string{"'; DROP TABLE invoice;--"}}}
		}, ""},
		{"date range end before start", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "invoice_date", Op: OpDateRange, Values: []string{"2026-02-01", "2026-01-01"}}}
		}, "must not be before"},
		{"date range not dates", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "invoice_date", Op: OpDateRange, Values: []string{"yesterday", "today"}}}
		}, "needs two dates"},
		{"operator not allowed for type", func(d *Definition) {
			d.Filters = []FilterRule{{Field: "memo", Op: OpGt, Values: []string{"a"}}}
		}, "cannot be filtered with"},
		{"too many columns", func(d *Definition) {
			d.GroupBy, d.Totals, d.Chart = nil, nil, "none"
			d.Columns = manyColumns
		}, "at most 20 columns"},
		{"too many filters", func(d *Definition) {
			rule := FilterRule{Field: "memo", Op: OpEq, Values: []string{"x"}}
			d.Filters = []FilterRule{rule, rule, rule, rule, rule, rule, rule, rule, rule, rule, rule}
		}, "at most 10 filters"},
		{"top n too large", func(d *Definition) { d.TopN = 101 }, "between 1 and 100"},
		{"sum on a text field", func(d *Definition) {
			d.Totals = []Total{{Fn: AggSum, Field: "memo"}}
		}, "can only total number fields"},
		{"sum without field", func(d *Definition) {
			d.Totals = []Total{{Fn: AggSum}}
		}, "Choose a field"},
		{"date group without bucket", func(d *Definition) {
			d.GroupBy = &GroupBy{Field: "invoice_date"}
		}, "Choose day, week, month or quarter"},
		{"date group with bucket", func(d *Definition) {
			d.GroupBy = &GroupBy{Field: "invoice_date", DateBucket: DateBucketMonth}
		}, ""},
		{"unknown source", func(d *Definition) { d.Source = "leads" }, "Unknown source"},
		{"unknown chart", func(d *Definition) { d.Chart = "pie" }, "chart"},
		{"sort by total out of range", func(d *Definition) {
			d.Sort = &SortSpec{Key: "total:5"}
		}, "Sort by something"},
		{"sort by total ok", func(d *Definition) {
			d.Sort = &SortSpec{Key: "total:1", Desc: true}
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def := validGrouped()
			tc.mutate(&def)
			err := Validate(def, defTestSource())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("want *ValidationError containing %q, got %v", tc.wantErr, err)
			}
			if !strings.Contains(verr.Message, tc.wantErr) {
				t.Fatalf("message %q does not contain %q", verr.Message, tc.wantErr)
			}
		})
	}
}

func TestOperatorsFor(t *testing.T) {
	tests := []struct {
		in   FieldType
		want []Operator
	}{
		{FieldText, []Operator{OpEq, OpNeq, OpContains, OpIn, OpIsEmpty}},
		{FieldNumber, []Operator{OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpBetween, OpIsEmpty}},
		{FieldMoney, []Operator{OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpBetween, OpIsEmpty}},
		{FieldDate, []Operator{OpDateRange, OpIsEmpty}},
		{FieldStatus, []Operator{OpIn}},
	}
	for _, tc := range tests {
		got := OperatorsFor(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestValidateRejectsMalformedSortKeys(t *testing.T) {
	for _, key := range []string{"total:0x", "total:00", "total:01", "total:-1", "total: 1", "total:1 ", "total:", "Total:0", "group "} {
		def := validGrouped()
		def.Sort = &SortSpec{Key: key}
		if err := Validate(def, defTestSource()); err == nil {
			t.Errorf("sort key %q must be rejected (it would become ORDER BY 0 and fail in Postgres)", key)
		}
	}
	for _, key := range []string{"group", "total:0", "total:1"} {
		def := validGrouped()
		def.Sort = &SortSpec{Key: key}
		if err := Validate(def, defTestSource()); err != nil {
			t.Errorf("sort key %q is valid: %v", key, err)
		}
	}
}

func TestValidateNumbersArePlainDecimals(t *testing.T) {
	for _, v := range []string{"0x1p4", "0x10", "Inf", "-Inf", "NaN", "1_000", "1,000", "", "  ", "1.2.3", "--1", "1e"} {
		def := validGrouped()
		def.Filters = []FilterRule{{Field: "net_amount", Op: OpGt, Values: []string{v}}}
		if err := Validate(def, defTestSource()); err == nil {
			t.Errorf("%q must be rejected: Postgres' ::numeric cast would fail it", v)
		}
	}
	for _, v := range []string{"10", " 10 ", "-3.5", "+2", ".5", "5.", "1e3", "2.5E-2"} {
		def := validGrouped()
		def.Filters = []FilterRule{{Field: "net_amount", Op: OpGt, Values: []string{v}}}
		if err := Validate(def, defTestSource()); err != nil {
			t.Errorf("%q is a plain decimal: %v", v, err)
		}
	}
}

func TestValidateRejectsNULBytesInValues(t *testing.T) {
	def := validGrouped()
	def.Filters = []FilterRule{{Field: "memo", Op: OpEq, Values: []string{"a\x00b"}}}
	err := Validate(def, defTestSource())
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.Message, "cannot be used") {
		t.Fatalf("a NUL byte makes Postgres fail with a 500, want a 400 ValidationError, got %v", err)
	}
}

// TestValidateTheExactShapesTheUISends pins the cross-repo contract: the
// builder saves a relative date range WITH its two dates (and the preset), and
// a preset alone, with no dates, is not a valid filter.
func TestValidateTheExactShapesTheUISends(t *testing.T) {
	withDates := validGrouped()
	withDates.Filters = []FilterRule{{Field: "invoice_date", Op: OpDateRange, Values: []string{"2026-10-01", "2026-11-06"}, Preset: "this-quarter"}}
	if err := Validate(withDates, defTestSource()); err != nil {
		t.Fatalf("a preset with its resolved dates must save: %v", err)
	}

	presetOnly := validGrouped()
	presetOnly.Filters = []FilterRule{{Field: "invoice_date", Op: OpDateRange, Values: []string{}, Preset: "this-quarter"}}
	err := Validate(presetOnly, defTestSource())
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.Message, "needs two dates") {
		t.Fatalf("a preset with no dates is not savable (the frontend must resolve it first), got %v", err)
	}
}
