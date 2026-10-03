package docextractjob

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSignificantTokens(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"drops legal forms", "ACME Stone, Inc.", []string{"acme", "stone"}},
		{"drops the and llc", "The Granite & Marble LLC", []string{"granite", "marble"}},
		{"keeps digits", "Studio 54 Ltd", []string{"studio", "54"}},
		{"empty", "", nil},
		{"only stopwords", "The Company Inc", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, SignificantTokens(tc.in)) })
	}
}

func TestTokenDice(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "Acme Stone", "ACME STONE", 1},
		{"legal form ignored", "Acme Stone Inc", "Acme Stone LLC", 1},
		{"half overlap", "Acme Stone", "Acme Tile", 0.5},
		{"subset", "Acme", "Acme Stone Works", 0.5},
		{"disjoint", "Acme Stone", "Zenith Tile", 0},
		{"empty a", "", "Acme", 0},
		{"stopwords only", "Inc", "LLC", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.InDelta(t, tc.want, TokenDice(tc.a, tc.b), 1e-9) })
	}
}

func TestRankByDice(t *testing.T) {
	cands := []string{"Zenith Tile", "Acme Stone Works", "Acme Stone", "Acme Tile", "Acme Stone Supply Co"}
	tests := []struct {
		name   string
		target string
		n      int
		want   []int
	}{
		{"top three by score", "Acme Stone", 3, []int{2, 1, 4}},
		{"zero scores dropped", "Zenith", 3, []int{0}},
		{"nothing matches", "Quartz", 3, nil},
		{"n larger than matches", "Acme Tile", 10, []int{3, 0, 2, 1, 4}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			for _, s := range RankByDice(tc.target, cands, tc.n) {
				got = append(got, s.Index)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPrefilterToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ACME Stone Inc", "acme"},
		{"The AB Granite", "granite"},
		{"Inc", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) { assert.Equal(t, tc.want, prefilterToken(tc.in)) })
	}
}
