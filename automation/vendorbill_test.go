package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var vbLoc = time.FixedZone("tenant", -5*3600)

func vbEnv(hour int) JobEnv {
	return JobEnv{Now: time.Date(2026, 11, 23, hour, 0, 0, 0, vbLoc),
		Today: time.Date(2026, 11, 23, 0, 0, 0, 0, vbLoc), Loc: vbLoc}
}

func vbRule() ruleRow {
	return ruleRow{ID: "rule-vb", Config: ARRuleConfig{Cadence: []ARCadenceStage{{OffsetDays: -3}, {OffsetDays: 0}}},
		EnabledOn: time.Date(2026, 11, 1, 0, 0, 0, 0, vbLoc)}
}

func vbDoc(number, email string, dueDay int) VendorBillDoc {
	return VendorBillDoc{UUID: "uuid-" + number, Number: number, VendorName: "Granite Co", OwnerEmail: email,
		OwnerUserID: "u1", OwnerIdentityID: "i1", Balance: 1250.5,
		DueDate: time.Date(2026, 11, dueDay, 0, 0, 0, 0, time.UTC)}
}

// vbTargetOf is the bill with its owner as the one recipient (none when the doc has no owner email).
func vbTargetOf(d VendorBillDoc) vendorBillTarget {
	t := vendorBillTarget{Doc: d}
	if d.OwnerEmail != "" {
		t.Rcpts = []vendorBillRecipient{{UserID: d.OwnerUserID, IdentityID: d.OwnerIdentityID, Email: d.OwnerEmail}}
	}
	return t
}

func TestVendorBillInsertsForApproversEachGetTheirOwnRow(t *testing.T) {
	d := vbDoc("VBIL-1", "", 26)
	target := vendorBillTarget{Doc: d, Rcpts: []vendorBillRecipient{
		{UserID: "u-a", Email: "a@x.io"}, {UserID: "u-b", Email: "b@x.io"}}}
	other := vendorBillTarget{Doc: vbDoc("VBIL-2", "", 26), Rcpts: target.Rcpts[:1]}

	got := vendorBillInsertsFor([]vendorBillTarget{target, other}, vbEnv(9), vbRule())
	require.Len(t, got, 3, "two approvers on one bill, one on the other")
	keys := map[string]bool{}
	for _, g := range got {
		keys[g.Key] = true
		assert.Equal(t, statusPlanned, g.Status)
		require.NotNil(t, g.UserID)
	}
	assert.Len(t, keys, 3, "every recipient and bill has its own occurrence key")
	assert.Equal(t, *got[0].Digest, *got[2].Digest, "one person gets one digest across all their bills")
	assert.NotEqual(t, *got[0].Digest, *got[1].Digest, "another person gets their own")
}

func TestVendorBillRecipientFor(t *testing.T) {
	rcpts := []vendorBillRecipient{{UserID: "u1", Email: "A@X.io"}, {UserID: "u2", Email: "b@x.io"}}
	got, ok := vendorBillRecipientFor(rcpts, "a@x.IO")
	assert.True(t, ok)
	assert.Equal(t, "u1", got.UserID, "the match ignores case")
	_, ok = vendorBillRecipientFor(rcpts, "stranger@x.io")
	assert.False(t, ok)
	_, ok = vendorBillRecipientFor(nil, "a@x.io")
	assert.False(t, ok)
}

func TestVendorBillInsertsFor(t *testing.T) {
	tests := []struct {
		name       string
		doc        VendorBillDoc
		wantStatus []string
	}{
		{"due in 3 days: first stage planned", vbDoc("VBIL-1", "o@x.io", 26), []string{statusPlanned}},
		{"due today: only the latest stage is planned, earlier superseded", vbDoc("VBIL-2", "o@x.io", 23), []string{statusSkipped, statusPlanned}},
		{"due in 2 days: stage 0 is catch-up", vbDoc("VBIL-3", "o@x.io", 25), []string{statusPlanned}},
		{"no email skipped", vbDoc("VBIL-4", "", 26), []string{statusSkipped}},
		{"due in 5 days: nothing due yet", vbDoc("VBIL-5", "o@x.io", 28), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := vendorBillInsertsFor([]vendorBillTarget{vbTargetOf(tc.doc)}, vbEnv(9), vbRule())
			var statuses []string
			for _, g := range got {
				statuses = append(statuses, g.Status)
			}
			assert.Equal(t, tc.wantStatus, statuses)
		})
	}
}

func TestVendorBillInsertsForNoEmailReason(t *testing.T) {
	got := vendorBillInsertsFor([]vendorBillTarget{vbTargetOf(vbDoc("VBIL-1", "", 26))}, vbEnv(9), vbRule())
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Reason)
	assert.Equal(t, reasonNoEmail, *got[0].Reason)
	assert.Nil(t, got[0].Digest)
}

