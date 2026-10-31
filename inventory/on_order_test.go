package inventory

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlabSerialPrefix(t *testing.T) {
	tests := []struct {
		name    string
		po      string
		want    string
		wantErr string // substring; "" = no error
	}{
		{name: "a PO number", po: "PORD-000012", want: "PORD-000012-"},
		{name: "surrounding space is trimmed", po: "  PORD-7 ", want: "PORD-7-"},
		{name: "blank", po: "   ", wantErr: "no number yet"},
		{name: "empty", po: "", wantErr: "no number yet"},
		{name: "no room left for the running number", po: strings.Repeat("P", MaxSlabSerialLen), wantErr: "too long"},
		{name: "exactly room for it", po: strings.Repeat("P", MaxSlabSerialLen-SlabSerialSuffixDigits-1),
			want: strings.Repeat("P", MaxSlabSerialLen-SlabSerialSuffixDigits-1) + "-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SlabSerialPrefix(tc.po)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "a bad PO number is a 400")
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSlabSerial(t *testing.T) {
	tests := []struct {
		prefix string
		n      int
		want   string
	}{
		{"PORD-000012-", 1, "PORD-000012-001"},
		{"PORD-000012-", 42, "PORD-000012-042"},
		{"PORD-000012-", 999, "PORD-000012-999"},
		{"PORD-000012-", 1000, "PORD-000012-1000"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, SlabSerial(tc.prefix, tc.n))
	}
	assert.LessOrEqual(t, len(SlabSerial(strings.Repeat("P", MaxSlabSerialLen-SlabSerialSuffixDigits-1)+"-", 1)), MaxSlabSerialLen)
}
