package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

var _ = addProvider(Provider{Key: "customer", Resource: authz.ResourceCustomer, Search: searchCRM("customer")})
var _ = addProvider(Provider{Key: "lead", Resource: authz.ResourceLead, Search: searchCRM("lead")})
var _ = addProvider(Provider{Key: "prospect", Resource: authz.ResourceProspect, Search: searchCRM("prospect")})

// searchCRM returns a SearchFunc bound to one of the three CRM workflow keys
// that all share the customer table (crmstore's relational store).
func searchCRM(key string) SearchFunc {
	return func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
		t, err := tenancy.TenantFromContext(ctx)
		if err != nil {
			return nil, false, err
		}
		st := crmstore.For(t.DesignVersion)
		page, err := st.SearchRecords(ctx, pool, key, string(scope), identityID, query.Request{Search: term, Limit: cap})
		if err != nil {
			return nil, false, err
		}
		out := make([]Result, len(page.Records))
		for i, rec := range page.Records {
			name, _ := rec.CoreFields["customer_name"].(string)
			out[i] = Result{
				Type:        key,
				ID:          rec.ID,
				Number:      rec.RecordNumber,
				DisplayName: name,
				UpdatedAt:   rec.UpdatedAt,
			}
		}
		return out, page.HasMore, nil
	}
}
