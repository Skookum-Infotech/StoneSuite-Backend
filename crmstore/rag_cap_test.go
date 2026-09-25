package crmstore

import "testing"

func TestCapRecordFields(t *testing.T) {
	tests := []struct {
		name       string
		core       map[string]any
		custom     map[string]any
		priority   []string
		labels     map[string]string
		budget     int
		wantCore   map[string]any
		wantCustom map[string]any
	}{
		{
			name:       "everything fits under a generous budget",
			core:       map[string]any{"customer_name": "Acme", "customer_addr_city": "Bellari"},
			custom:     map[string]any{"deal_size": 5000},
			priority:   []string{"record_number", "customer_name"},
			budget:     ragRecordByteBudget,
			wantCore:   map[string]any{"customer_name": "Acme", "customer_addr_city": "Bellari"},
			wantCustom: map[string]any{"deal_size": 5000},
		},
		{
			name:       "nil and blank fields are free and always kept",
			core:       map[string]any{"customer_name": "Acme", "customer_dba_name": "", "customer_fax": nil},
			priority:   nil,
			budget:     ragRecordByteBudget,
			wantCore:   map[string]any{"customer_name": "Acme"},
			wantCustom: map[string]any{},
		},
		{
			name:     "priority field survives a tight budget that drops the rest",
			core:     map[string]any{"customer_name": "Acme Corporation International Holdings", "customer_addr_city": "Bellari"},
			priority: []string{"customer_name"},
			// "customer_name: Acme Corporation International Holdings\n" costs
			// 55 bytes; "customer_addr_city: Bellari\n" costs 28 more. 130 - the
			// 64-byte header reserve leaves 66: enough for the first, not both.
			budget:     130,
			wantCore:   map[string]any{"customer_name": "Acme Corporation International Holdings"},
			wantCustom: map[string]any{},
		},
		{
			name:       "a budget too small even for the first field yields nothing",
			core:       map[string]any{"customer_name": "Acme"},
			priority:   []string{"customer_name"},
			budget:     ragRecordHeaderOverheadBytes, // 0 remaining after the header reserve
			wantCore:   map[string]any{},
			wantCustom: map[string]any{},
		},
		{
			name: "once a field doesn't fit, a later smaller one is not squeezed in ahead of it",
			core: map[string]any{
				"a": "a fairly long value that eats the whole budget up", // costs 53
				"b": "x",                                                 // costs 5 — would fit alone, but "a" sorts first
			},
			priority: nil,
			// 74 - the 64-byte header reserve leaves 10: too little for "a"
			// (53), so capRecordFields stops there — "b" is never tried even
			// though its own 5-byte cost would have fit.
			budget:     74,
			wantCore:   map[string]any{},
			wantCustom: map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCore, gotCustom := capRecordFields(tt.core, tt.custom, tt.priority, tt.labels, tt.budget)
			if !mapsEqual(gotCore, tt.wantCore) {
				t.Errorf("core = %#v, want %#v", gotCore, tt.wantCore)
			}
			if !mapsEqual(gotCustom, tt.wantCustom) {
				t.Errorf("custom = %#v, want %#v", gotCustom, tt.wantCustom)
			}
		})
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
