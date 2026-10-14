package bintransfer

import (
	"errors"
	"reflect"
	"testing"

	"stonesuite-backend/approvalchain"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"draft submits for approval", "DRFT", "PAPV", true},
		{"draft can be cancelled", "DRFT", "CANC", true},
		{"draft cannot jump to approved", "DRFT", "APPV", false},
		{"draft cannot jump to completed", "DRFT", "COMP", false},

		{"pending is approved", "PAPV", "APPV", true},
		{"pending goes back to draft", "PAPV", "DRFT", true},
		{"pending can be cancelled", "PAPV", "CANC", true},
		{"pending cannot complete", "PAPV", "COMP", false},

		{"approved completes", "APPV", "COMP", true},
		{"approved is revised back to draft", "APPV", "DRFT", true},
		{"approved can be cancelled", "APPV", "CANC", true},
		{"approved cannot go back to pending", "APPV", "PAPV", false},

		// A completed transfer has already moved the stone; there is no
		// un-moving it, only a new transfer back.
		{"completed is terminal", "COMP", "DRFT", false},
		{"completed cannot be cancelled", "COMP", "CANC", false},
		{"cancelled is terminal", "CANC", "DRFT", false},

		{"self transition", "DRFT", "DRFT", false},
		{"unknown from", "XXXX", "DRFT", false},
		{"unknown to", "DRFT", "XXXX", false},
		{"empty codes", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransition(tc.from, tc.to); got != tc.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "PAPV"); err != nil {
		t.Errorf("ValidateTransition(DRFT, PAPV) = %v, want nil", err)
	}
	if err := ValidateTransition("COMP", "DRFT"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("ValidateTransition(COMP, DRFT) = %v, want ErrInvalidTransition", err)
	}
}

func TestNextStatuses(t *testing.T) {
	tests := []struct {
		from string
		want []string
	}{
		{"DRFT", []string{"CANC", "PAPV"}},
		{"PAPV", []string{"APPV", "CANC", "DRFT"}},
		{"APPV", []string{"CANC", "COMP", "DRFT"}},
		{"COMP", []string{}},
		{"CANC", []string{}},
		{"XXXX", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.from, func(t *testing.T) {
			got := NextStatuses(tc.from)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NextStatuses(%q) = %v, want %v", tc.from, got, tc.want)
			}
		})
	}
}

// With nobody configured to approve, the Pending Approval checkpoint is
// collapsed out of the status control: a draft goes straight to Completed
// (the stone is moved) without ever stopping on Pending or Approved.
func TestNextStatuses_UngatedCheckpointCollapsesToComplete(t *testing.T) {
	cfg, ok := approvalchain.ForWorkflowKey("bin_transfer")
	if !ok {
		t.Fatal(`"bin_transfer" is not registered in approvalchain`)
	}
	ungated := map[string]approvalchain.Gate{}
	for _, g := range cfg.Gates {
		ungated[g.StatusCode] = g
	}
	got := approvalchain.NextStatusCodes("DRFT", NextStatuses, ungated)
	want := []string{"CANC", "COMP"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ungated NextStatusCodes(DRFT) = %v, want %v", got, want)
	}

	gated := approvalchain.NextStatusCodes("DRFT", NextStatuses, map[string]approvalchain.Gate{})
	wantGated := []string{"CANC", "PAPV"}
	if !reflect.DeepEqual(gated, wantGated) {
		t.Errorf("gated NextStatusCodes(DRFT) = %v, want %v", gated, wantGated)
	}
}

func TestIsEditable(t *testing.T) {
	for code, want := range map[string]bool{
		"DRFT": true, "PAPV": false, "APPV": false, "COMP": false, "CANC": false, "": false,
	} {
		if got := IsEditable(code); got != want {
			t.Errorf("IsEditable(%q) = %v, want %v", code, got, want)
		}
	}
}

// A slab belongs to at most one open transfer. Open = it still intends to move
// stone: Draft, Pending and Approved. Completed and Cancelled release the slab.
func TestIsOpen(t *testing.T) {
	for code, want := range map[string]bool{
		"DRFT": true, "PAPV": true, "APPV": true, "COMP": false, "CANC": false, "": false,
	} {
		if got := IsOpen(code); got != want {
			t.Errorf("IsOpen(%q) = %v, want %v", code, got, want)
		}
	}
}
