package refund

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefund_JSONShape_RefundDate(t *testing.T) {
	rf := Refund{ID: "abc", Number: "RFND-000001", RefundDate: "2026-01-02"}
	b, err := json.Marshal(rf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "2026-01-02", m["refundDate"])
}
