// Package proposedaction stores AI/docextract-proposed record changes that a
// human must confirm before anything is written. A proposal is validated and
// previewed when created, and re-authorized under the confirming caller's
// CURRENT roles at confirm time, so a stale proposal can never widen access.
package proposedaction

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"stonesuite-backend/authz"
)

// Kind is the type of change a proposal makes.
type Kind string

// Proposal kinds.
const (
	KindCreate Kind = "create"
	KindUpdate Kind = "update"
)

// Source names who produced the proposal.
type Source string

// Proposal sources.
const (
	SourceAgent      Source = "agent"
	SourceDocExtract Source = "docextract"
)

// Status is the lifecycle state of a proposal.
type Status string

// Proposal statuses.
const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
	StatusExpired   Status = "expired"
	StatusFailed    Status = "failed"
)

// Page-size convention, mirroring the query package (which must stay app-free).
const (
	defaultListLimit = 25
	maxListLimit     = 100
)

// Sanitized messages persisted on failed rows; raw errors are never stored.
const (
	msgNoLongerValid   = "The proposed change is no longer valid."
	msgNotCompleted    = "The action could not be completed."
	eventConfirmedType = "proposed_action.confirmed"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Querier is the subset of pgx used by the store; a pool or a pgx.Tx satisfies it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Caller identifies who is acting; both ids come from the JWT, never a body.
type Caller struct {
	IdentityID string
	UserID     string
}

// PreviewField is one labelled line of a preview.
type PreviewField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Preview is the human-readable summary shown before confirmation.
type Preview struct {
	Title   string         `json:"title"`
	Summary string         `json:"summary"`
	Fields  []PreviewField `json:"fields"`
}

// ResultRef points at the record a confirmed action created or changed.
type ResultRef struct {
	Resource string `json:"resource"`
	RecordID string `json:"recordId"`
	Label    string `json:"label"`
}

// Action is the API view of a proposal. The raw payload is never serialized.
type Action struct {
	ID           string          `json:"id"`
	IdentityID   string          `json:"-"`
	UserID       string          `json:"-"`
	Kind         Kind            `json:"kind"`
	Resource     string          `json:"resource"`
	Payload      json.RawMessage `json:"-"`
	Preview      Preview         `json:"preview"`
	Source       Source          `json:"source"`
	Status       Status          `json:"status"`
	ResultRef    *ResultRef      `json:"resultRef,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	ExpiresAt    time.Time       `json:"expiresAt"`
	CreatedAt    time.Time       `json:"createdAt"`
	ResolvedAt   *time.Time      `json:"resolvedAt,omitempty"`
}

// Proposal is the input to Store.Propose.
type Proposal struct {
	Caller   Caller
	Kind     Kind
	Resource string
	Source   Source
	Payload  json.RawMessage
}

// actionFor maps a proposal kind to the permission needed to confirm it.
func actionFor(k Kind) authz.Action {
	if k == KindCreate {
		return authz.ActionCreate
	}
	return authz.ActionUpdate
}

func validKind(k Kind) bool { return k == KindCreate || k == KindUpdate }

func validSource(s Source) bool { return s == SourceAgent || s == SourceDocExtract }
