package query

import (
	"strings"
	"testing"
)

type fakeSearchRes struct{ fakeSortRes }

func (fakeSearchRes) SearchPredicate(ph string) string {
	return "(t.number ILIKE '%'||" + ph + "||'%' OR t.memo ILIKE '%'||" + ph + "||'%')"
}

func TestBuild_Search_AppendsParameterizedPredicate(t *testing.T) {
	b, err := Build(Request{Search: "acme"}, fakeSearchRes{}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(b.Where, "t.number ILIKE '%'||$1||'%'") {
		t.Fatalf("search predicate not in WHERE: %q", b.Where)
	}
	if len(b.Args) != 1 || b.Args[0] != "acme" {
		t.Fatalf("search term must be bound as a parameter, got args=%v", b.Args)
	}
}

func TestBuild_Search_UnsupportedResolver_Is400(t *testing.T) {
	_, err := Build(Request{Search: "acme"}, fakeSortRes{}, 1) // fakeSortRes has no SearchPredicate
	if _, ok := err.(*InvalidFilterError); !ok {
		t.Fatalf("expected InvalidFilterError when search unsupported, got %v", err)
	}
}
