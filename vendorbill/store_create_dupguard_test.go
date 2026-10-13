package vendorbill

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeDocNumber_VendorInvoice(t *testing.T) {
	cases := []struct{ a, b string }{
		{"INV-0042", "inv 42"},
		{" 0042 ", "42"},
	}
	for _, tc := range cases {
		t.Run(tc.a+"|"+tc.b, func(t *testing.T) {
			assert.Equal(t, NormalizeDocNumber(tc.a), NormalizeDocNumber(tc.b))
		})
	}
	assert.NotEqual(t, NormalizeDocNumber("INV-42"), NormalizeDocNumber("INV-43"))
}

func TestGuardDuplicateInvoice_BlankNumberIsNeverGuarded(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		u, n, err := guardDuplicateInvoice(context.Background(), nil, 1, in, false, "")
		assert.NoError(t, err)
		assert.Empty(t, u)
		assert.Empty(t, n)
	}
}

func TestDuplicateDocumentError(t *testing.T) {
	var err error = &DuplicateDocumentError{ExistingUUID: "u", ExistingNumber: "VBIL-000001"}
	var d *DuplicateDocumentError
	assert.True(t, errors.As(err, &d))
	assert.Contains(t, err.Error(), "VBIL-000001")
}

func TestPlanLinkedCreate_NoLinkedLinesIsPlainReference(t *testing.T) {
	po := 5
	plan, err := planLinkedCreate(context.Background(), nil, &po, []resolvedLine{{lineNumber: 1}})
	assert.NoError(t, err)
	assert.Nil(t, plan)
}

func TestPlanLinkedCreate_LinkWithoutPurchaseOrderIsRefused(t *testing.T) {
	plan, err := planLinkedCreate(context.Background(), nil, nil, []resolvedLine{{lineNumber: 1, purchaseOrderItemUUID: "x"}})
	assert.Nil(t, plan)
	assert.True(t, IsClientError(err))
}
