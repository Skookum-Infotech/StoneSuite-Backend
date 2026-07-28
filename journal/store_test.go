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
