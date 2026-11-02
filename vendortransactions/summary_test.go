package vendortransactions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFoldCounts(t *testing.T) {
	sources := allGranted(t, false)
	byType := map[string]*Count{
		"vendor_credit": {Type: "vendor_credit", Count: 1, Statuses: []StatusCount{{Code: "APPV", Count: 1}}},
		"requisition": {Type: "requisition", Count: 3, Statuses: []StatusCount{
			{Code: "SENT", Count: 1}, {Code: "APPV", Count: 1}, {Code: "DRFT", Count: 1},
		}},
	}
	got := foldCounts(sources, byType)
	require.Len(t, got, 2, "types with no records are dropped")
	assert.Equal(t, "requisition", got[0].Type, "registry order, not map order")
	assert.Equal(t, "vendor_credit", got[1].Type)
	codes := []string{got[0].Statuses[0].Code, got[0].Statuses[1].Code, got[0].Statuses[2].Code}
	assert.Equal(t, []string{"APPV", "DRFT", "SENT"}, codes, "statuses sorted by code")
	assert.Empty(t, foldCounts(sources, map[string]*Count{}))
}

func TestScopeLimited(t *testing.T) {
	tests := []struct {
		name    string
		sources []granted
		want    bool
	}{
		{"every type readable with all scope", allGranted(t, false), false},
		{"every type readable but own-scoped", allGranted(t, true), true},
		{"one type missing", allGranted(t, false)[1:], true},
		{"nothing readable", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scope{sources: tt.sources}.limited())
		})
	}
}

func TestTypeInfos(t *testing.T) {
	got := typeInfos(allGranted(t, false)[3:5])
	require.Len(t, got, 2)
	assert.Equal(t, TypeInfo{Type: "vendor_bill", Label: "Vendor Bill", Domain: "purchases", Module: "vendor_bill"}, got[0])
	assert.Equal(t, "vendor_payment", got[1].Type)
	assert.Empty(t, typeInfos(nil))
}

func TestScopeByKey(t *testing.T) {
	sc := scope{sources: allGranted(t, false)}
	g, ok := sc.byKey("vendor_bill")
	require.True(t, ok)
	assert.Equal(t, "vendor_bill", g.Key)
	_, ok = scope{}.byKey("vendor_bill")
	assert.False(t, ok)
}
