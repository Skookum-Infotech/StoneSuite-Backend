package globalsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// selectProviders is pure (reads the registry, applies an allowlist) and
// testable without a database. The RBAC-denial and partial-failure behavior
// of Search itself is covered by controllers/globalsearch_dbtest_test.go,
// since authz.Check requires a real tenant pool.
func TestSelectProviders(t *testing.T) {
	all := All()
	assert.NotEmpty(t, all, "registry must have providers registered for this test to be meaningful")

	t.Run("empty allowlist returns every provider", func(t *testing.T) {
		got := selectProviders(nil)
		assert.Len(t, got, len(all))
	})

	t.Run("allowlist restricts to matching keys", func(t *testing.T) {
		got := selectProviders([]string{"quote", "vendor"})
		keys := make(map[string]bool, len(got))
		for _, p := range got {
			keys[p.Key] = true
		}
		assert.Equal(t, map[string]bool{"quote": true, "vendor": true}, keys)
	})

	t.Run("unknown keys are ignored, not an error", func(t *testing.T) {
		got := selectProviders([]string{"not_a_real_module"})
		assert.Empty(t, got)
	})

	t.Run("allowlist tolerates surrounding whitespace", func(t *testing.T) {
		got := selectProviders([]string{" quote ", "vendor"})
		assert.Len(t, got, 2)
	})
}
