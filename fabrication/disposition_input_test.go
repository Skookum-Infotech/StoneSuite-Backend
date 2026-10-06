package fabrication

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A recovered offcut becomes real stock, so it must carry real dimensions and a
// destination bin -- never a placeholder. These all fail before the (nil) pool
// is touched.
func TestRecordDisposition_RecoveredNeedsDimensionsAndBin(t *testing.T) {
	full := DispositionInput{
		Disposition: "recovered", RecoveredArea: 1,
		LengthMM: 1000, WidthMM: 500, ThicknessMM: 20, DestinationBinUUID: "bin-1",
	}
	tests := []struct {
		name    string
		mutate  func(*DispositionInput)
		wantMsg string
	}{
		{"missing length", func(in *DispositionInput) { in.LengthMM = 0 }, "length, width and thickness"},
		{"missing width", func(in *DispositionInput) { in.WidthMM = 0 }, "length, width and thickness"},
		{"missing thickness", func(in *DispositionInput) { in.ThicknessMM = 0 }, "length, width and thickness"},
		{"negative dimension", func(in *DispositionInput) { in.LengthMM = -5 }, "length, width and thickness"},
		{"missing bin", func(in *DispositionInput) { in.DestinationBinUUID = "  " }, "destination bin"},
		{"unknown disposition", func(in *DispositionInput) { in.Disposition = "lost" }, "recovered, scrapped, or delivered"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := full
			tc.mutate(&in)
			err := RecordDisposition(context.Background(), nil, "job-1", in, 1)
			var ce ClientError
			if assert.True(t, errors.As(err, &ce), "want ClientError, got %T: %v", err, err) {
				assert.Contains(t, ce.Msg, tc.wantMsg)
			}
		})
	}
}
