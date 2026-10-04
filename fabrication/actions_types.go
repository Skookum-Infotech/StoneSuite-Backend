package fabrication

// Workflow versions distinguish historical whole-job processing from piece actions.
const (
	WorkflowLegacy       = 1
	WorkflowPieceActions = 2
)

// CommandMeta identifies a replay-safe mutation against a known record version.
type CommandMeta struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	RequestID       string `json:"requestId"`
}

// ActionBlocker describes an operational prerequisite visible to an authorized user.
type ActionBlocker struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	TargetID string `json:"targetId,omitempty"`
}

// AvailableAction describes a server-authorized action and its required form.
type AvailableAction struct {
	Code      string          `json:"code"`
	Label     string          `json:"label"`
	Enabled   bool            `json:"enabled"`
	InputType string          `json:"inputType"`
	Blockers  []ActionBlocker `json:"blockers"`
}
