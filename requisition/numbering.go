package requisition

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// numberPrefix is the REQN record-type code (lkp_record_type.record_type_code).
const numberPrefix = "REQN"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: REQN-000001. Used as the fallback when
// the "requisition" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires requisitions into the per-workflow Record Numbering config
// (Configure → Record Numbering, workflow_numbering_configs).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "requisition",
	UpdateSQL:   `UPDATE requisition SET requisition_number = $1 WHERE requisition_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a requisition's record number — the configured format
// when numbering is enabled for the "requisition" workflow, otherwise
// FormatNumber — and writes it to the row. Must run inside the create
// transaction. A misconfigured number (collides with an existing requisition,
// or too long for the column) surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
