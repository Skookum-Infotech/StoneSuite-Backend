package proposedentity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/workflow"
)

// Audit labels. Table names match the ones the HTTP create paths use.
const (
	auditActionCreate  = "create"
	auditTableCustomer = "customer"
	auditTableVendor   = "vendor"
	auditTableItem     = "inventory_item"
	auditMetaSourceRef = "source_ref"
)

// Register adds the customer, vendor and inventory-item create executors.
//
// None of the executors fires post-commit side effects (customer welcome
// email, item-created notification, immediate RAG enqueue): proposedaction
// has no post-commit hook, and the confirm transaction must stay atomic. The
// audit observer and the RAG reconcile sweep cover indexing.
func Register(reg *proposedaction.Registry) error {
	for _, e := range []proposedaction.Executor{customerExecutor{}, vendorExecutor{}, itemExecutor{}} {
		if err := reg.Register(proposedaction.KindCreate, e); err != nil {
			return fmt.Errorf("register proposed entity executors: %w", err)
		}
	}
	return nil
}

// inScopeAlways is the shared InScope: creates have no row-level scope, and
// the store has already run authz.Check(create) for the confirming caller.
func inScopeAlways() (bool, error) { return true, nil }

// actorEmployeeID resolves the caller's employee id; 0 when there is none.
func actorEmployeeID(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	if userID == "" {
		return 0, nil
	}
	var id int
	err := tx.QueryRow(ctx, `
		SELECT employee_id FROM employee
		WHERE employee_user_id = $1 AND employee_deleted_at IS NULL`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve actor employee: %w", err)
	}
	return id, nil
}

// auditCreate writes the create audit row on tx; provenance (via) comes from ctx.
func auditCreate(ctx context.Context, tx pgx.Tx, caller proposedaction.Caller, resource authz.Resource, table, id, sourceRef string, snapshot map[string]any) error {
	err := workflow.LogAuditFull(ctx, tx, caller.UserID, auditActionCreate, string(resource), id, table,
		nil, snapshot, map[string]any{auditMetaSourceRef: sourceRef}, "", "", "")
	if err != nil {
		return fmt.Errorf("audit proposed %s create: %w", resource, err)
	}
	return nil
}
