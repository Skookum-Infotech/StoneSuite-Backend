//go:build dbtest

package fabrication

import (
	"context"
	"errors"
	"testing"
)

// stepsByCode returns the job's checklist rows for one step code.
func stepsByCode(t *testing.T, job *Job, code string) []Step {
	t.Helper()
	var out []Step
	for _, s := range job.Steps {
		if s.Code == code {
			out = append(out, s)
		}
	}
	return out
}

// A piece-grain step is seeded once per piece. Updating it must touch only the
// addressed piece, and an ambiguous update must be rejected rather than
// completing the step for pieces nobody worked on.
func TestUpdateStep_TargetsOnePiece(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}
	in.Pieces = []PieceInput{{PieceName: "Island"}, {PieceName: "Perimeter"}}
	job, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saw := stepsByCode(t, job, "SAW_CUTTING")
	if len(saw) != 2 || saw[0].PieceUUID == "" || saw[0].PieceUUID == saw[1].PieceUUID {
		t.Fatalf("want 2 SAW_CUTTING rows with distinct piece uuids, got %+v", saw)
	}

	var ce ClientError
	// No piece on a multi-row step: ambiguous.
	if _, err := UpdateStep(ctx, pool, job.ID, "SAW_CUTTING", "", StepCompleted, "", nil, 1); !errors.As(err, &ce) {
		t.Fatalf("ambiguous update: want ClientError, got %T: %v", err, err)
	}
	// A piece on a job-grain step: meaningless.
	if _, err := UpdateStep(ctx, pool, job.ID, "INTAKE_VERIFY", saw[0].PieceUUID, StepCompleted, "", nil, 1); !errors.As(err, &ce) {
		t.Fatalf("piece on job-grain step: want ClientError, got %T: %v", err, err)
	}
	// A piece from another job.
	otherIn := CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}
	otherIn.Pieces = []PieceInput{{PieceName: "Other"}}
	other, err := Create(ctx, pool, otherIn, 1)
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	foreign := stepsByCode(t, other, "SAW_CUTTING")[0].PieceUUID
	if _, err := UpdateStep(ctx, pool, job.ID, "SAW_CUTTING", foreign, StepCompleted, "", nil, 1); !errors.As(err, &ce) {
		t.Fatalf("foreign piece: want ClientError, got %T: %v", err, err)
	}

	got, err := UpdateStep(ctx, pool, job.ID, "SAW_CUTTING", saw[0].PieceUUID, StepCompleted, "", nil, 1)
	if err != nil {
		t.Fatalf("targeted update: %v", err)
	}
	if got.PieceUUID != saw[0].PieceUUID || got.Status != StepCompleted {
		t.Fatalf("returned step = %+v, want piece %s completed", got, saw[0].PieceUUID)
	}
	after, err := Get(ctx, pool, job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, s := range stepsByCode(t, after, "SAW_CUTTING") {
		want := StepPending
		if s.PieceUUID == saw[0].PieceUUID {
			want = StepCompleted
		}
		if s.Status != want {
			t.Errorf("piece %s status = %s, want %s", s.PieceUUID, s.Status, want)
		}
	}

	// A job-grain step still updates without a piece.
	if _, err := UpdateStep(ctx, pool, job.ID, "INTAKE_VERIFY", "", StepCompleted, "", nil, 1); err != nil {
		t.Fatalf("job-grain update: %v", err)
	}
}
