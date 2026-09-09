package vendorpayment

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

const numberPrefix = "VPAY"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: VPAY-000001. Used as the fallback when
// the "vendor_payment" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires vendor payments into the per-workflow Record Numbering
// config (Configure → Record Numbering, workflow_numbering_configs).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "vendor_payment",
	UpdateSQL:   `UPDATE vendor_payment SET vendor_payment_number = $1 WHERE vendor_payment_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a vendor payment's record number — the configured format
// when numbering is enabled for the "vendor_payment" workflow, otherwise
// FormatNumber — and writes it to the row. Must run inside the create
// transaction. A misconfigured number (collides with an existing vendor
// payment, or too long for the column) surfaces as a ClientError rather than a
// 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
