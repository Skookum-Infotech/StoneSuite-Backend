package inventory

import "testing"

func TestAreaFor(t *testing.T) {
	tests := []struct {
		name      string
		lengthMM  float64
		widthMM   float64
		unitCode  string
		unitCat   string
		want      float64
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "typical granite slab in square feet",
			lengthMM: 3000, widthMM: 1400,
			unitCode: UnitCodeSquareFoot, unitCat: UnitCategoryArea,
			// 3000*1400 = 4,200,000 mm^2; / 304.8^2 (92903.04) = 45.208...
			want: 45.208,
		},
		{
			name:     "same slab in square metres",
			lengthMM: 3000, widthMM: 1400,
			unitCode: UnitCodeSquareM, unitCat: UnitCategoryArea,
			want: 4.2,
		},
		{
			// The 10.76x trap: identical dimensions, different unit. If a store
			// ever ledgers the SQM number against a SQFT item, on-hand is wrong
			// by this ratio and no database constraint catches it.
			name:     "square metre and square foot results differ by the conversion factor",
			lengthMM: 1000, widthMM: 1000,
			unitCode: UnitCodeSquareFoot, unitCat: UnitCategoryArea,
			want: 10.764,
		},
		{
			name:     "count-category item counts as exactly one regardless of size",
			lengthMM: 3000, widthMM: 1400,
			unitCode: "EA", unitCat: UnitCategoryCount,
			want: 1,
		},
		{
			name:     "count-category item with no dimensions is still one",
			lengthMM: 0, widthMM: 0,
			unitCode: "SLAB", unitCat: UnitCategoryCount,
			want: 1,
		},
		{
			name:     "linear foot uses length only",
			lengthMM: 3048, widthMM: 500,
			unitCode: UnitCodeLinearFoot, unitCat: UnitCategoryLength,
			want: 10,
		},
		{
			name:     "zero length is rejected for a measured piece",
			lengthMM: 0, widthMM: 1400,
			unitCode: UnitCodeSquareFoot, unitCat: UnitCategoryArea,
			wantErr: true, errSubstr: "greater than zero",
		},
		{
			name:     "negative width is rejected",
			lengthMM: 3000, widthMM: -1,
			unitCode: UnitCodeSquareFoot, unitCat: UnitCategoryArea,
			wantErr: true, errSubstr: "greater than zero",
		},
		{
			name:     "unknown area unit is rejected rather than guessed",
			lengthMM: 3000, widthMM: 1400,
			unitCode: "ACRE", unitCat: UnitCategoryArea,
			wantErr: true, errSubstr: "Unsupported area unit",
		},
		{
			name:     "weight cannot be derived from two dimensions",
			lengthMM: 3000, widthMM: 1400,
			unitCode: "KG", unitCat: UnitCategoryWeight,
			wantErr: true, errSubstr: "Cannot derive",
		},
		{
			name:     "volume cannot be derived from two dimensions",
			lengthMM: 3000, widthMM: 1400,
			unitCode: "L", unitCat: UnitCategoryVolume,
			wantErr: true, errSubstr: "Cannot derive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AreaFor(tt.lengthMM, tt.widthMM, tt.unitCode, tt.unitCat)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("AreaFor() = %v, want error", got)
				}
				if !contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.errSubstr)
				}
				if !IsClientError(err) {
					t.Errorf("error should be a ClientError so controllers map it to 400, got %T", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("AreaFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanCut(t *testing.T) {
	tests := []struct {
		name       string
		parentArea float64
		childAreas []float64
		wantRecov  float64
		wantScrap  float64
		wantErr    bool
		errSubstr  string
	}{
		{
			// The core accounting rule: kerf and dropped scrap are real losses,
			// so the shortfall is recorded rather than silently absorbed.
			name:       "kerf loss is recorded as scrap so the ledger still reconciles",
			parentArea: 45.208,
			childAreas: []float64{30.0, 12.5},
			wantRecov:  42.5,
			wantScrap:  2.708,
		},
		{
			name:       "a cut that recovers everything leaves no scrap",
			parentArea: 10,
			childAreas: []float64{6, 4},
			wantRecov:  10,
			wantScrap:  0,
		},
		{
			name:       "a cut with nothing retained scraps the whole parent",
			parentArea: 10,
			childAreas: nil,
			wantRecov:  0,
			wantScrap:  10,
		},
		{
			// Would manufacture stock out of a saw cut.
			name:       "children totalling more than the parent are rejected",
			parentArea: 10,
			childAreas: []float64{6, 5},
			wantErr:    true, errSubstr: "more area than",
		},
		{
			name:       "a zero-area child is rejected",
			parentArea: 10,
			childAreas: []float64{6, 0},
			wantErr:    true, errSubstr: "positive area",
		},
		{
			name:       "a parent with no area is rejected",
			parentArea: 0,
			childAreas: []float64{1},
			wantErr:    true, errSubstr: "no recorded area",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanCut(tt.parentArea, tt.childAreas)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("PlanCut() = %+v, want error", got)
				}
				if !contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.RecoveredArea != tt.wantRecov {
				t.Errorf("RecoveredArea = %v, want %v", got.RecoveredArea, tt.wantRecov)
			}
			if got.ScrappedArea != tt.wantScrap {
				t.Errorf("ScrappedArea = %v, want %v", got.ScrappedArea, tt.wantScrap)
			}
			// The invariant the whole design depends on: what leaves stock must
			// equal what comes back plus what was destroyed.
			if got.RecoveredArea+got.ScrappedArea != got.ParentArea {
				t.Errorf("recovered(%v) + scrapped(%v) != parent(%v); the ledger would drift",
					got.RecoveredArea, got.ScrappedArea, got.ParentArea)
			}
		})
	}
}

func TestIsUsableRemnant(t *testing.T) {
	tests := []struct {
		name                    string
		lengthMM, widthMM       float64
		minLengthMM, minWidthMM float64
		want                    bool
	}{
		{"comfortably above the threshold", 1200, 700, 600, 300, true},
		{"exactly on the threshold is usable", 600, 300, 600, 300, true},
		{"too small on both edges is scrap", 500, 250, 600, 300, false},
		{"long enough but too narrow is scrap", 1200, 200, 600, 300, false},
		{
			// 500x700 looks too short against a 600 minimum length, but the
			// 600x300 rectangle fits across the 700 edge. Judging edge-by-edge
			// on the nominal length/width would scrap a perfectly usable piece.
			name:     "a piece that only fits the minimum rotated is still usable",
			lengthMM: 500, widthMM: 700, minLengthMM: 600, minWidthMM: 300, want: true,
		},
		{
			// A 300x1200 offcut is the same piece as a 1200x300 one; judging it
			// on nominal length/width alone would wrongly scrap it.
			name:     "a rotated offcut is judged on its long and short edge",
			lengthMM: 300, widthMM: 1200, minLengthMM: 600, minWidthMM: 300, want: true,
		},
		{"no configured policy keeps everything", 10, 10, 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUsableRemnant(tt.lengthMM, tt.widthMM, tt.minLengthMM, tt.minWidthMM); got != tt.want {
				t.Errorf("IsUsableRemnant() = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
