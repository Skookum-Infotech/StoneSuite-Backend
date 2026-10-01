package mytransactions

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/globalsearch"
)

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// notBusinessRecords are globalsearch providers that are not "records the user
// created or updated" in the My Transactions sense: sub-entities of a customer,
// and tenant users.
var notBusinessRecords = map[string]bool{
	"crm_activity":  true,
	"customer_note": true,
	"user":          true,
}

func TestSources_WellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Sources() {
		t.Run(s.Key, func(t *testing.T) {
			assert.Regexp(t, identRe, s.Key, "key is embedded in SQL as a literal")
			assert.False(t, seen[s.Key], "duplicate key")
			seen[s.Key] = true

			assert.NotEmpty(t, s.Label)
			assert.NotEmpty(t, s.Resource)
			assert.NotEmpty(t, s.Domain)
			assert.NotEmpty(t, s.Module)
			assert.Regexp(t, identRe, s.Table)

			for name, col := range map[string]string{
				"CreatedBy": s.CreatedBy, "CreatedAt": s.CreatedAt,
				"UpdatedAt": s.UpdatedAt, "DeletedAt": s.DeletedAt,
			} {
				assert.Regexp(t, identRe, col, "%s must be a bare column", name)
			}
			for name, col := range map[string]string{"UpdatedBy": s.UpdatedBy, "Owner": s.Owner} {
				if col != "" {
					assert.Regexp(t, identRe, col, "%s must be a bare column", name)
				}
			}

			assert.NotEmpty(t, s.ID, "every record needs an id to route on")
			assert.NotEmpty(t, s.Number+s.Name, "every record needs something to show")
			assert.NotEqual(t, s.UpdatedBy != "", s.AuditUpdated,
				"exactly one source of 'updated by me': an updated_by column or the audit trail")
			assert.NotContains(t, s.Where+s.Joins+s.ID+s.Number+s.Name+s.Account+s.Amount, ";",
				"a registry fragment must be a single static expression")
		})
	}
	assert.Len(t, Sources(), 26)
}

// TestSources_MatchGlobalSearch pins this registry to globalsearch's: same key,
// RBAC resource and route segments for every shared module, and no business
// module there left out here (the "dead module" trap the other registries
// guard against).
func TestSources_MatchGlobalSearch(t *testing.T) {
	ours := map[string]Source{}
	for _, s := range Sources() {
		ours[s.Key] = s
	}
	theirs := map[string]bool{}
	for _, p := range globalsearch.All() {
		theirs[p.Key] = true
		if notBusinessRecords[p.Key] {
			continue
		}
		s, ok := ours[p.Key]
		require.Truef(t, ok, "globalsearch module %q is missing from the My Transactions registry", p.Key)
		assert.Equal(t, p.Resource, s.Resource, "%s: RBAC resource", p.Key)
		assert.Equal(t, p.Domain, s.Domain, "%s: route domain", p.Key)
		assert.Equal(t, p.Module, s.Module, "%s: route module", p.Key)
	}
	for key := range ours {
		assert.Truef(t, theirs[key], "%q is not a globalsearch module — route/RBAC cannot be cross-checked", key)
	}
}
