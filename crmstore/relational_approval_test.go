package crmstore

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckTransitionGate(t *testing.T) {
	tests := []struct {
		name           string
		required       int
		approvalStatus string
		toStatusCode   string
		wantErr        bool
	}{
		{"not gated (no approvers configured) -> allowed", 0, StatusPending, "PDIS", false},
		{"gated, pending, ordinary target -> blocked", 1, StatusPending, "PDIS", true},
		{"gated, rejected, ordinary target -> blocked", 1, StatusRejected, "PDIS", true},
		{"gated, pending, lead unqualified exit -> allowed", 1, StatusPending, "LUNQ", false},
		{"gated, pending, prospect closed lost exit -> allowed", 1, StatusPending, "PCLL", false},
		{"gated, pending, customer closed lost exit -> allowed", 1, StatusPending, "CCLL", false},
		{"gated, rejected, closed lost exit -> allowed", 1, StatusRejected, "CCLL", false},
		{"gated but approved -> allowed", 1, StatusApproved, "PDIS", false},
		{"gated, approved, exit code too -> allowed", 1, StatusApproved, "CCLL", false},
		{"gated, none status (edge case) -> blocked", 1, StatusNone, "PDIS", true},
		{"gated, empty target code -> blocked (conversion path)", 1, StatusPending, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTransitionGate(tc.required, tc.approvalStatus, tc.toStatusCode)
			if tc.wantErr {
				assert.ErrorIs(t, err, ErrApprovalRequired)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestApprovalDecision(t *testing.T) {
	tests := []struct {
		name                  string
		status                string
		required              int
		callerIsApprover      bool
		callerIsSuperAdmin    bool
		callerAlreadyApproved bool
		approvalsSoFar        int
		wantErr               error
		wantOK                bool
		wantFinalize          bool
	}{
		{"pending, configured, single approver required -> finalize", StatusPending, 1, true, false, false, 0, nil, true, true},
		{"pending, configured, 2 required, first approval -> stays pending", StatusPending, 2, true, false, false, 0, nil, true, false},
		{"pending, configured, 2 required, second approval -> finalize", StatusPending, 2, true, false, false, 1, nil, true, true},
		{"pending, caller not approver, not super admin -> not authorized", StatusPending, 1, false, false, false, 0, ErrNotApprover, false, false},
		{"pending, caller already approved -> already approved by you", StatusPending, 2, true, false, true, 1, ErrAlreadyApprovedByYou, false, false},
		{"pending, nobody configured -> no approver configured", StatusPending, 0, false, false, false, 0, ErrNoApproverConfigured, false, false},
		{"pending, super admin override, not personally configured -> finalize immediately", StatusPending, 2, false, true, false, 0, nil, true, true},
		{"pending, configured approver who also happens to be super admin -> normal quorum path, not override", StatusPending, 2, true, true, false, 0, nil, true, false},
		{"already approved -> already approved", StatusApproved, 1, true, false, false, 0, ErrAlreadyApproved, false, false},
		{"already approved, super admin -> still already approved", StatusApproved, 1, false, true, false, 0, ErrAlreadyApproved, false, false},
		{"rejected -> not pending approval", StatusRejected, 1, true, false, false, 0, nil, false, false},
		{"none -> not pending approval", StatusNone, 1, false, false, false, 0, nil, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			finalize, err := approvalDecision(tc.status, tc.required, tc.callerIsApprover, tc.callerIsSuperAdmin, tc.callerAlreadyApproved, tc.approvalsSoFar)
			if tc.wantOK {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantFinalize, finalize)
				return
			}
			assert.False(t, finalize)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			// "rejected"/"none" status: expect a ClientError, not one of the
			// named sentinels -- the record must be edited (resubmitted)
			// before it can be approved again, not approved directly.
			assert.Error(t, err)
			assert.False(t, errors.Is(err, ErrNotApprover))
			assert.False(t, errors.Is(err, ErrAlreadyApproved))
			assert.False(t, errors.Is(err, ErrNoApproverConfigured))
			assert.False(t, errors.Is(err, ErrAlreadyApprovedByYou))
			var ce ClientError
			assert.True(t, errors.As(err, &ce))
			assert.Equal(t, "This record is not pending approval.", ce.Msg)
		})
	}
}

func TestCrmApprovalSentinelErrorsAreDistinct(t *testing.T) {
	errs := []error{
		ErrNotApprover, ErrAlreadyApproved, ErrNoApproverConfigured, ErrAlreadyApprovedByYou,
		ErrApprovalRequired, ErrLockedPendingApproval, ErrNotRejectable,
	}
	for i := range errs {
		for j := range errs {
			if i == j {
				continue
			}
			assert.NotEqual(t, errs[i].Error(), errs[j].Error(), "errs[%d] and errs[%d] should be distinct", i, j)
		}
	}
}
