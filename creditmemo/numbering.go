package creditmemo

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// numberPrefix matches the CRDT record type code seeded in lkp_record_type.
const numberPrefix = "CRDT"

// FormatNumber renders a credit memo's human-facing document number from its
// serial primary key, e.g. CRDT-000001. Used as the fallback when the
// "credit_memo" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires credit memos into the per-workflow Record Numbering config
// (Configure → Record Numbering, workflow_numbering_configs).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "credit_memo",
	UpdateSQL:   `UPDATE credit_memo SET credit_memo_number = $1 WHERE credit_memo_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a credit memo's record number — the configured format
// when numbering is enabled for the "credit_memo" workflow, otherwise
// FormatNumber — and writes it to the row. Must run inside the create
// transaction. A misconfigured number (collides with an existing credit memo,
// or too long for the column) surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
