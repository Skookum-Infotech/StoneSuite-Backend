package proposedaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Propose validates and previews a proposal, then stores it as pending.
// q may be a pgx.Tx or a pool. The returned id identifies the new row.
func (s *Store) Propose(ctx context.Context, q Querier, p Proposal) (string, error) {
	if !validKind(p.Kind) || !validSource(p.Source) {
		return "", &ValidationError{Message: "unknown action"}
	}
	ex, ok := s.reg.Lookup(p.Resource, p.Kind)
	if !ok {
		return "", &ValidationError{Message: "unknown action"}
	}
	if !json.Valid(p.Payload) {
		return "", &ValidationError{Message: "payload is not valid JSON"}
	}
	if err := ex.Validate(ctx, p.Payload); err != nil {
		return "", asValidation(err)
	}
	pv, err := ex.Preview(ctx, p.Payload)
	if err != nil {
		return "", asValidation(err)
	}
	if pv.Fields == nil {
		pv.Fields = []PreviewField{}
	}
	pvJSON, err := json.Marshal(pv)
	if err != nil {
		return "", fmt.Errorf("marshal preview: %w", err)
	}
	var id string
	err = q.QueryRow(ctx, `
		INSERT INTO ai_proposed_action
			(identity_id, user_id, kind, resource, payload, preview, source, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now() + make_interval(secs => $8::float8))
		RETURNING id`,
		p.Caller.IdentityID, p.Caller.UserID, string(p.Kind), p.Resource,
		[]byte(p.Payload), pvJSON, string(p.Source), s.ttl.Seconds()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert proposed action: %w", err)
	}
	return id, nil
}

// asValidation keeps an existing ValidationError or wraps err's message in one.
func asValidation(err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve
	}
	return &ValidationError{Message: err.Error()}
}
