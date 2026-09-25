package ai

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// Record types rag_chunks.record_type may hold — the CRM workflow keys the
// index worker writes (see crmstore.CRMWorkflowKeys). A grant naming anything
// else is ignored.
var recordTypes = map[string]bool{"lead": true, "prospect": true, "customer": true}

// Scope values a grant may carry. Anything else is treated as no grant.
const (
	ScopeOwn = "own"
	ScopeAll = "all"
)

// Grants maps each CRM record type the caller may read to their RBAC scope for
// it ("own" or "all"). A type absent from the map is not readable at all.
//
// Per type, not one scope for everything: rag_chunks mixes leads, prospects and
// customers, so a single clause either over-exposes (a lead:all grant reaching
// customers the caller has no grant on) or under-includes (a customer:own grant
// narrowing leads the caller may see in full).
type Grants map[string]string

// Types returns the record types this grant set can actually read, sorted —
// unknown types and unrecognised scopes are dropped, fail-closed.
func (g Grants) Types() []string {
	var out []string
	for t, scope := range g {
		if recordTypes[t] && (scope == ScopeOwn || scope == ScopeAll) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// RecordScopeFilter narrows rag_chunks to the rows grants allows:
//
//	(record_type = $a) OR (record_type = $b AND owner_user_id = $c) OR ...
//
// Every value — type names included — is a bound parameter. A row with a NULL
// record_type (indexed before the column existed, not yet refreshed by the
// reconciliation sweep) matches no clause, so it is invisible rather than
// visible to everyone. No readable types at all yields DenyAll.
func RecordScopeFilter(grants Grants, callerUserID string) rag.ScopeFilter {
	types := grants.Types()
	if len(types) == 0 {
		return rag.DenyAll
	}
	return func(firstArg int) (string, []any) {
		n := firstArg
		parts := make([]string, 0, len(types))
		args := make([]any, 0, len(types)*2)
		for _, t := range types {
			switch grants[t] {
			case ScopeAll:
				parts = append(parts, fmt.Sprintf("record_type = $%d", n))
				args = append(args, t)
				n++
			case ScopeOwn:
				parts = append(parts, fmt.Sprintf("(record_type = $%d AND owner_user_id = $%d)", n, n+1))
				args = append(args, t, callerUserID)
				n += 2
			}
		}
		return strings.Join(parts, " OR "), args
	}
}
