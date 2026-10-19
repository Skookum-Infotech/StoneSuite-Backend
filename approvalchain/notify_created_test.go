package approvalchain

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildActorCreatedRequest pins the Notify wire shape for the created-event
// email: who is emailed, who acted, and the resource the bell files it under.
func TestBuildActorCreatedRequest(t *testing.T) {
	ev := ActorCreated{
		EventType: "crm_activity.created", Resource: "crm_activity", RecordUUID: "rec-1",
		Title: "Task logged: Call back", Body: "A task was logged.",
	}
	actor := contact{UserID: "identity-1", Email: "a@example.com", Name: "Ann"}

	tests := []struct {
		name       string
		actorUser  string
		wantActor  string
		wantEmail  string
		wantRecips int
	}{
		{name: "staff actor is both recipient and actor", actorUser: "identity-1", wantActor: "identity-1", wantEmail: "a@example.com", wantRecips: 1},
		{name: "no staff actor leaves ActorUserID empty", actorUser: "", wantActor: "", wantEmail: "a@example.com", wantRecips: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildActorCreatedRequest("tenant-9", actor, tt.actorUser, ev)
			assert.Equal(t, "tenant-9", req.TenantID)
			assert.Equal(t, tt.wantActor, req.ActorUserID)
			assert.Equal(t, "crm_activity.created", req.EventType)
			assert.Equal(t, "crm_activity", req.Resource)
			assert.Equal(t, "rec-1", req.ResourceID)
			assert.Equal(t, []string{"email"}, req.Channels)
			require.Len(t, req.Recipients, tt.wantRecips)
			assert.Equal(t, "identity-1", req.Recipients[0].UserID)
			assert.Equal(t, tt.wantEmail, req.Recipients[0].Email)
		})
	}
}

// TestCreatedNotifyCoverage guards clone discipline: every create path (and each
// conversion that mints a new record) must call its created-event hook. A new
// create or convert that forgets the hook fails here, not silently in prod.
func TestCreatedNotifyCoverage(t *testing.T) {
	tests := []struct {
		file   string
		marker string
	}{
		// Document creates, after commit.
		{"../estimate/store_create.go", "notifyCreated(ctx"},
		{"../quote/store_create.go", "notifyCreated(ctx"},
		{"../salesorder/store_create.go", "notifyCreated(ctx"},
		{"../invoice/store_create.go", "notifyCreated(ctx"},
		{"../payment/store_create.go", "notifyCreated(ctx"},
		{"../creditmemo/store_create.go", "notifyCreated(ctx"},
		{"../refund/store_create.go", "notifyCreated(ctx"},
		{"../purchaseorder/store_create.go", "notifyCreated(ctx"},
		{"../requisition/store_create.go", "notifyCreated(ctx"},
		{"../vendorbill/store_create.go", "notifyCreated(ctx"},
		{"../vendorpayment/store_create.go", "notifyCreated(ctx"},
		{"../vendorcredit/store_create.go", "notifyCreated(ctx"},
		{"../expense/store_create.go", "notifyCreated(ctx"},
		{"../fabrication/store_create.go", "notifyCreated(ctx"},
		{"../bintransfer/store_write.go", "notifyCreated(ctx"},
		{"../payment/quickpay.go", "notifyCreated(ctx"},
		// Conversions that create a record from another one.
		{"../quote/store_convert.go", "notifyCreated(ctx"},
		{"../salesorder/store_convert.go", "notifyCreated(ctx"},
		{"../invoice/store_convert.go", "notifyCreated(ctx"},
		{"../purchaseorder/store_convert.go", "notifyCreated(ctx"},
		{"../vendorbill/store_convert.go", "notifyCreated(ctx"},
		// Records with no approval chain, and the portal order-form path.
		{"../crmactivity/store.go", "approvalchain.NotifyActorCreated("},
		{"../inventory/store_create.go", "approvalchain.NotifyActorCreated("},
		{"../orderform/store.go", "salesorder.NotifyOrderFormSubmitted("},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			require.NoError(t, err)
			assert.Contains(t, string(src), tt.marker, "create/convert path must send its created-event notification")
		})
	}
}
