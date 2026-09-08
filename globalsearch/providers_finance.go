package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/chartofaccounts"
	"stonesuite-backend/fabrication"
	"stonesuite-backend/query"
)

// chartofaccounts has no owner column or scope narrowing — RBAC is
// resource-level only, so this adapter ignores scope/identityID and passes
// a zero-value Filters{} (no extra narrowing).
var _ = addProvider(Provider{Key: "chart_of_account", Resource: authz.ResourceChartOfAccount, Domain: "finance", Module: "chart-of-accounts", Search: searchChartOfAccounts})

func searchChartOfAccounts(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	page, err := chartofaccounts.Search(ctx, pool, query.Request{Search: term, Limit: cap}, chartofaccounts.Filters{})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, a := range page.Records {
		out[i] = Result{Type: "chart_of_account", ID: a.ID, Number: a.Code, DisplayName: a.Name, UpdatedAt: a.UpdatedAt}
	}
	return out, page.HasMore, nil
}

// The frontend surfaces this module as "Journal Entries" (finance/journal-entries)
// -- the backend still calls it cash_transfer. There is no separate journal-entry
// entity: journal/ is the internal GL posting engine, not a browsable record.
var _ = addProvider(Provider{Key: "cash_transfer", Resource: authz.ResourceCashTransfer, Domain: "finance", Module: "journal-entries", Search: searchCashTransfers})

func searchCashTransfers(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := cashtransfer.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, ct := range page.Records {
		out[i] = Result{Type: "cash_transfer", ID: ct.ID, Number: ct.Number, DisplayName: "Transfer " + ct.Number, Subtitle: ct.FromAccount.Name + " -> " + ct.ToAccount.Name, UpdatedAt: ct.UpdatedAt}
	}
	return out, page.HasMore, nil
}

// fabrication (job) is guarded by authz.ResourceInstallation, not a
// "fabrication" resource -- see controllers/fabrication.go -- and the frontend
// routes it under sales/installation, so Module diverges from Key here.
var _ = addProvider(Provider{Key: "fabrication_job", Resource: authz.ResourceInstallation, Domain: "sales", Module: "installation", Search: searchFabricationJobs})

func searchFabricationJobs(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := fabrication.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, j := range page.Records {
		out[i] = Result{Type: "fabrication_job", ID: j.ID, Number: j.Number, DisplayName: "Job " + j.Number, Subtitle: j.Customer.Name, UpdatedAt: j.UpdatedAt}
	}
	return out, page.HasMore, nil
}
