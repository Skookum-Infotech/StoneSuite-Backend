package automation

import (
	"context"
	"errors"
	"fmt"

	"stonesuite-backend/tenancy"
)

// PortalIdentity is a customer's customer-portal login. It is what makes a notification appear in
// the customer's in-app feed: a notification needs a user id to be listed anywhere, and a customer
// has one only when they have accepted a portal invite.
type PortalIdentity struct {
	IdentityID string
	Email      string
	Name       string
}

// PortalLookup finds the active portal login of a customer (a tenant-database customer uuid). found
// is false when the customer has no accepted invite or their access was revoked; that is not an
// error, it just means the customer can be reached by email only.
type PortalLookup func(ctx context.Context, tenantID, customerUUID string) (id PortalIdentity, found bool, err error)

// ControlPlanePortalLookup is the production PortalLookup, with the same rules the approval
// notifications to customers use (controllers.notifyCustomerEvent): the most recent accepted invite
// whose link is still active.
func ControlPlanePortalLookup(cp *tenancy.ControlPlane) PortalLookup {
	return func(ctx context.Context, tenantID, customerUUID string) (PortalIdentity, bool, error) {
		invite, err := cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if errors.Is(err, tenancy.ErrPortalInviteNotFound) {
			return PortalIdentity{}, false, nil
		}
		if err != nil {
			return PortalIdentity{}, false, fmt.Errorf("portal invite for customer: %w", err)
		}
		active, err := cp.PortalLinkActive(ctx, invite.IdentityID, tenantID)
		if err != nil {
			return PortalIdentity{}, false, fmt.Errorf("portal link active: %w", err)
		}
		if !active {
			return PortalIdentity{}, false, nil
		}
		identity, err := cp.IdentityByID(ctx, invite.IdentityID)
		if err != nil {
			return PortalIdentity{}, false, fmt.Errorf("portal identity: %w", err)
		}
		return PortalIdentity{IdentityID: identity.ID, Email: identity.Email, Name: identity.FullName}, true, nil
	}
}
