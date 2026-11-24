package employee

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// recordTypeCode is the lkp_record_type code for Employee.
const recordTypeCode = "EMPL"

// FormatNumber renders the document number from the row's serial PK, zero-padded
// to 6 digits: EMPL-000001. The fallback when the "employee" workflow has no
// Record Numbering config enabled (and the format schema.sql's backfill uses).
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", recordTypeCode, serialID)
}

// numberTarget wires employees into the per-workflow Record Numbering config
// (Configure -> Record Numbering).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "employee",
	UpdateSQL:   `UPDATE employee SET employee_number = $1 WHERE employee_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims an employee's record number -- the configured format when
// numbering is enabled for the workflow, otherwise FormatNumber -- and writes it
// to the row. Must run inside the create transaction. A misconfigured number
// surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
