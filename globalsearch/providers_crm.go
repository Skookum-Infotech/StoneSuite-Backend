package globalsearch

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

var _ = addProvider(Provider{Key: "customer", Resource: authz.ResourceCustomer, Domain: "crm", Module: "customer", Search: searchCRM("customer")})
var _ = addProvider(Provider{Key: "lead", Resource: authz.ResourceLead, Domain: "crm", Module: "lead", Search: searchCRM("lead")})
var _ = addProvider(Provider{Key: "prospect", Resource: authz.ResourceProspect, Domain: "crm", Module: "prospect", Search: searchCRM("prospect")})

// crm_activity and customer_note are customer children with no page of their own:
// hits route to the parent customer's detail page (id = customer uuid).
var _ = addProvider(Provider{Key: "crm_activity", Resource: authz.ResourceCRMActivity, Domain: "crm", Module: "customer", Search: searchCRMActivity})
var _ = addProvider(Provider{Key: "customer_note", Resource: authz.ResourceCustomerNote, Domain: "crm", Module: "customer", Search: searchCustomerNotes})

func searchCRMActivity(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	hits, hasMore, err := crmstore.SearchActivity(ctx, pool, string(scope), identityID, term, cap)
	if err != nil {
		return nil, false, err
	}
	return childHitsToResults("crm_activity", hits), hasMore, nil
}

func searchCustomerNotes(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	hits, hasMore, err := crmstore.SearchCustomerNotes(ctx, pool, string(scope), identityID, term, cap)
	if err != nil {
		return nil, false, err
	}
	return childHitsToResults("customer_note", hits), hasMore, nil
}

// childHitsToResults maps a customer-child hit to a Result routed to the parent
// customer. DisplayName is the text snippet (trimmed); Subtitle is the customer.
func childHitsToResults(typ string, hits []crmstore.ChildHit) []Result {
	out := make([]Result, len(hits))
	for i, h := range hits {
		snippet := strings.TrimSpace(h.Snippet)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		subtitle := h.CustomerName
		if h.Kind != "" {
			subtitle = h.CustomerName + " · " + h.Kind
		}
		out[i] = Result{Type: typ, ID: h.CustomerUUID, DisplayName: snippet, Subtitle: subtitle, UpdatedAt: h.UpdatedAt}
	}
	return out
}

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
