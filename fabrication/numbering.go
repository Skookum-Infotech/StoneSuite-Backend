package fabrication

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// numberPrefix is the FJOB record-type code (lkp_record_type.record_type_code).
const numberPrefix = "FJOB"

// FormatNumber renders the human-readable job number from the row's serial PK,
// zero-padded to 6 digits: FJOB-000001. Used as the fallback when the
// "installation" workflow has no Record Numbering config enabled.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}

// numberTarget wires fabrication jobs into the per-workflow Record Numbering
// config (Configure → Record Numbering, workflow_numbering_configs). The
// fabrication module is the "installation" workflow (workflows.key).
var numberTarget = workflow.NumberTarget{
	WorkflowKey: "installation",
	UpdateSQL:   `UPDATE fabrication_job SET fabrication_job_number = $1 WHERE fabrication_job_id = $2`,
	Fallback:    FormatNumber,
}

// assignNumber claims a fabrication job's number — the configured format when
// numbering is enabled for the "installation" workflow, otherwise FormatNumber
// — and writes it to the row. Must run inside the create transaction. A
// misconfigured number (collides with an existing job, or too long for the
// column) surfaces as a ClientError rather than a 500.
func assignNumber(ctx context.Context, tx workflow.Querier, serialID int64) (string, error) {
	num, err := workflow.AssignRecordNumber(ctx, tx, numberTarget, serialID)
	if workflow.IsNumberConfigError(err) {
		return "", ClientError{Msg: workflow.NumberConfigMessage(err)}
	}
	return num, err
}
