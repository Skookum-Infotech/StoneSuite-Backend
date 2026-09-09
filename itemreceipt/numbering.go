package itemreceipt

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// numberPrefix is the IRCT record-type code (lkp_record_type.record_type_code).
const numberPrefix = "IRCT"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: IRCT-000001. Used as the fallback when
// the "item_receipt" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires item receipts into the per-workflow Record Numbering
// config (Configure → Record Numbering, workflow_numbering_configs).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "item_receipt",
	UpdateSQL:   `UPDATE item_receipt SET item_receipt_number = $1 WHERE item_receipt_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims an item receipt's record number — the configured format
// when numbering is enabled for the "item_receipt" workflow, otherwise
// FormatNumber — and writes it to the row. Must run inside the create
// transaction. A misconfigured number (collides with an existing item receipt,
// or too long for the column) surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
