package globalsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAIRegistry_EveryHookHasAProvider catches an AI hook file whose key
// doesn't match a registered provider (it would silently never index), and a
// hook set missing a loader or lister (reconcile/indexing would nil-panic).
func TestAIRegistry_EveryHookHasAProvider(t *testing.T) {
	for key, h := range aiRegistry {
		_, ok := registry[key]
		assert.True(t, ok, "AI hooks %q have no globalsearch provider", key)
		assert.NotNil(t, h.Load, "%s: Load", key)
		assert.NotNil(t, h.ListLive, "%s: ListLive", key)
		assert.NotNil(t, h.Count, "%s: Count", key)
		assert.NotEmpty(t, h.RecordType, "%s: RecordType", key)
		assert.NotEmpty(t, h.AuditResource, "%s: AuditResource", key)
	}
}

// TestAIRegistry_RecordTypesAreUnique guards the grant map and rag_chunks
// record_type against two modules sharing one type name.
func TestAIRegistry_RecordTypesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, p := range AIProviders() {
		prev, dup := seen[p.AI.RecordType]
		assert.False(t, dup, "%s and %s share record type %q", prev, p.Key, p.AI.RecordType)
		seen[p.AI.RecordType] = p.Key
	}
	for _, crm := range []string{"lead", "prospect", "customer"} {
		_, clash := seen[crm]
		assert.False(t, clash, "module record type %q collides with a CRM workflow key", crm)
	}
}

func TestAIByLookups(t *testing.T) {
	p, ok := AIByRecordType("quote")
	require.True(t, ok)
	assert.Equal(t, "quote", p.Key)
	assert.True(t, p.AI.OwnScoped)

	_, ok = AIByAuditResource("quote")
	assert.True(t, ok)
	_, ok = AIByAuditResource("no_such_resource")
	assert.False(t, ok)
	_, ok = AIByRecordType("lead")
	assert.False(t, ok, "CRM types are loaded by the CRM store, not a module hook")
}

func TestSummarizeLines(t *testing.T) {
	line := func(n string) aiLine { return aiLine{Name: n, Quantity: 2, UnitPrice: 10, Total: 20} }
	six := []aiLine{line("a"), line("b"), line("c"), line("d"), line("e"), line("f")}
	tests := []struct {
		name  string
		lines []aiLine
		want  string
	}{
		{"none", nil, ""},
		{"one with sku", []aiLine{{Name: "Slab", SKU: "GR-1", Quantity: 1.5, UnitPrice: 450, Total: 675}}, "1. Slab (GR-1) x1.5 @ 450.00 = 675.00"},
		{"capped at five with remainder", six, "1. a x2 @ 10.00 = 20.00; 2. b x2 @ 10.00 = 20.00; 3. c x2 @ 10.00 = 20.00; 4. d x2 @ 10.00 = 20.00; 5. e x2 @ 10.00 = 20.00; (+1 more)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, summarizeLines(tt.lines)) })
	}
}
