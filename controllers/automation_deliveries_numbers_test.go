package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeliveryNumberSources guards the resource -> number-column map.
func TestDeliveryNumberSources(t *testing.T) {
	for _, res := range []string{"invoice", "quote", "estimate", "purchase_order", "vendor_bill"} {
		src, ok := deliveryNumberSources[res]
		require.True(t, ok, res)
		assert.Regexp(t, deliveryNumberIdentRE, src.Table)
		assert.Regexp(t, deliveryNumberIdentRE, src.UUIDColumn)
		assert.Regexp(t, deliveryNumberIdentRE, src.NumberColumn)
	}
}
