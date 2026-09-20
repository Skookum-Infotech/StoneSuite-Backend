// salesorder/nextstatus.go
package salesorder

import (
	"context"
	"fmt"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// moduleConfig resolves the shared approvalchain.ModuleConfig for Sales
// Order (workflows.key "sales_order"). Sales Order predates the
// approvalchain engine and keeps its own approval.go, but the registry is
// the one place its gate is declared, so the checkpoint-collapsing helpers
// read it from there.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("sales_order")
	if !ok {
		panic("approvalchain: \"sales_order\" is not registered")
	}
	return cfg
}

// fillNextStatusCodes sets NextStatusCodes on every order given from one
// read of the module's approver config -- a caller serving a list passes
// every row at once so the counts are fetched once per request, not per row.
func fillNextStatusCodes(ctx context.Context, q workflow.Querier, orders ...*Order) error {
	if len(orders) == 0 {
		return nil
	}
	recordTypeID, err := recordTypeIDByCode(ctx, q, sordRecordTypeCode)
	if err != nil {
		return fmt.Errorf("resolve SORD record type: %w", err)
	}
	ungated, err := approvalchain.UngatedGates(ctx, q, moduleConfig(), recordTypeID)
	if err != nil {
		return err
	}
	for _, o := range orders {
		o.NextStatusCodes = approvalchain.NextStatusCodes(o.StatusCode, NextStatuses, ungated)
	}
	return nil
}
