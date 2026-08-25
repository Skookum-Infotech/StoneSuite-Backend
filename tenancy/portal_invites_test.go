package tenancy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Expiry is a derived property, not a stored status: an expired invite stays
// 'pending' so a resend can refresh it in place. These assert that derivation,
// which is what the accept path and the staff UI both branch on.
func TestPortalInviteExpiredAndUsable(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	tests := []struct {
		name        string
		status      string
		expiresAt   time.Time
		wantExpired bool
		wantUsable  bool
	}{
		{"pending and in date", PortalInviteStatusPending, future, false, true},
		{"pending but past expiry", PortalInviteStatusPending, past, true, false},
		{"accepted in date", PortalInviteStatusAccepted, future, false, false},
		{"accepted past expiry", PortalInviteStatusAccepted, past, false, false},
		{"revoked in date", PortalInviteStatusRevoked, future, false, false},
		{"revoked past expiry", PortalInviteStatusRevoked, past, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := &PortalInvite{Status: tt.status, ExpiresAt: tt.expiresAt}
			assert.Equal(t, tt.wantExpired, inv.Expired(), "Expired()")
			assert.Equal(t, tt.wantUsable, inv.Usable(), "Usable()")
		})
	}
}

// An invite must never be both expired and usable — that combination would mean
// a stale link still redeems.
func TestPortalInviteExpiredAndUsableAreExclusive(t *testing.T) {
	for _, status := range []string{
		PortalInviteStatusPending, PortalInviteStatusAccepted, PortalInviteStatusRevoked,
	} {
		for _, exp := range []time.Time{time.Now().Add(-time.Hour), time.Now().Add(time.Hour)} {
			inv := &PortalInvite{Status: status, ExpiresAt: exp}
			assert.False(t, inv.Expired() && inv.Usable(),
				"status=%s expired and usable at once", status)
		}
	}
}

// A status the CHECK constraint does not permit must still fail closed if one
// ever reached the struct.
func TestPortalInviteUnknownStatusIsNotUsable(t *testing.T) {
	inv := &PortalInvite{Status: "sent", ExpiresAt: time.Now().Add(time.Hour)}
	assert.False(t, inv.Usable(), "an unrecognised status must not be redeemable")
	assert.False(t, inv.Expired())
}
