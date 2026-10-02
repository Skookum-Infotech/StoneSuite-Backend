package cashtransfer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCashTransferTransferDateJSON asserts transferDate marshals as a plain yyyy-mm-dd string.
func TestCashTransferTransferDateJSON(t *testing.T) {
	b, err := json.Marshal(CashTransfer{TransferDate: "2026-03-04"})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, "2026-03-04", out["transferDate"])
}
