//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantOps_EmailClaimedElsewhere_DB guards the platform-wide uniqueness of
// onboarding emails: an address owned by one tenant must be rejected for any
// other tenant's onboarding, regardless of case, while the owning tenant may
// still reuse it (re-send / re-submit).
func TestTenantOps_EmailClaimedElsewhere_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	h := &TenantOps{CP: cp}

	suffix := time.Now().UnixNano()
	owner, err := cp.CreateTenant(ctx, fmt.Sprintf("email-owner-%d", suffix), "Email Owner", false)
	require.NoError(t, err)
	other, err := cp.CreateTenant(ctx, fmt.Sprintf("email-other-%d", suffix), "Email Other", false)
	require.NoError(t, err)

	taken := fmt.Sprintf("taken-%d@example.com", suffix)
	_, err = cp.CreateIdentity(ctx, owner.ID, taken, "", "Owner", false)
	require.NoError(t, err)

	tests := []struct {
		name       string
		email      string
		ownTenant  string
		wantStatus int
	}{
		{"unused email is free", fmt.Sprintf("free-%d@example.com", suffix), "", 0},
		{"taken email rejected for new tenant", taken, "", http.StatusConflict},
		{"taken email rejected for a different tenant", taken, other.ID, http.StatusConflict},
		{"case variant is still taken", strings.ToUpper(taken), other.ID, http.StatusConflict},
		{"owning tenant may reuse its own email", taken, owner.ID, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := h.emailClaimedElsewhere(ctx, tc.email, tc.ownTenant)
			assert.Equal(t, tc.wantStatus, status)
			if tc.wantStatus == 0 {
				assert.Empty(t, msg)
			} else {
				assert.Contains(t, msg, "already registered")
			}
		})
	}
}
