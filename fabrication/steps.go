package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Step statuses stored in fabrication_job_step.step_status.
const (
	StepPending    = "pending"
	StepInProgress = "in_progress"
	StepBlocked    = "blocked"
	StepSkipped    = "skipped"
	StepCompleted  = "completed"
)

// stepDef describes one of the 16 canonical checklist steps (spec §5). Grain is
// "job" (one row per job, NULL item) or "piece" (one row per fabricated piece).
type stepDef struct {
	Code     string
	Sequence int
	Grain    string // "job" | "piece"
	Label    string
}

// canonicalSteps is the ordered 16-step checklist seeded onto every job (§5).
// The gate a step precedes is derived from Sequence, not stored here.
var canonicalSteps = []stepDef{
	{"INTAKE_VERIFY", 1, "job", "Order Intake & Verification"},
	{"SLAB_ALLOCATE", 2, "job", "Inventory Check & Slab Allocation"},
	{"TEMPLATING", 3, "piece", "Digital/Physical Templating"},
	{"SLAB_LAYOUT", 4, "piece", "Slab Layout & Programming"},
	{"SAW_CUTTING", 5, "piece", "Primary Saw Cutting"},
	{"CNC_CUTTING", 6, "piece", "CNC Route / Waterjet Cutting"},
	{"EDGE_PROFILE", 7, "piece", "Edge Profiling & Polishing"},
	{"HAND_POLISH", 8, "piece", "Manual Detailing & Hand Polishing"},
	{"RODDING", 9, "piece", "Rodding & Reinforcement"},
	{"DRY_RUN", 10, "job", "Layout Match & Dry Run"},
	{"FINAL_QC", 11, "piece", "Final Quality Control"},
	{"SEALING", 12, "piece", "Sealing & Treatment"},
	{"BUNDLE_LOAD", 13, "job", "Bundling & A-Frame Loading"},
	{"DISPATCH", 14, "job", "Dispatch & Logistics"},
	{"SITE_INSTALL", 15, "job", "Site Installation"},
	{"SIGN_OFF", 16, "job", "Post-Install Sign-off"},
}

// reworkStepCodes are the steps reopened on a QCPD→EDGP rework transition (§5):
// edge profiling, hand polishing, rodding, and final QC.
var reworkStepCodes = []string{"EDGE_PROFILE", "HAND_POLISH", "RODDING", "FINAL_QC"}

// seedSteps inserts the 16 canonical checklist rows for a new job. Job-grain
// steps get a NULL item; piece-grain steps are seeded once per piece, or once
// with a NULL item when the job has no pieces yet (they can be re-seeded when
// pieces are added — omitted here for the create path's simplicity).
func seedSteps(ctx context.Context, tx pgx.Tx, jobInternalID int, pieceIDs []int) error {
	for _, s := range canonicalSteps {
		if s.Grain == "piece" && len(pieceIDs) > 0 {
			for _, pid := range pieceIDs {
				if err := insertStep(ctx, tx, jobInternalID, &pid, s); err != nil {
					return err
				}
			}
			continue
		}
		if err := insertStep(ctx, tx, jobInternalID, nil, s); err != nil {
			return err
		}
	}
	return nil
}

func insertStep(ctx context.Context, tx pgx.Tx, jobInternalID int, pieceID *int, s stepDef) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO fabrication_job_step
			(fabrication_job_id, fabrication_job_item_id, step_code, step_sequence, step_status)
		VALUES ($1, $2, $3, $4, 'pending')`,
		jobInternalID, pieceID, s.Code, s.Sequence)
	if err != nil {
		return fmt.Errorf("seed step %s: %w", s.Code, err)
	}
	return nil
}

// UpdateStep sets a step's status/notes/payload. A skipped step requires a
// non-empty reason (spec §5), otherwise the checklist can be bypassed silently.
func UpdateStep(ctx context.Context, pool *pgxpool.Pool, jobUUID, stepCode, status, notes string, payload map[string]any, actorEmployeeID int) (*Step, error) {
	if !validStepStatus(status) {
		return nil, ClientError{Msg: "Invalid step status."}
	}
	if status == StepSkipped && notes == "" {
		return nil, ClientError{Msg: "A skipped step requires a note explaining why."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update step: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var jobID int
	err = tx.QueryRow(ctx, `
		SELECT fabrication_job_id FROM fabrication_job
		WHERE fabrication_job_uuid = $1 AND fabrication_job_deleted_at IS NULL`, jobUUID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load job for step update: %w", err)
	}

	if payload == nil {
		payload = map[string]any{}
	}
	// Stamp start/complete timestamps as the status crosses those thresholds.
	var stepID int
	err = tx.QueryRow(ctx, `
		UPDATE fabrication_job_step SET
			step_status = $3,
			step_notes = $4,
			step_payload = $5,
			step_started_at = CASE WHEN $3 = 'in_progress' AND step_started_at IS NULL THEN NOW() ELSE step_started_at END,
			step_started_by = CASE WHEN $3 = 'in_progress' AND step_started_by IS NULL THEN $6 ELSE step_started_by END,
			step_completed_at = CASE WHEN $3 IN ('completed','skipped') THEN NOW() ELSE NULL END,
			step_completed_by = CASE WHEN $3 IN ('completed','skipped') THEN $6 ELSE NULL END
		WHERE fabrication_job_id = $1 AND step_code = $2
		RETURNING fabrication_job_step_id`,
		jobID, stepCode, status, notes, payload, nullableInt(actorEmployeeID)).Scan(&stepID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown step for this job."}
	}
	if err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update step: %w", err)
	}
	steps, err := loadSteps(ctx, pool, jobUUID)
	if err != nil {
		return nil, err
	}
	for i := range steps {
		if steps[i].Code == stepCode {
			return &steps[i], nil
		}
	}
	return nil, ErrNotFound
}

func validStepStatus(s string) bool {
	switch s {
	case StepPending, StepInProgress, StepBlocked, StepSkipped, StepCompleted:
		return true
	}
	return false
}
