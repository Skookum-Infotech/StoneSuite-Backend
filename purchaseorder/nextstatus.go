// purchaseorder/nextstatus.go
package purchaseorder

import (
	"context"
	"fmt"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// fillNextStatusCodes sets NextStatusCodes on every order given from one
// read of the module's approver config -- a caller serving a list passes
// every row at once so the counts are fetched once per request, not per row.
func fillNextStatusCodes(ctx context.Context, q workflow.Querier, orders ...*PurchaseOrder) error {
	if len(orders) == 0 {
		return nil
	}
	recordTypeID, err := recordTypeIDByCode(ctx, q, pordRecordTypeCode)
	if err != nil {
		return fmt.Errorf("resolve PORD record type: %w", err)
	}
	ungated, err := approvalchain.UngatedGates(ctx, q, moduleConfig(), recordTypeID)
	if err != nil {
		return err
	}
	for _, p := range orders {
		p.NextStatusCodes = approvalchain.NextStatusCodes(p.StatusCode, NextStatuses, ungated)
	}
	return nil
}
