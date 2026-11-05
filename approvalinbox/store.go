package approvalinbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

const hoursPerDay = 24

// Caller is the authenticated user asking. EmployeeID is 0 when the identity has no
// employee row (then the "mine" queue is empty, never an error).
type Caller struct {
	IdentityID   string
	EmployeeID   int
	IsSuperAdmin bool
}

// CheckFunc answers one RBAC question for the caller. Injected so callers can test
// module exclusion without a database.
type CheckFunc func(r authz.Resource, a authz.Action) authz.Decision

// GrantsCheck loads the caller's effective grants once and answers every per-module
// check from them (15 modules x 2 actions would otherwise be 30 grant lookups).
func GrantsCheck(ctx context.Context, q authz.Querier, identityID string) (CheckFunc, error) {
	grants, err := authz.EffectiveGrants(ctx, q, identityID)
	if err != nil {
		return nil, fmt.Errorf("load grants: %w", err)
	}
	return func(r authz.Resource, a authz.Action) authz.Decision {
		return authz.DecideAny(grants, []authz.Resource{r}, a)
	}, nil
}

// Permitted returns the modules the caller may see, in registry-key order. A module is
// dropped (never an error) when read is denied, or when read is scoped to "own" and the
// module has no owner column to scope by (fail closed). Any scope other than "all" is
// treated as "own".
func Permitted(check CheckFunc) []Source {
	var out []Source
	for _, key := range Keys() {
		meta, ok := metas[key]
		cfg, regOK := approvalchain.ForWorkflowKey(key)
		if !ok || !regOK {
			continue
		}
		read := check(meta.Resource, authz.ActionRead)
		if !read.Allowed {
			continue
		}
		own := read.Scope != authz.ScopeAll
		if own && cfg.Record.OwnerColumn == "" {
			continue
		}
		out = append(out, Source{
			Key: key, OwnOnly: own,
			CanTransition: check(meta.Resource, authz.ActionTransition).Allowed,
		})
	}
	return out
}

// ModuleCount is one permitted module's pending count.
type ModuleCount struct {
	WorkflowKey string `json:"workflowKey"`
	Label       string `json:"label"`
	Count       int    `json:"count"`
}

// Item is one record waiting for sign-off.
type Item struct {
	WorkflowKey       string    `json:"workflowKey"`
	Label             string    `json:"label"`
	Domain            string    `json:"domain"`
	RouteModule       string    `json:"routeModule"`
	RecordUUID        string    `json:"recordUuid"`
	Number            string    `json:"number"`
	StatusCode        string    `json:"statusCode"`
	StatusName        string    `json:"statusName"`
	EnteredAt         time.Time `json:"enteredAt"`
	AgeDays           int       `json:"ageDays"`
	OwnerName         string    `json:"ownerName"`
	PartyName         string    `json:"partyName,omitempty"`
	Amount            string    `json:"amount,omitempty"`
	RequiredApprovals int       `json:"requiredApprovals"`
	ApprovedCount     int       `json:"approvedCount"`
	CanApprove        bool      `json:"canApprove"`
	CanReject         bool      `json:"canReject"`
}

// Params selects one page.
type Params struct {
	Scope  Scope
	Module string // optional registry key
	Limit  int
	Cursor *Cursor
}

// Page is one inbox page plus the module breakdown.
type Page struct {
	Total      int
	Modules    []ModuleCount
	Items      []Item
	NextCursor string
	HasMore    bool
}

// Counts is the per-module breakdown the page and the dashboard tile share.
type Counts struct {
	Total   int
	Modules []ModuleCount
	Oldest  *time.Time
}

func countStatement(ctx context.Context, q workflow.Querier, sources []Source, c Caller, scope Scope) (Counts, error) {
	out := Counts{Modules: []ModuleCount{}}
	if len(sources) == 0 {
		return out, nil
	}
	sql, args, err := buildCount(sources, scope, c.EmployeeID)
	if err != nil {
		return out, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return out, fmt.Errorf("count approval inbox: %w", err)
	}
	defer rows.Close()
	byKey := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		var oldest *time.Time
		if err := rows.Scan(&key, &n, &oldest); err != nil {
			return out, fmt.Errorf("scan approval inbox count: %w", err)
		}
		byKey[key] = n
		out.Total += n
		if oldest != nil && (out.Oldest == nil || oldest.Before(*out.Oldest)) {
			out.Oldest = oldest
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate approval inbox count: %w", err)
	}
	for _, s := range sources {
		out.Modules = append(out.Modules, ModuleCount{WorkflowKey: s.Key, Label: metas[s.Key].Label, Count: byKey[s.Key]})
	}
	return out, nil
}

// errScopeAll: "all" is refused for non-super-admins here as well as in the controller
// (defence in depth).
var errScopeAll = errors.New("approvalinbox: scope all requires a super admin")

func checkScope(c Caller, scope Scope) error {
	if !scope.Valid() {
		return fmt.Errorf("approvalinbox: invalid scope %q", scope)
	}
	if scope == ScopeAll && !c.IsSuperAdmin {
		return errScopeAll
	}
	return nil
}

// Count counts the caller's inbox with exactly the predicate List pages over, so the
// dashboard "Needs Approval" tile equals the inbox total. Eligible reports whether the
// tile is meaningful for the caller at all (they can see at least one module they
// approve for), independent of the current count.
func Count(ctx context.Context, q workflow.Querier, c Caller, scope Scope) (Counts, bool, error) {
	if err := checkScope(c, scope); err != nil {
		return Counts{}, false, err
	}
	check, err := GrantsCheck(ctx, q, c.IdentityID)
	if err != nil {
		return Counts{}, false, err
	}
	return CountWith(ctx, q, check, c, scope)
}

// CountWith is Count with an injected RBAC check.
func CountWith(ctx context.Context, q workflow.Querier, check CheckFunc, c Caller, scope Scope) (Counts, bool, error) {
	if err := checkScope(c, scope); err != nil {
		return Counts{}, false, err
	}
	sources := Permitted(check)
	counts, err := countStatement(ctx, q, sources, c, scope)
	if err != nil {
		return counts, false, err
	}
	eligible := counts.Total > 0 || (len(sources) > 0 && c.IsSuperAdmin)
	if !eligible && len(sources) > 0 && c.EmployeeID > 0 {
		eligible, err = isApproverAnywhere(ctx, q, sources, c.EmployeeID)
	}
	return counts, eligible, err
}
