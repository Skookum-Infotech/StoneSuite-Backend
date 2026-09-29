package purchaseorder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

func TestValidateExpectedSlabs(t *testing.T) {
	tests := []struct {
		name     string
		expected *int
		tracking string
		wantErr  string
	}{
		{name: "not stated on a slab line", expected: nil, tracking: "serialized"},
		{name: "not stated on a quantity line", expected: nil, tracking: "quantity"},
		{name: "a count on a slab line", expected: intPtr(12), tracking: "serialized"},
		{name: "one slab", expected: intPtr(1), tracking: "serialized"},
		{name: "the largest allowed", expected: intPtr(maxExpectedSlabs), tracking: "serialized"},
		{name: "on a quantity line", expected: intPtr(12), tracking: "quantity", wantErr: "slab-tracked"},
		{name: "on a line with no item tracking", expected: intPtr(12), tracking: "", wantErr: "slab-tracked"},
		{name: "zero", expected: intPtr(0), tracking: "serialized", wantErr: "between 1 and"},
		{name: "negative", expected: intPtr(-3), tracking: "serialized", wantErr: "between 1 and"},
		{name: "too many", expected: intPtr(maxExpectedSlabs + 1), tracking: "serialized", wantErr: "between 1 and"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExpectedSlabs(3, tc.expected, tc.tracking)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, IsClientError(err), "must be a 400, got %T", err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "Line 3", "names the line")
		})
	}
}

// The form sends expectedSlabs on a line and reads expectedSlabs / slabsReceived
// back; this pins those wire keys so a rename on either side fails here.
func TestExpectedSlabsWireShape(t *testing.T) {
	t.Run("decodes what the form sends", func(t *testing.T) {
		var in LineInput
		require.NoError(t, json.Unmarshal([]byte(`{"lineNumber":1,"inventoryItemUuid":"x","quantity":600,"expectedSlabs":12}`), &in))
		require.NotNil(t, in.ExpectedSlabs)
		assert.Equal(t, 12, *in.ExpectedSlabs)
	})

	t.Run("a line without it decodes to nil", func(t *testing.T) {
		var in LineInput
		require.NoError(t, json.Unmarshal([]byte(`{"lineNumber":1,"quantity":4}`), &in))
		assert.Nil(t, in.ExpectedSlabs)
	})

	t.Run("encodes what the form reads", func(t *testing.T) {
		raw, err := json.Marshal(Line{ExpectedSlabs: intPtr(12), SlabsReceived: 11})
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, 12.0, got["expectedSlabs"])
		assert.Equal(t, 11.0, got["slabsReceived"])
	})

	t.Run("a plain line omits both keys", func(t *testing.T) {
		raw, err := json.Marshal(Line{})
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "expectedSlabs")
		assert.NotContains(t, string(raw), "slabsReceived")
	})
}
