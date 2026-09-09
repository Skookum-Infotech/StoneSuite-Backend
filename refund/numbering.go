package refund

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

const numberPrefix = "RFND"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: RFND-000001. Used as the fallback when
// the "refund" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires refunds into the per-workflow Record Numbering config
// (Configure → Record Numbering, workflow_numbering_configs).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "refund",
	UpdateSQL:   `UPDATE refund SET refund_number = $1 WHERE refund_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a refund's record number — the configured format when
// numbering is enabled for the "refund" workflow, otherwise FormatNumber — and
// writes it to the row. Must run inside the create transaction. A misconfigured
// number (collides with an existing refund, or too long for the column)
// surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