func TestVendorBillInsertsForOffHoursWaitsForWindow(t *testing.T) {
	got := vendorBillInsertsFor([]vendorBillTarget{vbTargetOf(vbDoc("VBIL-1", "o@x.io", 26))}, vbEnv(22), vbRule())
	require.Len(t, got, 1)
	assert.True(t, InSendWindow(got[0].RunAt, vbLoc))
}

func TestVendorBillInsertsForKeysAreStable(t *testing.T) {
	d := vbDoc("VBIL-1", "o@x.io", 26)
	got := vendorBillInsertsFor([]vendorBillTarget{vbTargetOf(d), vbTargetOf(d)}, vbEnv(9), vbRule())
	require.Len(t, got, 2)
	assert.Equal(t, got[0].Key, got[1].Key, "same bill and stage must collide so the insert is idempotent")
}

func TestVendorBillReason(t *testing.T) {
	due := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	base := vendorBillFacts{Status: "APPV", Balance: 100, DueDate: &due}
	mut := func(f func(*vendorBillFacts)) vendorBillFacts { v := base; f(&v); return v }
	moved := due.AddDate(0, 0, 5)
	tests := []struct {
		name string
		f    vendorBillFacts
		want string
	}{
		{"approved and unpaid", base, ""},
		{"partially paid still reminded", mut(func(f *vendorBillFacts) { f.Status = "PART" }), ""},
		{"overdue still reminded", mut(func(f *vendorBillFacts) { f.Status = "ODUE" }), ""},
		{"deleted", mut(func(f *vendorBillFacts) { f.Deleted = true }), reasonVoided},
		{"void", mut(func(f *vendorBillFacts) { f.Status = "VOID" }), reasonVoided},
		{"paid status", mut(func(f *vendorBillFacts) { f.Status = "PAID" }), reasonPaid},
		{"fully covered by credit", mut(func(f *vendorBillFacts) { f.Balance = 0 }), reasonPaid},
		{"payment in flight", mut(func(f *vendorBillFacts) { f.Status = "PINI" }), reasonPaymentInitiated},
		{"draft not approved", mut(func(f *vendorBillFacts) { f.Status = "DRFT" }), reasonNotPayable},
		{"due date moved", mut(func(f *vendorBillFacts) { f.DueDate = &moved }), reasonAnchorChanged},
		{"due date cleared", mut(func(f *vendorBillFacts) { f.DueDate = nil }), reasonAnchorChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, vendorBillReason(tc.f, due)) })
	}
}

func TestRenderVendorBillDigest(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, vbLoc)
	late := vbDoc("VBIL-2", "o@x.io", 25)
	early := vbDoc("VBIL-1", "o@x.io", 21)
	early.Route = "/purchasing/vendor-bills/x"
	in := []VendorBillDoc{late, early}
	e := renderVendorBillDigest("Acme Stone", in, today, vendorBillOwnerReason)
	assert.Equal(t, []VendorBillDoc{late, early}, in, "input must not be reordered")
	require.Len(t, e.Details, 2)
	assert.Equal(t, "Vendor bill VBIL-1", e.Details[0].Label, "earliest due first")
	assert.Contains(t, e.Details[0].Value, "2 days overdue")
	assert.Contains(t, e.Details[0].Value, "Granite Co")
	assert.Contains(t, e.Details[1].Value, "due in 2 days")
	assert.Equal(t, vendorBillTitle, e.Heading)
}

func TestVendorBillTimingAndBody(t *testing.T) {
	assert.Equal(t, "due today", vendorBillTiming(0))
	assert.Equal(t, "due in 1 day", vendorBillTiming(1))
	assert.Equal(t, "1 day overdue", vendorBillTiming(-1))
	assert.Equal(t, "1 vendor bill needs payment.", vendorBillBody(1))
	assert.Equal(t, "4 vendor bills need payment.", vendorBillBody(4))
}

func TestDefaultJobsIncludesVendorBillReminders(t *testing.T) {
	var names []string
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, vendorBillJobName)
}

func TestVendorBillReasonFor(t *testing.T) {
	mine := VendorBillDoc{OwnerUserID: "u1"}
	unowned := VendorBillDoc{}
	assert.Equal(t, vendorBillOwnerReason, vendorBillReasonFor([]VendorBillDoc{mine}, "u1"))
	assert.Equal(t, vendorBillApproverReason, vendorBillReasonFor([]VendorBillDoc{unowned}, "u1"), "an unowned bill reaches approvers")
	assert.Equal(t, vendorBillApproverReason, vendorBillReasonFor([]VendorBillDoc{mine, unowned}, "u1"), "any bill they do not own makes it an approver notice")
}
