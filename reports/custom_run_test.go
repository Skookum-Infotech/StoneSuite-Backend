package reports

import "testing"

func rowsOf(n int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = []any{"row", float64(i)}
	}
	return rows
}

func TestShapeRows(t *testing.T) {
	tests := []struct {
		name          string
		raw           int
		limit         int
		topN          bool
		wantRows      int
		wantTruncated bool
	}{
		{"one past the limit is cut and flagged", 501, 500, false, 500, true},
		{"exactly at the limit is whole", 500, 500, false, 500, false},
		{"fewer rows than the limit", 3, 500, false, 3, false},
		{"empty", 0, 500, false, 0, false},
		{"a top-N cut is what the user asked for, not truncation", 11, 10, true, 10, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := shapeRows(rowsOf(tc.raw), tc.limit, tc.topN)
			if len(got) != tc.wantRows || truncated != tc.wantTruncated {
				t.Fatalf("got %d rows truncated=%v, want %d rows truncated=%v", len(got), truncated, tc.wantRows, tc.wantTruncated)
			}
		})
	}
}

func TestShapeRowsNeverReturnsNil(t *testing.T) {
	got, _ := shapeRows(nil, 500, false)
	if got == nil {
		t.Fatal("a JSON-encoded result must be [] not null")
	}
}

func TestShapeTotals(t *testing.T) {
	count, totals := shapeTotals([]any{float64(42), nil, 1500.5})
	if count != 42 {
		t.Errorf("count = %d, want 42", count)
	}
	if len(totals) != 2 || totals[0] != nil || totals[1] != 1500.5 {
		t.Errorf("totals = %v, want [nil 1500.5]", totals)
	}
	if c, tt := shapeTotals(nil); c != 0 || len(tt) != 0 {
		t.Errorf("no totals row should be zero, got %d %v", c, tt)
	}
}

func TestStatusStages(t *testing.T) {
	for _, code := range []string{"LEAD", "PROS", "CUST"} {
		if !isCRMStage(code) {
			t.Errorf("%s is a CRM stage", code)
		}
	}
	if isCRMStage("INVC") {
		t.Error("INVC is a document status list")
	}
}

func TestValidReportID(t *testing.T) {
	if !IsUUID("3f2b8a14-6d0e-4c52-9a1f-0b7e5d3c8a21") {
		t.Fatal("a real uuid should pass")
	}
	if IsUUID("not-a-uuid") || IsUUID("") {
		t.Fatal("junk must fail so the store never sends it to a ::uuid cast")
	}
}
