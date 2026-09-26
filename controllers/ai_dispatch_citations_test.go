package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

func TestWithRetrievedFallback(t *testing.T) {
	cite := func(id string) ragcore.Citation { return ragcore.Citation{SourceType: "records", SourceID: id} }
	four := []ragcore.Citation{cite("1"), cite("2"), cite("3"), cite("4")}

	tests := []struct {
		name      string
		res       ragcore.AskResult
		retrieved []ragcore.Citation
		want      []ragcore.Citation
	}{
		{"grounded with no markers gets top retrieved", ragcore.AskResult{Grounded: true}, four, four[:maxFallbackCitations]},
		{"fewer than the cap are all kept", ragcore.AskResult{Grounded: true}, four[:2], four[:2]},
		{"model-cited sources win", ragcore.AskResult{Grounded: true, Citations: []ragcore.Citation{cite("9")}}, four, []ragcore.Citation{cite("9")}},
		{"refusal never gets sources", ragcore.AskResult{Grounded: false, Citations: []ragcore.Citation{}}, four, []ragcore.Citation{}},
		{"refusal is passed through unchanged even if Citations were non-empty", ragcore.AskResult{Grounded: false, Citations: []ragcore.Citation{cite("stale")}}, four, []ragcore.Citation{cite("stale")}},
		{"nothing retrieved stays empty", ragcore.AskResult{Grounded: true, Citations: []ragcore.Citation{}}, nil, []ragcore.Citation{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, withRetrievedFallback(tt.res, tt.retrieved))
		})
	}
}

type recordingSink struct {
	cites  []ragcore.Citation
	tokens []string
}

func (r *recordingSink) OnRetrieved(c []ragcore.Citation) error { r.cites = c; return nil }
func (r *recordingSink) OnToken(t string) error                 { r.tokens = append(r.tokens, t); return nil }

func TestRetrievedCollector(t *testing.T) {
	cites := []ragcore.Citation{{SourceID: "a"}}

	t.Run("nil next drops tokens but keeps the set", func(t *testing.T) {
		c := &retrievedCollector{}
		assert.NoError(t, c.OnRetrieved(cites))
		assert.NoError(t, c.OnToken("x"))
		assert.Equal(t, cites, c.retrieved)
	})
	t.Run("forwards to next", func(t *testing.T) {
		next := &recordingSink{}
		c := &retrievedCollector{next: next}
		assert.NoError(t, c.OnRetrieved(cites))
		assert.NoError(t, c.OnToken("x"))
		assert.Equal(t, cites, c.retrieved)
		assert.Equal(t, cites, next.cites)
		assert.Equal(t, []string{"x"}, next.tokens)
	})
}
