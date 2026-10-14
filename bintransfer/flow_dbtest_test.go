// bintransfer/flow_dbtest_test.go
//go:build dbtest

package bintransfer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/inventory"
)

// With nobody configured to approve, a draft is completed directly: the
// checkpoint is collapsed out and the slab is moved in the same step.
func TestComplete_UngatedMovesSlabsAndTagsTheirHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	a, b := newSlab(t, pool, &from), newSlab(t, pool, nil)
	bt := draftMoving(t, pool, to, a, b)

	done, err := Complete(ctx, pool, bt.ID, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if done.StatusCode != StatusCompleted {
		t.Errorf("StatusCode = %q, want COMP", done.StatusCode)
	}
	if done.CompletedAt == nil {
		t.Error("CompletedAt not set")
	}
	for _, u := range []*inventory.Unit{a, b} {
		if got := binOf(t, pool, u.ID); got != to {
			t.Errorf("slab %s bin = %q, want %q", u.Serial, got, to)
		}
		total, tagged := moveHistory(t, pool, u.ID, bt.ID)
		if total != 1 || tagged != 1 {
			t.Errorf("slab %s bin_move history = %d total / %d tagged, want 1 / 1", u.Serial, total, tagged)
		}
	}
	for _, l := range done.Lines {
		if l.MovedAt == nil {
			t.Errorf("line %d MovedAt not set", l.LineNumber)
		}
	}

	// The unit's own history names the document, so its History tab can link to it.
	trail, err := inventory.UnitHistory(ctx, pool, a.ID)
	if err != nil {
		t.Fatalf("UnitHistory: %v", err)
	}
	var linked bool
	for _, e := range trail {
		if e.Action == "bin_move" && e.TransferNumber == bt.Number && e.TransferID == bt.ID {
			linked = true
		}
	}
	if !linked {
		t.Errorf("unit history %+v has no bin_move naming %s", trail, bt.Number)
	}

	// Completing twice must not move (or log) anything again.
	if _, err := Complete(ctx, pool, bt.ID, 1); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("second Complete = %v, want ErrInvalidTransition", err)
	}
	if total, _ := moveHistory(t, pool, a.ID, bt.ID); total != 1 {
		t.Errorf("history rows after a refused second complete = %d, want 1", total)
	}
}

func TestApproval_GatedTransferCompletesOnlyAfterSignOff(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, unit)

	// Gated: Complete is not offered from a draft and is refused.
	if bt.NextStatusCodes == nil || strings.Join(bt.NextStatusCodes, ",") != "CANC,PAPV" {
		t.Errorf("draft NextStatusCodes = %v, want [CANC PAPV] while an approver is configured", bt.NextStatusCodes)
	}
	if _, err := Complete(ctx, pool, bt.ID, 1); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Complete of an unapproved draft = %v, want ErrApprovalRequired", err)
	}

	submitted, err := Transition(ctx, pool, bt.ID, StatusPending, "", 1)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.StatusCode != StatusPending || submitted.ApprovalStatus != approvalchain.StatusPending {
		t.Fatalf("after submit status=%q approval=%q, want PAPV / pending", submitted.StatusCode, submitted.ApprovalStatus)
	}
	// Pending cannot be pushed through by hand, nor completed.
	if _, err := Transition(ctx, pool, bt.ID, StatusApproved, "", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Errorf("manual PAPV->APPV = %v, want ErrApprovalRequired", err)
	}
	if _, err := Complete(ctx, pool, bt.ID, 1); err == nil {
		t.Error("Complete of a pending transfer succeeded, want it refused")
	}
	if got := binOf(t, pool, unit.ID); got != from {
		t.Fatalf("slab moved before approval: bin = %q, want %q", got, from)
	}

	// Someone who is not a configured approver cannot sign it off.
	if _, err := Approve(ctx, pool, bt.ID, 999999, false); !errors.Is(err, ErrNotApprover) {
		t.Errorf("Approve by a non-approver = %v, want ErrNotApprover", err)
	}

	approved, err := Approve(ctx, pool, bt.ID, 1, false)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.StatusCode != StatusApproved {
		t.Fatalf("after approve status = %q, want APPV (the engine auto-advances)", approved.StatusCode)
	}
	if got := binOf(t, pool, unit.ID); got != from {
		t.Errorf("approving moved the slab (bin = %q); only Complete may", got)
	}

	done, err := Complete(ctx, pool, bt.ID, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if done.StatusCode != StatusCompleted {
		t.Errorf("after complete status = %q, want COMP", done.StatusCode)
	}
	if got := binOf(t, pool, unit.ID); got != to {
		t.Errorf("slab bin after complete = %q, want %q", got, to)
	}
}

