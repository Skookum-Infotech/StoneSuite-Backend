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

func TestBuild_Search_MultiWord_AndsEachToken(t *testing.T) {
	b, err := Build(Request{Search: "acme  granite"}, fakeSearchRes{}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two tokens => two OR-fragments bound as $1 and $2, joined by AND.
	if !strings.Contains(b.Where, "$1") || !strings.Contains(b.Where, "$2") {
		t.Fatalf("both tokens must be bound: %q", b.Where)
	}
	if strings.Count(b.Where, " AND ") < 1 {
		t.Fatalf("tokens must be AND-ed, not OR-ed: %q", b.Where)
	}
	if len(b.Args) != 2 || b.Args[0] != "acme" || b.Args[1] != "granite" {
		t.Fatalf("got args=%v, want [acme granite]", b.Args)
	}
}

func TestBuild_Search_StripsLikeMetacharacters(t *testing.T) {
	b, err := Build(Request{Search: `50%_\x`}, fakeSearchRes{}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b.Args) != 1 || b.Args[0] != "50x" {
		t.Fatalf("LIKE metacharacters must be stripped, got args=%v", b.Args)
	}
}

func TestBuild_Search_OnlyMetacharacters_NoPredicate(t *testing.T) {
	b, err := Build(Request{Search: "%%%"}, fakeSearchRes{}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Where != "" || len(b.Args) != 0 {
		t.Fatalf("a term that strips to nothing must add no predicate, got where=%q args=%v", b.Where, b.Args)
	}
}
