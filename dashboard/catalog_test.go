package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// TestCatalogKeysAreUnique guards against a copy-paste duplicate Key silently
// shadowing another widget in ByKey lookups.
func TestCatalogKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range Catalog() {
		require.False(t, seen[w.Key], "duplicate widget key %q", w.Key)
		seen[w.Key] = true
	}
}

// TestCatalogPermissionsAreGrantable asserts every widget's {Resource, Action}
// exists in the authz catalog. A widget riding on a permission no role can
// ever hold is a widget nobody can ever see -- the same drift class
// controllers/rbac_catalog_drift_test.go guards for CRM resources.
func TestCatalogPermissionsAreGrantable(t *testing.T) {
	for _, w := range Catalog() {
		assert.Truef(t, authz.IsValidPermission(w.Resource, w.Action),
			"widget %q rides on {%s, %s} which is missing from the authz catalog (ungrantable widget). Add it to authz/catalog.go.",
			w.Key, w.Resource, w.Action)
	}
}

// TestCatalogDefaultsAreInBounds guards the grid bounds (MinSize..MaxWidth,
// MinSize..MaxHeight) so a catalog entry can't ship a default ValidatePrefs
// would itself reject.
func TestCatalogDefaultsAreInBounds(t *testing.T) {
	for _, w := range Catalog() {
		assert.GreaterOrEqualf(t, w.DefaultPosition, 0, "widget %q has a negative DefaultPosition", w.Key)
		assert.Truef(t, w.DefaultWidth >= MinSize && w.DefaultWidth <= MaxWidth,
			"widget %q DefaultWidth %d out of [%d,%d]", w.Key, w.DefaultWidth, MinSize, MaxWidth)
		assert.Truef(t, w.DefaultHeight >= MinSize && w.DefaultHeight <= MaxHeight,
			"widget %q DefaultHeight %d out of [%d,%d]", w.Key, w.DefaultHeight, MinSize, MaxHeight)
	}
}

// TestByKey covers both the hit and miss paths.
func TestByKey(t *testing.T) {
	w, ok := ByKey("sales.quotes")
	require.True(t, ok)
	assert.Equal(t, "Quotes", w.Title)

	_, ok = ByKey("does.not.exist")
	assert.False(t, ok)
}

// TestCatalogReturnsACopy proves mutating the returned slice cannot corrupt
// the package-level catalog for the next caller (mirrors authz.Catalog's
// contract).
func TestCatalogReturnsACopy(t *testing.T) {
	first := Catalog()
	require.NotEmpty(t, first)
	first[0].Title = "corrupted"
	second := Catalog()
	assert.NotEqual(t, "corrupted", second[0].Title)
}
