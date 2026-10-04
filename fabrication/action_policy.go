package fabrication

// PieceActionState is a server-loaded snapshot, not a client-supplied policy.
type PieceActionState struct {
	Stage         PieceStage
	Mode          DeliveryMode
	Held          bool
	RemakePending bool
}

// PieceActions lists operations for an authorized caller. Command handlers still
// revalidate their operation-specific prerequisites inside the transaction.
func PieceActions(state PieceActionState, permitted bool) []AvailableAction {
	out := []AvailableAction{}
	if !permitted {
		return out
	}
	var code, label string
	switch state.Stage {
	case StageEdging:
		code, label = "complete_edging", "Complete edging"
	case StageQC:
		code, label = "record_qc", "Record QC result"
	case StageQCPassed:
		code, label = "create_handover", "Hand over pieces"
	case StageHandedOver:
		if state.Mode == DeliveryInstalled {
			code, label = "record_installation", "Record installation"
		}
	case StageInstalled:
		if state.Mode == DeliveryInstalled {
			code, label = "record_signoff", "Record customer sign-off"
		}
	}
	if code == "" {
		return out
	}
	blockers := []ActionBlocker{}
	if state.Held {
		blockers = append(blockers, ActionBlocker{Code: "job_held", Message: "Resume this job before continuing production."})
	}
	if state.RemakePending {
		blockers = append(blockers, ActionBlocker{Code: "remake_pending", Message: "Resolve the remake request for this piece first."})
	}
	return append(out, AvailableAction{Code: code, Label: label, InputType: code, Enabled: len(blockers) == 0, Blockers: blockers})
}
