package proposedaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
)

// noopRecordID is the placeholder record id NoopExecutor returns.
const noopRecordID = "noop"

// NoopExecutor is a test / dark-ship executor that writes nothing. It is NOT
// registered anywhere in main.go. Its payload is a JSON object with an
// optional string "name".
type NoopExecutor struct{ Res authz.Resource }

type noopPayload struct {
	Name string `json:"name"`
}

func decodeNoop(payload json.RawMessage) (noopPayload, error) {
	var p noopPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return noopPayload{}, fmt.Errorf("payload must be a JSON object with an optional name: %w", err)
	}
	return p, nil
}

// Resource returns the configured resource.
func (n NoopExecutor) Resource() authz.Resource { return n.Res }

// Validate requires a JSON object payload.
func (n NoopExecutor) Validate(_ context.Context, payload json.RawMessage) error {
	_, err := decodeNoop(payload)
	return err
}

// Preview summarizes the optional name.
func (n NoopExecutor) Preview(_ context.Context, payload json.RawMessage) (Preview, error) {
	p, err := decodeNoop(payload)
	if err != nil {
		return Preview{}, err
	}
	return Preview{Title: "No-op", Summary: p.Name, Fields: []PreviewField{{Label: "Name", Value: p.Name}}}, nil
}

// InScope always allows.
func (n NoopExecutor) InScope(context.Context, Querier, authz.Scope, Caller, Kind, json.RawMessage) (bool, error) {
	return true, nil
}

// Execute inserts nothing and returns a placeholder reference.
func (n NoopExecutor) Execute(_ context.Context, _ pgx.Tx, _ Caller, _ json.RawMessage) (ResultRef, error) {
	return ResultRef{Resource: string(n.Res), RecordID: noopRecordID}, nil
}
