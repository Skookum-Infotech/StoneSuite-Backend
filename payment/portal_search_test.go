package payment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/portal"
)

// The portal predicate is the security boundary for customer reads, so its
// shape is asserted directly. These are unit tests on the WHERE construction —
// the end-to-end "customer A cannot see customer B" assertion lives in the
// dbtest integration suite.

func TestPortalWhereScopesToCustomerAndVisibleStatuses(t *testing.T) {
	where, args, nextIdx, err := portalWhere(42)
	require.NoError(t, err)

	// The customer predicate must be present, bound to $1, and compare the
	// module's own customer column.
	assert.Contains(t, where, "p.payment_customer_id = $1")
	// The status predicate must be present and bound to $2.
	assert.Contains(t, where, "rs.record_status_code = ANY($2)")
	// Soft-deleted rows are excluded.
	assert.Contains(t, where, "p.payment_deleted_at IS NULL")

	require.Len(t, args, 2)
	assert.Equal(t, 42, args[0], "the customer id must be the first bound arg")

	vis, ok := portal.Visible(portal.ModulePayment)
	require.True(t, ok)
	assert.Equal(t, vis.StatusCodes, args[1],
		"the status arg must come from the visibility table, not be inlined")

	// User filters must start after the two predicate params, or query.Build
	// would collide with them.
	assert.Equal(t, 3, nextIdx)
}

// A caller-supplied filter can only ever narrow. The predicate clauses are
// joined with AND by PortalSearch, so a filter naming another customer yields
// an impossible conjunction rather than widening access.
func TestPortalWhereClausesAreConjunctive(t *testing.T) {
	where, _, _, err := portalWhere(7)
	require.NoError(t, err)

	// Every clause must stand alone: an OR at this level would let a filter
	// widen the result set.
	for _, clause := range where {
		assert.NotContains(t, clause, " OR ",
			"portal predicate clause %q must not contain OR", clause)
	}
	// The customer clause must not be optional.
	assert.NotEmpty(t, where)
}

// The customer id is bound as a parameter, never interpolated, so it cannot
// carry SQL even if a future caller passes something unexpected.
func TestPortalWhereParameterizesCustomer(t *testing.T) {
	where, args, _, err := portalWhere(999999)
	require.NoError(t, err)
	for _, clause := range where {
		assert.NotContains(t, clause, "999999",
			"customer id must be bound, not interpolated into %q", clause)
	}
	assert.Equal(t, 999999, args[0])
}
