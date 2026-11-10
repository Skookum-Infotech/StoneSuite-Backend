package controllers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/doclinks"
	"stonesuite-backend/workflow"
)

// flowResources maps each flow stage to the RBAC resource that guards its records.
var flowResources = map[doclinks.Kind]authz.Resource{
	doclinks.KindQuote:       authz.ResourceQuote,
	doclinks.KindSalesOrder:  authz.ResourceSalesOrder,
	doclinks.KindFabrication: authz.ResourceInstallation,
	doclinks.KindInvoice:     authz.ResourceInvoice,
	doclinks.KindPayment:     authz.ResourcePayment,
	doclinks.KindRefund:      authz.ResourceRefund,
}

// workflowVisibility turns the caller's read grants into per-stage access, so
// the chain never reveals a record the caller could not open on its own.
func workflowVisibility(ctx context.Context, pool *pgxpool.Pool, identityID string) (doclinks.Visibility, error) {
	vis := make(doclinks.Visibility, len(flowResources))
	for kind, res := range flowResources {
		d, err := authz.Check(ctx, pool, identityID, res, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		vis[kind] = doclinks.Access{Allowed: d.Allowed, OwnOnly: d.Allowed && d.Scope != authz.ScopeAll}
	}
	return vis, nil
}

// workflowBlockFor builds the read-only `workflow` object for a detail response.
// A failure here must not break the page, so it is logged and the block omitted
// (the frontend then falls back to the links it can resolve itself).
func workflowBlockFor(r *http.Request, pool *pgxpool.Pool, identityID string, kind doclinks.Kind, uuid string) *doclinks.Block {
	ctx := r.Context()
	vis, err := workflowVisibility(ctx, pool, identityID)
	if err != nil {
		slog.WarnContext(ctx, "workflow block: permission check failed", "kind", kind, "record", uuid, "error", err)
		return nil
	}
	userID, err := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err != nil {
		userID = "" // no tenant user: own-only stages show nothing, all-scope stages are unaffected
	}
	block, err := doclinks.Resolve(ctx, pool, kind, uuid, userID, vis)
	if err != nil {
		slog.WarnContext(ctx, "workflow block: resolve failed", "kind", kind, "record", uuid, "error", err)
		return nil
	}
	return &block
}
