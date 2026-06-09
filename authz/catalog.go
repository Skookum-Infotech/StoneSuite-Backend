// Package authz implements StoneSuite's dynamic role-based access control:
// a stable permission CATALOG defined in Go (resource x action), tenant-scoped
// roles that bundle {resource, action, scope} grants, and an enforcer that
// resolves whether a caller may perform an action and at what scope.
//
// All RBAC data lives in the per-tenant database (roles, role_permissions,
// user_roles), so a caller's permissions are resolved against the tenant pool
// already attached to the request by the tenancy resolver middleware.
package authz

// Resource identifies a thing that actions are performed on. The catalog is a
// stable, code-defined list; super admins compose roles from it in the UI.
type Resource string

// Action identifies an operation performed on a resource.
type Action string

// Scope narrows how many rows an action may touch. Precedence: all > team > own.
type Scope string

const (
	ResourceWorkflow       Resource = "workflow"        // workflow definitions
	ResourceRecord         Resource = "record"          // generic workflow engine records
	ResourceLead           Resource = "lead"            // CRM leads
	ResourceProspect       Resource = "prospect"        // CRM prospects
	ResourceCustomer       Resource = "customer"        // CRM customers
	ResourceUser           Resource = "user"            // tenant users
	ResourceRole           Resource = "role"            // roles & permissions
	ResourceTeam           Resource = "team"            // teams & membership
	ResourceWorkflowConfig Resource = "workflow_config" // states/transitions/fields config
	ResourceSSOConfig      Resource = "sso_config"      // per-tenant SSO settings
	ResourceAudit          Resource = "audit"           // audit log

	// ResourceAny is the wildcard resource. Granting it matches every resource;
	// it is how the seeded super_admin role is expressed as a single row.
	ResourceAny Resource = "*"
)

const (
	ActionCreate     Action = "create"
	ActionRead       Action = "read"
	ActionUpdate     Action = "update"
	ActionDelete     Action = "delete"
	ActionTransition Action = "transition" // move a record between workflow states
	ActionConfigure  Action = "configure"  // edit definitions/settings

	// ActionAny is the wildcard action. Granting it matches every action.
	ActionAny Action = "*"
)

const (
	ScopeAll  Scope = "all"  // every row in the tenant
	ScopeTeam Scope = "team" // rows owned by the caller's team(s)
	ScopeOwn  Scope = "own"  // only rows the caller owns
)

// Permission is a single {resource, action} pair from the catalog.
type Permission struct {
	Resource Resource `json:"resource"`
	Action   Action   `json:"action"`
}

// catalog is the authoritative list of resource x action permissions a role
// may grant. Adding a capability is a one-line change here.
var catalog = []Permission{
	{ResourceWorkflow, ActionRead},
	{ResourceWorkflow, ActionConfigure},

	{ResourceRecord, ActionCreate},
	{ResourceRecord, ActionRead},
	{ResourceRecord, ActionUpdate},
	{ResourceRecord, ActionDelete},
	{ResourceRecord, ActionTransition},

	{ResourceLead, ActionCreate},
	{ResourceLead, ActionRead},
	{ResourceLead, ActionUpdate},
	{ResourceLead, ActionDelete},
	{ResourceLead, ActionTransition},

	{ResourceProspect, ActionCreate},
	{ResourceProspect, ActionRead},
	{ResourceProspect, ActionUpdate},
	{ResourceProspect, ActionDelete},
	{ResourceProspect, ActionTransition},

	{ResourceCustomer, ActionCreate},
	{ResourceCustomer, ActionRead},
	{ResourceCustomer, ActionUpdate},
	{ResourceCustomer, ActionDelete},
	{ResourceCustomer, ActionTransition},

	{ResourceUser, ActionCreate},
	{ResourceUser, ActionRead},
	{ResourceUser, ActionUpdate},
	{ResourceUser, ActionDelete},

	{ResourceRole, ActionRead},
	{ResourceRole, ActionConfigure},

	{ResourceTeam, ActionRead},
	{ResourceTeam, ActionConfigure},

	{ResourceWorkflowConfig, ActionRead},
	{ResourceWorkflowConfig, ActionConfigure},

	{ResourceSSOConfig, ActionRead},
	{ResourceSSOConfig, ActionConfigure},

	{ResourceAudit, ActionRead},
}

// Catalog returns a copy of the permission catalog (safe for callers to mutate).
func Catalog() []Permission {
	out := make([]Permission, len(catalog))
	copy(out, catalog)
	return out
}

// validResources / validActions / validScopes are derived once for O(1) checks.
var (
	validResources = buildResourceSet()
	validActions   = buildActionSet()
	validScopes    = map[Scope]bool{ScopeAll: true, ScopeTeam: true, ScopeOwn: true}
)

func buildResourceSet() map[Resource]bool {
	m := map[Resource]bool{}
	for _, p := range catalog {
		m[p.Resource] = true
	}
	return m
}

func buildActionSet() map[Action]bool {
	m := map[Action]bool{}
	for _, p := range catalog {
		m[p.Action] = true
	}
	return m
}

// IsValidPermission reports whether {resource, action} exists in the catalog.
// Wildcards are intentionally rejected here: callers (the role editor) may only
// grant concrete catalog permissions; wildcards are reserved for system seeding.
func IsValidPermission(r Resource, a Action) bool {
	return validResources[r] && validActions[a] && permissionInCatalog(r, a)
}

func permissionInCatalog(r Resource, a Action) bool {
	for _, p := range catalog {
		if p.Resource == r && p.Action == a {
			return true
		}
	}
	return false
}

// IsValidScope reports whether s is one of all|team|own.
func IsValidScope(s Scope) bool { return validScopes[s] }
