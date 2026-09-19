// invoice/nextstatus.go
package invoice

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// fillNextStatusCodes sets NextStatusCodes on every invoice given from one
// read of the module's approver config -- a caller serving a list passes
// every row at once so the counts are fetched once per request, not per row.
func fillNextStatusCodes(ctx context.Context, pool *pgxpool.Pool, invoices ...*Invoice) error {
	if len(invoices) == 0 {
		return nil
	}
	typeID, err := typeIDByCode(ctx, pool, moduleConfig().RecordTypeCode)
	if err != nil {
		return err
	}
	ungated, err := approvalchain.UngatedGates(ctx, pool, moduleConfig(), typeID)
	if err != nil {
		return err
	}
	for _, inv := range invoices {
		inv.NextStatusCodes = approvalchain.NextStatusCodes(inv.StatusCode, NextStatuses, ungated)
	}
	return nil
}
