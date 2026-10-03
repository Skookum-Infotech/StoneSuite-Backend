package approvalchain

import "errors"

// ErrSequenceDecision rejects an unconfigured, out-of-order or repeated decision.
var ErrSequenceDecision = errors.New("only the next configured approver may decide this request")

// SequenceStep is an immutable request's configured approver and current decision.
type SequenceStep struct {
	EmployeeID int  `json:"employeeId"`
	Approved   bool `json:"approved"`
}

// NextSequenceDecision returns the next step index and whether approving it
// completes the configured sequence. The caller persists under its subject lock.
func NextSequenceDecision(steps []SequenceStep, actor int) (int, bool, error) {
	for i, step := range steps {
		if step.Approved {
			continue
		}
		if actor <= 0 || step.EmployeeID != actor {
			return 0, false, ErrSequenceDecision
		}
		for _, remaining := range steps[i+1:] {
			if !remaining.Approved {
				return i, false, nil
			}
		}
		return i, true, nil
	}
	return 0, false, ErrSequenceDecision
}
