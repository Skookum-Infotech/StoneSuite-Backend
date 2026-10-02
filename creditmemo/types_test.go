package creditmemo

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreditMemoJSON_DateIsPlainDate(t *testing.T) {
	b, err := json.Marshal(CreditMemo{CreditMemoDate: "2026-01-02"})
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "2026-01-02", m["creditMemoDate"])
}
