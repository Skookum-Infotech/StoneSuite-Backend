package journal

import "testing"

func TestValidateLines(t *testing.T) {
	tests := []struct {
		name    string
		lines   []LineInput
		wantErr error
	}{
		{
			name: "balanced two lines ok",
			lines: []LineInput{
				{AccountID: 1, Debit: 100},
				{AccountID: 2, Credit: 100},
			},
			wantErr: nil,
		},
		{
			name:    "single line rejected",
			lines:   []LineInput{{AccountID: 1, Debit: 100}},
			wantErr: ErrInvalidLine,
		},
		{
			name: "both debit and credit on one line rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 100, Credit: 50},
				{AccountID: 2, Credit: 50},
			},
			wantErr: ErrInvalidLine,
		},
		{
			name: "zero line rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 0, Credit: 0},
				{AccountID: 2, Credit: 100},
			},
			wantErr: ErrInvalidLine,
		},
		{
			name: "unbalanced rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 100},
				{AccountID: 2, Credit: 99},
			},
			wantErr: ErrUnbalancedEntry,
		},
		{
			// 0.1 + 0.1 + 0.1 == 0.30000000000000004 in IEEE-754 float64, not
			// 0.3 exactly. Without round2's cent-level rounding before the
			// debit/credit comparison, this legitimately balanced entry would
			// be rejected as unbalanced by a raw float64 != comparison.
			name: "balanced despite float64 summation drift is accepted",
			lines: []LineInput{
				{AccountID: 1, Debit: 0.1},
				{AccountID: 2, Debit: 0.1},
				{AccountID: 3, Debit: 0.1},
				{AccountID: 4, Credit: 0.3},
			},
			wantErr: nil,
		},
		{
			// A genuine one-cent discrepancy must still be caught even though
			// round2 tolerates sub-cent float noise.
			name: "one cent short is still rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 10.00},
				{AccountID: 2, Credit: 9.99},
			},
			wantErr: ErrUnbalancedEntry,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLines(tt.lines)
			if tt.wantErr == nil && err != nil {
				t.Errorf("validateLines() = %v, want nil", err)
			}
			if tt.wantErr != nil && err != tt.wantErr {
				t.Errorf("validateLines() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
