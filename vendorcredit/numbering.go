package vendorcredit

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// numberPrefix prefixes every generated vendor credit number. Deliberately
// shorter than the VCRD record-type code (lkp_record_type.record_type_code)
// per the request's requested document-number format.
const numberPrefix = "VCR"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: VCR-000001. Used as the fallback when
// the "vendor_credit" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires vendor credits into the per-workflow Record Numbering
// config (Configure → Record Numbering, workflow_numbering_configs).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "vendor_credit",
	UpdateSQL:   `UPDATE vendor_credit SET vendor_credit_number = $1 WHERE vendor_credit_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a vendor credit's record number — the configured format
// when numbering is enabled for the "vendor_credit" workflow, otherwise
// FormatNumber — and writes it to the row. Must run inside the create
// transaction. A misconfigured number (collides with an existing vendor
// credit, or too long for the column) surfaces as a ClientError rather than a
// 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
