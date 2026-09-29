package crmnotify

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

// TestRuleForStatus pins the CRM email diagram: who is told on each status.
func TestRuleForStatus(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		status   string
		event    string
		groups   []Group
	}{
		{"lead new", KeyLead, "LNEW", "lead.created", []Group{GroupOwner}},
		{"lead qualified", KeyLead, "LQUA", "lead.qualified", []Group{GroupOwner}},
		{"lead unqualified", KeyLead, "LUNQ", "lead.unqualified", []Group{GroupManager}},
		{"prospect new", KeyProspect, "PNEW", "prospect.created", []Group{GroupOwner, GroupManager}},
		{"proposal sent", KeyProspect, "PPRP", "prospect.proposal_sent", []Group{GroupManager}},
		{"in negotiation", KeyProspect, "PNEG", "prospect.in_negotiation", []Group{GroupManager}},
		{"decision pending", KeyProspect, "PIDM", "prospect.decision_pending", []Group{GroupOwner, GroupManager}},
		{"lost", KeyProspect, "PCLL", "prospect.lost", []Group{GroupManager}},
		{"pending conversion", KeyProspect, "PPCV", "prospect.pending_conversion", []Group{GroupOwner}},
		{"customer draft", KeyCustomer, "CDRF", "customer.draft", []Group{GroupOwner, GroupManager}},
		{"customer active", KeyCustomer, "CACT", "customer.active", []Group{GroupOwner, GroupManager, GroupFinance}},
		{"credit hold", KeyCustomer, "CCHD", "customer.credit_hold", []Group{GroupOwner, GroupManager, GroupFinance}},
		{"customer inactive", KeyCustomer, "CINA", "customer.inactive", []Group{GroupOwner, GroupManager, GroupFinance}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule, ok := RuleForStatus(tc.workflow, tc.status)
			require.True(t, ok)
			assert.Equal(t, tc.event, rule.EventType)
			assert.Equal(t, tc.groups, rule.Groups)
		})
	}
}

func TestRuleForStatus_SilentStatuses(t *testing.T) {
	for _, tc := range []struct{ workflow, status string }{
		{KeyProspect, "PPUR"}, // Contacted
		{KeyProspect, "PDIS"}, // In Discussion
		{KeyLead, "CACT"},     // a status from another stage
		{"", "LNEW"},          // unresolved workflow (v1 store)
		{KeyCustomer, ""},
	} {
		_, ok := RuleForStatus(tc.workflow, tc.status)
		assert.False(t, ok, "%s/%s", tc.workflow, tc.status)
	}
}

func testRecord(key, statusCode string, core map[string]any) *workflow.Record {
	c := map[string]any{"crm_status_code": statusCode, "crm_status_name": "Status Name", "customer_name": "Acme Stone"}
	for k, v := range core {
		c[k] = v
	}
	return &workflow.Record{ID: "rec-uuid", WorkflowID: key, RecordNumber: "CUST-000007", CoreFields: c}
}

func TestBuildRequest(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig = config.Config{FrontendURL: "https://app.example.com", EmailBrandName: "StoneSuite", SupportEmail: "support@stonesuite.app"}
	t.Cleanup(func() { config.AppConfig = prev })

	rec := testRecord(KeyCustomer, "CACT", nil)
	rule, _ := RuleForStatus(KeyCustomer, "CACT")
	recipients := []services.RecipientTarget{{UserID: "id-1", Email: "a@x.com", Name: "Ann"}}
	req := BuildRequest("tenant-1", "actor-1", rec, rule, "", recipients, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC))

	assert.Equal(t, "tenant-1", req.TenantID)
	assert.Equal(t, "actor-1", req.ActorUserID, "ActorUserID must be set so Notify knows who acted")
	assert.Equal(t, "customer.active", req.EventType)
	assert.Equal(t, "customer", req.Resource)
	assert.Equal(t, "rec-uuid", req.ResourceID)
	assert.Equal(t, "/crm/customer/rec-uuid", req.Link)
	assert.Equal(t, "Customer CUST-000007 is now Active.", req.Title)
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Equal(t, recipients, req.Recipients)

	body, err := services.RenderEmail(*req.Email)
	require.NoError(t, err)
	assert.Contains(t, body, "Customer CUST-000007")
	assert.Contains(t, body, "Acme Stone")
	assert.Contains(t, body, "https://app.example.com/crm/customer/rec-uuid")
	assert.NotContains(t, body, ">Reason<", "no reason row when none is given")
}

func TestBuildRequest_RejectionCarriesReason(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig = config.Config{FrontendURL: "https://app.example.com", EmailBrandName: "StoneSuite", SupportEmail: "s@x.com"}
	t.Cleanup(func() { config.AppConfig = prev })

	req := BuildRequest("t", "a", testRecord(KeyCustomer, "CDRF", nil), approvalRejectedRule, "Missing <tax> id", nil, time.Now())
	body, err := services.RenderEmail(*req.Email)
	require.NoError(t, err)
	assert.Equal(t, "customer.approval_rejected", req.EventType)
	assert.Contains(t, body, "Missing &lt;tax&gt; id", "reason is shown and HTML-escaped")
}

// TestGuards checks the paths that must send nothing and never touch the DB
// (the querier is nil, so any query would panic).
func TestGuards_NoSend(t *testing.T) {
	send := func(context.Context, services.NotificationRequest) error {
		t.Fatal("send must not be called")
		return nil
	}
	ctx := context.Background()

	NotifyStatus(ctx, nil, send, "a", nil)
	NotifyStatus(ctx, nil, send, "a", testRecord(KeyProspect, "PPUR", nil))
	NotifyApprovalRequestedIfPending(ctx, nil, send, "a", nil)
	NotifyApprovalRequestedIfPending(ctx, nil, send, "a", testRecord(KeyCustomer, "CDRF", map[string]any{"approval_status": "approved"}))
	NotifyApprovalRequestedIfPending(ctx, nil, send, "a", testRecord(KeyLead, "LNEW", map[string]any{"approval_status": "pending"}))
	NotifyRejected(ctx, nil, send, "a", nil, "why")
	NotifyRejected(ctx, nil, send, "a", testRecord(KeyProspect, "PNEW", nil), "why")
}

// TestDeliver_NoTenantSkips: with a matching rule but no tenant in context the
// event is dropped before any recipient lookup.
func TestDeliver_NoTenantSkips(t *testing.T) {
	send := func(context.Context, services.NotificationRequest) error {
		t.Fatal("send must not be called")
		return nil
	}
	NotifyStatus(context.Background(), nil, send, "a", testRecord(KeyLead, "LNEW", nil))
}
