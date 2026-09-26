package controllers

import (
	"testing"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
	"github.com/stretchr/testify/assert"

	"stonesuite-backend/ai"
)

func TestModuleCountClassification(t *testing.T) {
	tests := []struct {
		q        string
		wantKeys []string
		wantOK   bool
	}{
		{"how many quotes do we have", []string{"quote"}, true},
		{"How many of my quotes are there?", []string{"quote"}, true},
		{"how many quotes and leads", []string{"lead", "quote"}, true},
		{"how many quotes in Texas", nil, false}, // meaningful qualifier -> not a plain count
		{"how many angels dance on a pin", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			keys, ok := classifyCountQuestion(tt.q)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantKeys, keys)
		})
	}
}

func TestModuleFilteredCountRoutesToModuleNote(t *testing.T) {
	keys, ok := countObject("how many approved quotes do we have")
	assert.True(t, ok)
	assert.Equal(t, []string{"quote"}, keys)
	assert.True(t, hasFilterHintCountIntent("how many approved quotes do we have"))
	assert.True(t, anyModuleType(keys))
	assert.False(t, anyModuleType([]string{"lead"}))
}

func TestModuleCountFollowUp(t *testing.T) {
	history := []ragcore.Message{{Role: ragcore.RoleUser, Content: "how many leads do we have"}}
	keys, ok := classifyFollowUpCount("and quotes?", history)
	assert.True(t, ok)
	assert.Equal(t, []string{"quote"}, keys)
	_, ok = classifyFollowUpCount("and unicorns?", history)
	assert.False(t, ok)
}

func TestFormatCountAnswerUsesModuleLabel(t *testing.T) {
	assert.Equal(t, "You have 1 quote.", formatCountAnswer([]string{"quote"}, map[string]int{"quote": 1}, 1, ""))
	assert.Equal(t, "You have 3 quotes.", formatCountAnswer([]string{"quote"}, map[string]int{"quote": 3}, 3, ""))
	assert.Equal(t,
		"You have 2 leads, 3 quotes (5 records total).",
		formatCountAnswer([]string{"lead", "quote"}, map[string]int{"lead": 2, "quote": 3}, 5, ""))
	assert.Equal(t,
		"You have 2 leads, 1 customer (3 CRM records total).",
		formatCountAnswer([]string{"lead", "customer"}, map[string]int{"lead": 2, "customer": 1}, 3, ""))
}

func TestModuleIntentCue(t *testing.T) {
	assert.Equal(t, intentData, classifyIntent("show me my quotes from last week", ""))
	assert.Equal(t, intentHelp, classifyIntent("how do I create a quote", ""), "a how-to about a module stays help")
}

func TestDescribeModuleDoc(t *testing.T) {
	doc := ragcore.RecordDoc{
		Core: map[string]any{
			"number": "QUOT-000012", "status": "Draft", "customer": "Acme", "grand_total": 1234.5,
			"memo": "  ", "line_items": "1. Slab x1 @ 10.00 = 10.00",
		},
		Priority: []string{"number", "status", "customer", "date", "grand_total"},
	}
	assert.Equal(t,
		"Status: Draft; Customer: Acme; Grand total: 1234.50; Line items: 1. Slab x1 @ 10.00 = 10.00",
		describeModuleDoc(doc))
}

func TestRecordTypeLabel(t *testing.T) {
	assert.Equal(t, "quote", recordTypeLabel("quote"))
	assert.Equal(t, "lead", recordTypeLabel("lead"))
	assert.True(t, isModuleType("quote"))
	assert.False(t, isModuleType("lead"))
}

func TestModuleCoverageAnswer(t *testing.T) {
	quoteGrant := ai.Grants{"quote": ai.ScopeOwn}
	tests := []struct {
		name    string
		q       string
		grants  ai.Grants
		wantAns string // "" = not handled here (proceeds to RAG)
	}{
		{"indexed + granted proceeds", "what is the total on the Smith quote", quoteGrant, ""},
		{"indexed but no grant says so", "what is the total on the Smith quote", ai.Grants{}, "You don't have access to quote records."},
		{"unindexed module is honest", "what is the balance on the Smith invoice", quoteGrant, "I can't answer questions about invoice records yet. You can find them in the invoice section."},
		{"CRM type word defers to RAG", "which customers asked for an invoice", quoteGrant, ""},
		{"no module noun proceeds", "what is my favourite colour", quoteGrant, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, ok := moduleCoverageAnswer(tt.q, tt.grants)
			assert.Equal(t, tt.wantAns != "", ok)
			assert.Equal(t, tt.wantAns, res.Answer)
		})
	}
}
