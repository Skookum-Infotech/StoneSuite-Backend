package fabrication

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestPieceCompletionMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage PieceStage
		mode  DeliveryMode
		want  bool
	}{
		{"supply handed over", StageHandedOver, DeliverySupplyOnly, true},
		{"installed handed over", StageHandedOver, DeliveryInstalled, false},
		{"installed awaiting signoff", StageInstalled, DeliveryInstalled, false},
		{"installed signed off", StageSignedOff, DeliveryInstalled, true},
		{"unknown mode fails closed", StageSignedOff, "invalid", false},
		{"unknown stage fails closed", "invalid", DeliverySupplyOnly, false},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, PieceComplete(tc.stage, tc.mode)) })
	}
}

func TestSummarizePieces(t *testing.T) {
	for _, tc := range []struct {
		name                string
		pieces              []PieceProgress
		required, completed int
		done                bool
	}{
		{"empty", nil, 0, 0, false},
		{"partial", []PieceProgress{{Stage: StageHandedOver}, {Stage: StageCutting}}, 2, 1, false},
		{"replacement only counts once", []PieceProgress{{Stage: StageQC, Superseded: true}, {Stage: StageHandedOver}}, 1, 1, true},
		{"cancelled omitted", []PieceProgress{{Stage: StagePlanned, Cancelled: true}}, 0, 0, false},
		{"unknown blocks completion", []PieceProgress{{Stage: "invalid"}}, 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizePieces(tc.pieces, DeliverySupplyOnly)
			assert.Equal(t, tc.required, got.RequiredPieces)
			assert.Equal(t, tc.completed, got.CompletedPieces)
			assert.Equal(t, tc.done, got.Complete)
		})
	}
}
