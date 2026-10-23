// Package domainevent is a transactional outbox: modules Emit events inside
// their own transaction, and per-consumer cursors drain them in id order with
// bounded retries and dead-lettering.
package domainevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"stonesuite-backend/provenance"
)

const (
	// MaxDepth is the deepest follow-on chain an event may carry.
	MaxDepth int16 = 3
	// MaxAttempts is how many times a handler may fail on one event before it is skipped.
	MaxAttempts = 5
	// MaxEventsPerTick caps events drained per tenant per DrainAll call.
	MaxEventsPerTick = 500
	// DefaultBatch is the per-consumer batch size used by DrainAll.
	DefaultBatch = 50

	emptyPayload = "{}"
	insertEvent  = `INSERT INTO domain_event (event_type, resource, record_id, actor_user_id, via, depth, payload)
VALUES ($1, $2, $3, $4::uuid, $5, $6, $7::jsonb)`
)

// ErrDepthExceeded is returned by Emit when an event's depth is outside [0, MaxDepth].
var ErrDepthExceeded = errors.New("domainevent: depth exceeded")

// Querier is satisfied by a pgx.Tx or a pool.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Event is one domain event to publish.
type Event struct {
	Type        string
	Resource    string
	RecordID    string
	ActorUserID string // "" is stored as NULL
	Via         provenance.Via
	Depth       int16
	// Payload is JSON-marshalled; callers pass typed structs carrying ids and
	// changed-field keys only, never values such as PII or secrets.
	Payload any
}

// Stored is an Event as read back from the outbox.
type Stored struct {
	ID int64
	Event
	CreatedAt time.Time
	// Payload shadows Event.Payload with the raw stored JSON.
	Payload json.RawMessage
}

// ChildDepth returns the depth a consumer must use when emitting a follow-on event.
func ChildDepth(parent Stored) int16 { return parent.Depth + 1 }

// Emit inserts ev through q. Pass the caller's TRANSACTION, never the pool, from
// module stores so the event commits or rolls back atomically with the change.
func Emit(ctx context.Context, q Querier, ev Event) error {
	if ev.Type == "" || ev.Resource == "" || ev.RecordID == "" {
		return errors.New("domainevent: type, resource and record id are required")
	}
	if ev.Depth < 0 || ev.Depth > MaxDepth {
		return fmt.Errorf("emit %s: depth %d: %w", ev.Type, ev.Depth, ErrDepthExceeded)
	}
	via := ev.Via
	if via == "" {
		via = provenance.From(ctx)
	}
	payload := emptyPayload
	if ev.Payload != nil {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return fmt.Errorf("emit %s: marshal payload: %w", ev.Type, err)
		}
		payload = string(b)
	}
	var actor *string
	if ev.ActorUserID != "" {
		actor = &ev.ActorUserID
	}
	if _, err := q.Exec(ctx, insertEvent, ev.Type, ev.Resource, ev.RecordID, actor, string(via), ev.Depth, payload); err != nil {
		return fmt.Errorf("emit %s: insert: %w", ev.Type, err)
	}
	return nil
}
