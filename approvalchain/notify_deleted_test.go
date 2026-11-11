package approvalchain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListRoute(t *testing.T) {
	tests := []struct {
		name, resource, want string
	}{
		{"sales resource", "invoice", "/sales/invoice"},
		{"purchases resource", "vendor_bill", "/purchases/vendor_bill"},
		{"list-only quote", "quote", "/sales/quote"},
		{"list-only estimate", "estimate", "/sales/estimate"},
		{"list-only sales order", "sales_order", "/sales/sales_order"},
		{"unknown resource", "nope", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, listRoute(tt.resource))
		})
	}
}

func TestDeletedRecipients(t *testing.T) {
	actor := contact{UserID: "id-actor", Email: "a@example.com", Name: "Ann"}
	owner := contact{UserID: "id-owner", Email: "o@example.com", Name: "Oz"}
	same := contact{UserID: "id-actor", Email: "a@example.com", Name: "Ann"}

	tests := []struct {
		name       string
		actorOK    bool
		owner      contact
		ownerOK    bool
		wantActors int
		wantOwners int
	}{
		{"actor and distinct owner", true, owner, true, 1, 1},
		{"actor is owner dedupes to one", true, same, true, 1, 0},
		{"no owner", true, contact{}, false, 1, 0},
		{"no actor keeps owner", false, owner, true, 0, 1},
		{"neither", false, contact{}, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actors, owners := deletedRecipients(actor, tt.actorOK, tt.owner, tt.ownerOK)
			assert.Len(t, actors, tt.wantActors)
			assert.Len(t, owners, tt.wantOwners)
		})
	}
}

func TestBuildDeletedNotification(t *testing.T) {
	ec := EventContext{Resource: "invoice", DisplayName: "Invoice", RecordUUID: "rec-1"}
	facts := recordFacts{Number: "INV-000123"}
	c := []contact{{UserID: "id-1", Email: "a@example.com", Name: "Ann"}}

	tests := []struct {
		name       string
		note       approvalNote
		wantReason string
	}{
		{"actor copy", noteDeleted, "you deleted this record."},
		{"owner copy", noteDeletedOwner, "you own this record."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildApprovalNotification("tenant-9", "invoice.deleted", "id-1", facts, ec, tt.note, c)
			assert.Equal(t, "invoice.deleted", req.EventType)
			assert.Equal(t, "Invoice INV-000123 was deleted", req.Title)
			assert.Equal(t, "Deleted.", req.Body)
			assert.Equal(t, "/sales/invoice", req.Link, "must link to the list, not the deleted record")
			require.NotNil(t, req.Email)
			assert.Equal(t, "Deleted", req.Email.Badge)
			assert.Equal(t, "This record has been removed.", req.Email.Subtitle)
			assert.Equal(t, tt.wantReason, req.Email.Reason)
			assert.Equal(t, "View invoice", req.Email.ActionLabel)
			require.Len(t, req.Recipients, 1)
			assert.Equal(t, "id-1", req.ActorUserID)
		})
	}
}

func TestNotifyDeletedNoopWithoutDisplayName(t *testing.T) {
	// A nil pool would panic if the guard did not return first.
	assert.NotPanics(t, func() {
		NotifyDeleted(context.Background(), nil, EventContext{Resource: "invoice"})
	})
}
