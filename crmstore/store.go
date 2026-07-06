// Package crmstore abstracts CRM persistence behind a single interface so a
// tenant can run either database design and switch between them at runtime:
//
//   - DesignV1: the JSONB workflow_records engine (workflow.Engine).
//   - DesignV2: the relational lkp_* + crm_record design.
//
// Both implementations return the same DTOs (workflow.Record / workflow.StatusInfo)
// so the HTTP API — and therefore the frontend — is identical across designs.
// Controllers select the implementation with For(tenant.DesignVersion).
package crmstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// Sentinel errors.
var (
	// ErrNotSupported is returned by a design that does not implement an op
	// (e.g. Approve on DesignV1).
	ErrNotSupported = errors.New("operation not supported for this database design")
	// ErrRecordNotFound is returned when a record id matches nothing.
	ErrRecordNotFound = errors.New("record not found")
	// ErrNotApprover is returned when the caller is not a configured approver.
	ErrNotApprover = errors.New("you are not authorized to approve this document. Only the assigned approver(s) can approve it.")
	// ErrAlreadyApproved is returned when a record has already been approved.
	ErrAlreadyApproved = errors.New("this document has already been approved.")
	// ErrNoApproverConfigured is returned when a record is pending approval but
	// no active approver is configured for its record type.
	ErrNoApproverConfigured = errors.New("no approver is configured for this workflow. Please contact your administrator.")
)

// ClientError marks a caller-fault error (maps to HTTP 400). Server faults are
// returned as ordinary wrapped errors.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err should surface as a 400 to the client.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce) || errors.Is(err, ErrNotApprover)
}

// CreateInput carries the fields needed to create a CRM record. Owner/team and
// the chosen initial status are optional; stores fall back to sensible defaults.
type CreateInput struct {
	ActorIdentityID string         // control-plane identity of the caller
	OwnerUserID     string         // tenant user UUID (optional; defaults to caller)
	TeamID          string         // optional
	CrmStatusID     string         // optional chosen status at creation (v2)
	CoreFields      map[string]any // typed/built-in fields
	CustomFields    map[string]any // admin-defined dynamic fields (<=15)
}

// Store is the design-agnostic CRM persistence contract. Every method takes the
// resolved tenant pool. scope is the RBAC scope ("all" | "team" | "own").
type Store interface {
	// AllStatuses returns every CRM status across lead+prospect+customer
	// (for a combined filter dropdown).
	AllStatuses(ctx context.Context, pool *pgxpool.Pool) ([]workflow.StatusInfo, error)
	// Statuses returns the statuses shown on the CREATE form for key — the
	// key's own-stage statuses (e.g. lead -> Lead Qualified/Unqualified).
	Statuses(ctx context.Context, pool *pgxpool.Pool, key string) ([]workflow.StatusInfo, error)
	// KeyForRecord returns the CRM workflow key (lead|prospect|customer) of a
	// record, for RBAC resolution by record id.
	KeyForRecord(ctx context.Context, pool *pgxpool.Pool, id string) (string, error)
	// ListRecords lists records for key, filtered by RBAC scope.
	ListRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string) ([]workflow.Record, error)
	// SearchRecords lists records for key with server-side filtering, sorting,
	// and keyset pagination, all composed onto the caller's RBAC scope (a filter
	// can only narrow the scoped set, never widen it). Returns one page + cursor.
	SearchRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string, req query.Request) (workflow.Page, error)
	// CreateRecord creates a record in key's stage.
	CreateRecord(ctx context.Context, pool *pgxpool.Pool, key string, in CreateInput) (*workflow.Record, error)
	// GetRecord loads a single record by its external id.
	GetRecord(ctx context.Context, pool *pgxpool.Pool, id string) (*workflow.Record, error)
	// UpdateRecord merges core/custom fields onto an existing record.
	UpdateRecord(ctx context.Context, pool *pgxpool.Pool, id string, core, custom map[string]any) error
	// DeleteRecord removes a record.
	DeleteRecord(ctx context.Context, pool *pgxpool.Pool, id string) error
	// AvailableTransitions returns the statuses shown on the EDIT form — the
	// forward-stage statuses a record may advance to (lead -> prospect/customer,
	// prospect -> customer, customer -> own statuses).
	AvailableTransitions(ctx context.Context, pool *pgxpool.Pool, id string) ([]workflow.StatusInfo, error)
	// TransitionRecord moves a record to toStatusID, advancing its stage if the
	// chosen status belongs to a later type. Forward-only.
	TransitionRecord(ctx context.Context, pool *pgxpool.Pool, id, toStatusID, actorIdentityID string) (*workflow.Record, error)
	// ConvertRecord creates a new record in targetKey's stage linked to the
	// source via parent lineage (lead -> prospect -> customer).
	ConvertRecord(ctx context.Context, pool *pgxpool.Pool, id, targetKey string, core, custom map[string]any, actorIdentityID string) (newRec *workflow.Record, sourceID string, err error)
	// Approve approves a Closed-Won customer if the caller is a configured
	// approver. DesignV1 returns ErrNotSupported.
	Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error)
	// IsApprover reports whether identityID is a configured approver for record
	// id. Read-only — used to expose a canApprove flag on record reads without
	// mutating anything. DesignV1 always returns false, nil (unsupported).
	IsApprover(ctx context.Context, pool *pgxpool.Pool, id, identityID string) (bool, error)
}
