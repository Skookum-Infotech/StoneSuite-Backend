// estimate/nextstatus.go
package estimate

import (
	"context"
	"fmt"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// moduleConfig resolves the shared approvalchain.ModuleConfig for Estimate
// (workflows.key "estimate"). Estimate predates the approvalchain engine and
// keeps its own approval.go, but the registry is the one place its gate is
// declared, so the checkpoint-collapsing helpers read it from there.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("estimate")
	if !ok {
		panic("approvalchain: \"estimate\" is not registered")
	}
	return cfg
}

// fillNextStatusCodes sets NextStatusCodes on every estimate given from one
// read of the module's approver config -- a caller serving a list passes
// every row at once so the counts are fetched once per request, not per row.
func fillNextStatusCodes(ctx context.Context, q workflow.Querier, estimates ...*Estimate) error {
	if len(estimates) == 0 {
		return nil
	}
	recordTypeID, err := recordTypeIDByCode(ctx, q, estmRecordTypeCode)
	if err != nil {
		return fmt.Errorf("resolve ESTM record type: %w", err)
	}
	ungated, err := approvalchain.UngatedGates(ctx, q, moduleConfig(), recordTypeID)
	if err != nil {
		return err
	}
	for _, e := range estimates {
		e.NextStatusCodes = approvalchain.NextStatusCodes(e.StatusCode, NextStatuses, ungated)
	}
	return nil
}
