//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardExternalWeeklyCapIgnoresInternal proves a sent internal row for the
// same address does not count toward the external per-contact weekly cap.
func TestGuardExternalWeeklyCapIgnoresInternal(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	sent := env.Now.Add(-time.Hour)
	f.insert(dRow{status: "sent", sentAt: &sent, digest: "internal-1", kind: "internal"})
	s := automationSettings{DailyCap: 200, WeeklyCap: 1}

	reason, err := guardExternal(f.ctx, f.pool, s, f.email, env.Now)
	require.NoError(t, err)
	assert.Equal(t, "", reason, "internal rows must not count")

	f.insert(dRow{status: "sent", sentAt: &sent, digest: "external-1"})
	reason, err = guardExternal(f.ctx, f.pool, s, f.email, env.Now)
	require.NoError(t, err)
	assert.Equal(t, reasonCap, reason, "external row counts")
}
