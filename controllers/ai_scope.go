package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/authz"
)

// aiScopeResources are the CRM resources rag_chunks covers (see
// crmstore.CRMWorkflowKeys and controllers/crm.go storeFromContext). Each
// resource name is also its rag_chunks.record_type value.
var aiScopeResources = []authz.Resource{authz.ResourceLead, authz.ResourceProspect, authz.ResourceCustomer}

// resolveAIGrants checks read access on every aiScopeResources type and
// returns the caller's scope per granted type. Types the caller holds no grant
// on are absent — retrieval and counts then cannot reach them at all.
//
// This replaced a single "narrowest scope" across all types, which skipped
// ungranted types when picking the scope and then applied that scope to every
// row: a lead:all grant with no customer grant produced scope "all" and
// retrieved customer records.
func resolveAIGrants(ctx context.Context, pool *pgxpool.Pool, identityID string) (ai.Grants, error) {
	grants := ai.Grants{}
	for _, res := range aiScopeResources {
		d, err := authz.Check(ctx, pool, identityID, res, authz.ActionRead)
		if err != nil {
			return nil, fmt.Errorf("check %s read: %w", res, err)
		}
		if d.Allowed {
			grants[string(res)] = string(d.Scope)
		}
	}
	return grants, nil
}

// splitGranted partitions keys into the ones grants can read and the ones it
// can't, preserving order.
func splitGranted(grants ai.Grants, keys []string) (granted, denied []string) {
	readable := map[string]bool{}
	for _, t := range grants.Types() {
		readable[t] = true
	}
	for _, k := range keys {
		if readable[k] {
			granted = append(granted, k)
		} else {
			denied = append(denied, k)
		}
	}
	return granted, denied
}

// noAccessSentence says which record types a count left out, e.g. "You don't
// have access to customer records." It names types, never numbers — the whole
// point is that the caller learns nothing about data they can't read.
func noAccessSentence(denied []string) string {
	if len(denied) == 0 {
		return ""
	}
	return fmt.Sprintf("You don't have access to %s records.", joinOr(denied))
}

// joinOr renders ["a"] as "a", ["a","b"] as "a or b", and ["a","b","c"] as
// "a, b, or c".
func joinOr(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + " or " + words[1]
	default:
		return strings.Join(words[:len(words)-1], ", ") + ", or " + words[len(words)-1]
	}
}
