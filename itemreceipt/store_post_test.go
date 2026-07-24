package itemreceipt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckTolerance(t *testing.T) {
	tests := []struct {
		name       string
		lines      []postLine
		canApprove bool
		reason     string
		wantErr    error
		wantClient bool
	}{
		{
			name:  "everything inside tolerance",
			lines: []postLine{{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 100}},
		},
		{
			name:  "at the ceiling exactly",
			lines: []postLine{{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 105}},
		},
		{
			name:    "over tolerance without the grant is refused",
			lines:   []postLine{{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 150}},
			wantErr: ErrOverReceipt,
		},
		{
			name:       "over tolerance with the grant but no reason is a client error",
			lines:      []postLine{{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 150}},
			canApprove: true,
			wantClient: true,
		},
		{
			name:       "over tolerance with the grant and a reason passes",
			lines:      []postLine{{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 150}},
			canApprove: true,
			reason:     "vendor shipped a full pallet; agreed to keep it",
		},
		{
			name:       "blank reason does not count",
			lines:      []postLine{{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 150}},
			canApprove: true,
			reason:     "   ",
			wantClient: true,
		},
		{
			name: "one bad line among several trips the gate",
			lines: []postLine{
				{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 100},
				{lineNumber: 2, ordered: 10, alreadyRecv: 0, accepted: 50},
			},
			wantErr: ErrOverReceipt,
		},
		{
			name: "cumulative across prior receipts",
			lines: []postLine{
				{lineNumber: 1, ordered: 100, alreadyRecv: 100, accepted: 20},
			},
			wantErr: ErrOverReceipt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkTolerance(tt.lines, tt.canApprove, tt.reason)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantClient:
				require.Error(t, err)
				assert.True(t, IsClientError(err), "expected a ClientError, got %v", err)
			default:
				require.NoError(t, err)
			}
		})
	}
}

// The refusal message must name the offending lines — a bare "over-receipt" is
// useless to a warehouse operator holding a pallet.
func TestCheckToleranceNamesOffendingLines(t *testing.T) {
	err := checkTolerance([]postLine{
		{lineNumber: 1, ordered: 100, alreadyRecv: 0, accepted: 100},
		{lineNumber: 7, ordered: 10, alreadyRecv: 0, accepted: 50},
	}, false, "")
	require.ErrorIs(t, err, ErrOverReceipt)
	assert.Contains(t, err.Error(), "line 7")
	assert.NotContains(t, err.Error(), "line 1")
}

func TestReceiptStatusFor(t *testing.T) {
	tests := []struct {
		name  string
		lines []postLine
		want  string
	}{
		{
			name:  "single line fully satisfied",
			lines: []postLine{{ordered: 100, alreadyRecv: 0, accepted: 100}},
			want:  receivedStatusCode,
		},
		{
			name:  "single line partially satisfied",
			lines: []postLine{{ordered: 100, alreadyRecv: 0, accepted: 40}},
			want:  partialStatusCode,
		},
		{
			name:  "final instalment completes the line",
			lines: []postLine{{ordered: 100, alreadyRecv: 60, accepted: 40}},
			want:  receivedStatusCode,
		},
		{
			name: "one line short keeps the whole receipt partial",
			lines: []postLine{
				{ordered: 100, alreadyRecv: 0, accepted: 100},
				{ordered: 50, alreadyRecv: 0, accepted: 10},
			},
			want: partialStatusCode,
		},
		{
			name:  "over-delivery still counts as fully received",
			lines: []postLine{{ordered: 100, alreadyRecv: 0, accepted: 105}},
			want:  receivedStatusCode,
		},
		{
			name:  "everything rejected settles nothing",
			lines: []postLine{{ordered: 100, alreadyRecv: 0, accepted: 0}},
			want:  partialStatusCode,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, receiptStatusFor(tt.lines))
		})
	}
}

// Whatever receiptStatusFor returns must be a legal move out of PEND, or
// posting would compute a status the transition map then refuses.
func TestReceiptStatusForAlwaysValidFromPending(t *testing.T) {
	cases := [][]postLine{
		{{ordered: 100, alreadyRecv: 0, accepted: 100}},
		{{ordered: 100, alreadyRecv: 0, accepted: 40}},
		{{ordered: 100, alreadyRecv: 0, accepted: 0}},
		{{ordered: 0, alreadyRecv: 0, accepted: 5}},
	}
	for _, lines := range cases {
		code := receiptStatusFor(lines)
		assert.NoError(t, ValidateTransition(pendingStatusCode, code),
			"receiptStatusFor produced %q, which PEND cannot reach", code)
	}
}