func TestReject_SendsItBackToDraftWithTheReason(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, unit)
	if _, err := Transition(ctx, pool, bt.ID, StatusPending, "", 1); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if _, err := Reject(ctx, pool, bt.ID, 1, false, "  "); !errors.Is(err, approvalchain.ErrReasonRequired) {
		t.Errorf("Reject without a reason = %v, want ErrReasonRequired", err)
	}
	rejected, err := Reject(ctx, pool, bt.ID, 1, false, "Rack B2 is being re-bolted this week")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != StatusDraft {
		t.Errorf("status after reject = %q, want DRFT", rejected.StatusCode)
	}
	info, err := GetApprovalInfo(ctx, pool, bt.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Rack B2 is being re-bolted this week" {
		t.Fatalf("Rejection = %+v, want the reason kept", info.Rejection)
	}
	// It is editable again, and resubmitting clears the rejection.
	if _, err := Update(ctx, pool, bt.ID, Input{WarehouseID: 1, Lines: []LineInput{{UnitID: unit.ID, ToBinID: newBin(t, pool, 1)}}}, 1); err != nil {
		t.Fatalf("Update after reject: %v", err)
	}
	if _, err := Transition(ctx, pool, bt.ID, StatusPending, "", 1); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	info, err = GetApprovalInfo(ctx, pool, bt.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection != nil || !info.Gated {
		t.Errorf("info = %+v after resubmitting, want gated with no rejection", info)
	}
}

// Sign-offs belong to the version of the document they were given to. Revising
// an approved transfer must not let the old signatures count towards the next
// round.
func TestRevise_DiscardsEarlierSignOffs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, unit)
	if _, err := Transition(ctx, pool, bt.ID, StatusPending, "", 1); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := Approve(ctx, pool, bt.ID, 1, false); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	revised, err := Transition(ctx, pool, bt.ID, StatusDraft, "", 1)
	if err != nil {
		t.Fatalf("revise: %v", err)
	}
	if revised.StatusCode != StatusDraft {
		t.Fatalf("status after revise = %q, want DRFT", revised.StatusCode)
	}
	var signOffs int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bin_transfer_approval a
		JOIN bin_transfer bt ON bt.bin_transfer_id = a.bin_transfer_id
		WHERE bt.bin_transfer_uuid = $1`, bt.ID).Scan(&signOffs); err != nil {
		t.Fatalf("count sign-offs: %v", err)
	}
	if signOffs != 0 {
		t.Errorf("sign-offs after revise = %d, want 0", signOffs)
	}
}

// Configuration > Workflows > Bin Transfer drives the approver chain through
// approvalchain's generic config calls (GET/PUT .../approval-chain). This is that
// path, end to end: the gate is listed with its own label, setting an approver
// is read back, and from then on a transfer is gated -- and clearing it ungates
// the module again.
func TestApprovalChainConfig_IsAvailableToConfigureWorkflows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cfg, ok := approvalchain.ForWorkflowKey("bin_transfer")
	if !ok {
		t.Fatal(`"bin_transfer" is not in the approval registry`)
	}
	t.Cleanup(func() {
		_, _ = approvalchain.ReplaceApprovers(context.Background(), pool, cfg, "PAPV", []string{}, 0)
	})

	gates, err := approvalchain.GatesWithApprovers(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("GatesWithApprovers: %v", err)
	}
	if len(gates) != 1 || gates[0].StatusCode != "PAPV" || gates[0].StatusLabel != "Pending Approval" {
		t.Fatalf("gates = %+v, want the single Pending Approval gate", gates)
	}
	if len(gates[0].ApproverEmployeeIDs) != 0 {
		t.Fatalf("a fresh tenant has approvers configured: %v", gates[0].ApproverEmployeeIDs)
	}

	if _, err := approvalchain.ReplaceApprovers(ctx, pool, cfg, "PAPV", []string{"1"}, 1); err != nil {
		t.Fatalf("ReplaceApprovers: %v", err)
	}
	gates, err = approvalchain.GatesWithApprovers(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("GatesWithApprovers: %v", err)
	}
	if got := gates[0].ApproverEmployeeIDs; len(got) != 1 || got[0] != "1" {
		t.Fatalf("configured approvers = %v, want [1]", got)
	}

	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	gated := draftMoving(t, pool, to, newSlab(t, pool, &from))
	if strings.Join(gated.NextStatusCodes, ",") != "CANC,PAPV" {
		t.Errorf("with an approver configured NextStatusCodes = %v, want [CANC PAPV]", gated.NextStatusCodes)
	}

	if _, err := approvalchain.ReplaceApprovers(ctx, pool, cfg, "PAPV", []string{}, 1); err != nil {
		t.Fatalf("clearing approvers: %v", err)
	}
	ungated, err := Get(ctx, pool, gated.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Join(ungated.NextStatusCodes, ",") != "CANC,COMP" {
		t.Errorf("with the chain cleared NextStatusCodes = %v, want [CANC COMP]", ungated.NextStatusCodes)
	}
}
