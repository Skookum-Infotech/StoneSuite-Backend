package fabrication

// PieceStage is the physical production stage of a version-two piece.
type PieceStage string

// Version-two production stages are independent for each piece.
const (
	StagePlanned      PieceStage = "planned"
	StageReadyCutting PieceStage = "ready_cutting"
	StageCutting      PieceStage = "cutting"
	StageEdging       PieceStage = "edging"
	StageQC           PieceStage = "qc"
	StageQCPassed     PieceStage = "qc_passed"
	StageHandedOver   PieceStage = "handed_over"
	StageInstalled    PieceStage = "installed"
	StageSignedOff    PieceStage = "signed_off"
)

// DeliveryMode defines the completion obligation on a fabrication job.
type DeliveryMode string

// Supported delivery modes have different completion gates.
const (
	DeliverySupplyOnly DeliveryMode = "supply_only"
	DeliveryInstalled  DeliveryMode = "installed"
)

// PieceComplete reports whether a known stage satisfies the delivery mode.
func PieceComplete(stage PieceStage, mode DeliveryMode) bool {
	switch mode {
	case DeliverySupplyOnly:
		return stage == StageHandedOver || stage == StageInstalled || stage == StageSignedOff
	case DeliveryInstalled:
		return stage == StageSignedOff
	default:
		return false
	}
}

// PieceProgress is one active or historical piece's contribution to a job.
type PieceProgress struct {
	Stage      PieceStage
	Superseded bool
	Cancelled  bool
}

// ProgressSummary counts current delivery obligations without replacement inflation.
type ProgressSummary struct {
	RequiredPieces  int                `json:"requiredPieces"`
	CompletedPieces int                `json:"completedPieces"`
	StageCounts     map[PieceStage]int `json:"stageCounts"`
	Complete        bool               `json:"complete"`
}

// SummarizePieces excludes historical replacements and never completes an empty job.
func SummarizePieces(pieces []PieceProgress, mode DeliveryMode) ProgressSummary {
	out := ProgressSummary{StageCounts: make(map[PieceStage]int)}
	for _, p := range pieces {
		if p.Superseded || p.Cancelled {
			continue
		}
		out.RequiredPieces++
		out.StageCounts[p.Stage]++
		if PieceComplete(p.Stage, mode) {
			out.CompletedPieces++
		}
	}
	out.Complete = out.RequiredPieces > 0 && out.CompletedPieces == out.RequiredPieces
	return out
}
