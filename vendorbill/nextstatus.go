// vendorbill/nextstatus.go
package vendorbill

import (
	"context"
	"fmt"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// fillNextStatusCodes sets NextStatusCodes on every bill given from one read
// of the module's approver config -- a caller serving a list passes every
// row at once so the counts are fetched once per request, not per row.
func fillNextStatusCodes(ctx context.Context, q workflow.Querier, bills ...*VendorBill) error {
	if len(bills) == 0 {
		return nil
	}
	recordTypeID, err := recordTypeIDByCode(ctx, q, vbilRecordTypeCode)
	if err != nil {
		return fmt.Errorf("resolve VBIL record type: %w", err)
	}
	ungated, err := approvalchain.UngatedGates(ctx, q, moduleConfig(), recordTypeID)
	if err != nil {
		return err
	}
	for _, b := range bills {
		b.NextStatusCodes = approvalchain.NextStatusCodes(b.StatusCode, NextStatuses, ungated)
	}
	return nil
}
