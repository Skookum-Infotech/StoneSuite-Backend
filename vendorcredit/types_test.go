package vendorcredit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVendorCredit_CreditDateJSON(t *testing.T) {
	b, err := json.Marshal(VendorCredit{CreditDate: "2026-08-24"})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "2026-08-24", got["creditDate"])
}
