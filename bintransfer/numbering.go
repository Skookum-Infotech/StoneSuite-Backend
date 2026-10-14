package bintransfer

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// FormatNumber renders the document number from the row's serial PK, zero-padded
// to 6 digits: BTRF-000001. The fallback when the "bin_transfer" workflow has no
// Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", recordTypeCode, serialID)
}

// numberTarget wires bin transfers into the per-workflow Record Numbering config
// (Configure -> Record Numbering).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "bin_transfer",
	UpdateSQL:   `UPDATE bin_transfer SET bin_transfer_number = $1 WHERE bin_transfer_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a bin transfer's record number -- the configured format
// when numbering is enabled for the workflow, otherwise FormatNumber -- and
// writes it to the row. Must run inside the create transaction. A misconfigured
// number surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
