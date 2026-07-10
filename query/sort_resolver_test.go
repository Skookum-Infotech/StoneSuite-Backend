package query

import "testing"

// fakeSortRes resolves id + a NOT NULL numeric "grand_total" column and declares
// grand_total sortable via SortResolver.
type fakeSortRes struct{}

func (fakeSortRes) Resolve(key string) (string, DataType, bool) {
	switch key {
	case "id":
		return "t.uuid::text", TypeString, true
	case "created_at":
		return "t.created_at", TypeDate, true
	case "grand_total":
		return "t.grand_total::text", TypeString, true // filter expr (irrelevant to sort)
	}
	return "", "", false
}
func (fakeSortRes) SortExpr(key string) (string, DataType, bool) {
	if key == "grand_total" {
		return "t.grand_total", TypeNumber, true // sort uses the raw numeric column
	}
	return "", "", false
}

func TestBuild_SortResolver_AllowsExtraSortField(t *testing.T) {
	b, err := Build(Request{Sort: []SortKey{{Field: "grand_total", Dir: DirAsc}}}, fakeSortRes{}, 1)
	if err != nil {
		t.Fatalf("expected grand_total to be sortable, got %v", err)
	}
	if b.OrderBy != "t.grand_total ASC, t.uuid::text ASC" {
		t.Fatalf("unexpected order by: %q", b.OrderBy)
	}
}

func TestBuild_SortResolver_RejectsUnsortableField(t *testing.T) {
	_, err := Build(Request{Sort: []SortKey{{Field: "memo", Dir: DirAsc}}}, fakeSortRes{}, 1)
	if _, ok := err.(*InvalidFilterError); !ok {
		t.Fatalf("expected InvalidFilterError for unsortable field, got %v", err)
	}
}
