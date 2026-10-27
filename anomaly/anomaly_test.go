package anomaly

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/services"
)

func TestStatusValid(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusOpen, true},
		{StatusReviewed, true},
		{StatusDismissed, true},
		{StatusConfirmed, true},
		{Status(""), false},
		{Status("OPEN"), false},
		{Status("resolved"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.status.Valid())
		})
	}
}

func TestFindingValidate(t *testing.T) {
	good := Finding{
		RuleKey: RuleSelfApproval, SubjectKey: "expense:abc:7", Severity: SeverityWarning,
		Resource: "expense", Title: "Expense EXP-1 was approved by its own owner",
	}
	tests := []struct {
		name    string
		mutate  func(*Finding)
		wantErr error
	}{
		{"valid", func(*Finding) {}, nil},
		{"empty rule key", func(f *Finding) { f.RuleKey = "" }, errFindingRuleKey},
		{"long rule key", func(f *Finding) { f.RuleKey = strings.Repeat("r", maxRuleKeyLen+1) }, errFindingRuleKey},
		{"empty subject key", func(f *Finding) { f.SubjectKey = "" }, errFindingSubjectKey},
		{"long subject key", func(f *Finding) { f.SubjectKey = strings.Repeat("s", maxSubjectKeyLen+1) }, errFindingSubjectKey},
		{"blank title", func(f *Finding) { f.Title = "   " }, errFindingTitle},
		{"empty resource", func(f *Finding) { f.Resource = "" }, errFindingResource},
		{"unknown severity", func(f *Finding) { f.Severity = "fatal" }, errFindingSeverity},
		{"max length subject key", func(f *Finding) { f.SubjectKey = strings.Repeat("s", maxSubjectKeyLen) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := good
			tt.mutate(&f)
			err := f.validate()
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestClip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short unchanged", "abc", 5, "abc"},
		{"exact unchanged", "abcde", 5, "abcde"},
		{"long cut with ellipsis", "abcdefgh", 5, "abcd" + ellipsis},
		{"counts runes not bytes", "ééééééé", 4, "ééé" + ellipsis},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, clip(tt.in, tt.max))
		})
	}
}

func TestMergeRecipients(t *testing.T) {
	tests := []struct {
		name    string
		exclude string
		lists   [][]string
		want    []string
	}{
		{"union sorted and de-duplicated", "", [][]string{{"b", "a"}, {"a", "c"}}, []string{"a", "b", "c"}},
		{"drops the involved person", "b", [][]string{{"a", "b"}, {"b", "c"}}, []string{"a", "c"}},
		{"drops blanks", "", [][]string{{"", "a"}}, []string{"a"}},
		{"involved is the only recipient", "a", [][]string{{"a"}}, []string{}},
		{"no lists", "", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, mergeRecipients(tt.exclude, tt.lists...))
		})
	}
}

func TestNullIfEmpty(t *testing.T) {
	assert.Nil(t, nullIfEmpty(""))
	got := nullIfEmpty("x")
	require.NotNil(t, got)
	assert.Equal(t, "x", *got)
}

func TestIdempotencyKey(t *testing.T) {
	a := IdempotencyKey("11111111-1111-1111-1111-111111111111")
	assert.Len(t, a, 64, "notify caps keys at 128 bytes; always 64 hex chars")
	assert.Equal(t, a, IdempotencyKey("11111111-1111-1111-1111-111111111111"), "stable across retries")
	assert.NotEqual(t, a, IdempotencyKey("22222222-2222-2222-2222-222222222222"))
}

func TestBuildNotification(t *testing.T) {
	to := []services.RecipientTarget{{UserID: "u1", Email: "a@example.com", Name: "A"}}
	base := pendingAlert{
		uuid: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", ruleKey: RuleDuplicateVendorBill,
		resource: "vendor_bill", recordUUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		title: "Possible duplicate vendor bill VBIL-2", detail: "Same vendor…",
	}
	tests := []struct {
		name         string
		mutate       func(*pendingAlert)
		wantChannels []string
		wantResource string
	}{
		{"warning is bell only", func(a *pendingAlert) { a.severity = string(SeverityWarning) }, nil, base.recordUUID},
		{"info is bell only", func(a *pendingAlert) { a.severity = string(SeverityInfo) }, nil, base.recordUUID},
		{"critical adds email", func(a *pendingAlert) { a.severity = string(SeverityCritical) }, []string{"email"}, base.recordUUID},
		{"no record falls back to the anomaly id", func(a *pendingAlert) { a.severity = string(SeverityWarning); a.recordUUID = "" }, nil, base.uuid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := base
			tt.mutate(&a)
			req := buildNotification("tenant-1", a, to)
			assert.Equal(t, "tenant-1", req.TenantID)
			assert.Equal(t, "anomaly."+RuleDuplicateVendorBill, req.EventType)
			assert.Equal(t, "vendor_bill", req.Resource, "the bell filters by the record's own RBAC resource")
			assert.Equal(t, tt.wantResource, req.ResourceID)
			assert.Equal(t, tt.wantChannels, req.Channels)
			assert.Equal(t, tt.wantChannels != nil, req.Email != nil, "only an emailed alert carries email content")
			assert.Equal(t, AnomaliesLink, req.Link)
			assert.Equal(t, a.title, req.Title)
			assert.Equal(t, a.detail, req.Body)
			assert.Equal(t, IdempotencyKey(a.uuid), req.IdempotencyKey)
			assert.Equal(t, to, req.Recipients)
		})
	}
}

func TestCursorRoundTrip(t *testing.T) {
	in := Cursor{DetectedAt: "2026-10-27 09:15:30.123456", ID: 42}
	out, err := DecodeCursor(EncodeCursor(in))
	require.NoError(t, err)
	assert.Equal(t, in, *out)
}

func TestDecodeCursorRejects(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name string
		in   string
	}{
		{"not base64", "!!!"},
		{"no separator", enc("2026-10-27 09:15:30.123456")},
		{"bad timestamp", enc("yesterday|5")},
		{"timestamp with sql", enc("2026-10-27'; DROP TABLE anomaly;--|5")},
		{"non numeric id", enc("2026-10-27 09:15:30.123456|x")},
		{"zero id", enc("2026-10-27 09:15:30.123456|0")},
		{"negative id", enc("2026-10-27 09:15:30.123456|-3")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeCursor(tt.in)
			assert.ErrorIs(t, err, ErrBadCursor)
		})
	}
}

func TestSelfApprovalTitle(t *testing.T) {
	assert.Equal(t, "Expense Claim EXP-000001 was approved by its own owner", selfApprovalTitle("Expense Claim", "EXP-000001"))
	assert.Equal(t, "Expense Claim was approved by its own owner", selfApprovalTitle("Expense Claim", ""))
}

func TestSelfApprovalCovers(t *testing.T) {
	covered := func(key string) bool {
		cfg, ok := approvalchain.ForWorkflowKey(key)
		require.True(t, ok, key)
		return selfApprovalCovers(cfg)
	}
	assert.True(t, covered("expense"), "expense is wired for approval notifications")
	assert.True(t, covered("vendor_payment"))
	assert.False(t, selfApprovalCovers(approvalchain.ModuleConfig{}), "an empty config has nothing to scan")
}

// Every module the self-approval scan will touch must name every column it
// interpolates; a registry entry with a blank one would build broken SQL.
func TestSelfApprovalCoveredModulesHaveAllColumns(t *testing.T) {
	covered := 0
	for _, key := range approvalchain.Keys() {
		cfg, ok := approvalchain.ForWorkflowKey(key)
		require.True(t, ok, key)
		if !selfApprovalCovers(cfg) {
			continue
		}
		covered++
		r := cfg.Record
		for name, v := range map[string]string{
			"ApprovalTable": cfg.ApprovalTable, "Table": r.Table, "IDColumn": r.IDColumn,
			"UUIDColumn": r.UUIDColumn, "OwnerColumn": r.OwnerColumn, "NumberColumn": r.NumberColumn,
		} {
			assert.NotEmpty(t, v, "%s.%s", key, name)
		}
	}
	assert.Positive(t, covered)
}

func TestDuplicateBillAndJournalTitles(t *testing.T) {
	assert.Equal(t, "Possible duplicate vendor bill VBIL-000009", duplicateBillTitle("VBIL-000009"))
	assert.Equal(t, "Possible duplicate vendor bill", duplicateBillTitle(""))
	assert.Equal(t, "Journal entry JE-000003 does not balance", unbalancedJournalTitle("JE-000003"))
	assert.Equal(t, "A journal entry does not balance", unbalancedJournalTitle(""))
}

func TestVendorBillIsInTheApprovalRegistry(t *testing.T) {
	cfg, ok := approvalchain.ForWorkflowKey(vendorBillWorkflowKey)
	require.True(t, ok, "DuplicateVendorBill reads its approvers from this registry entry")
	assert.NotEmpty(t, cfg.ApproverTable)
	assert.NotEmpty(t, cfg.Resource)
}

func TestDefaultRulesAreUniquelyKeyed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range DefaultRules() {
		assert.NotEmpty(t, r.Key())
		assert.False(t, seen[r.Key()], "duplicate rule key %s", r.Key())
		seen[r.Key()] = true
	}
	assert.Len(t, seen, len(DefaultRules()), "every rule key is unique")
	assert.GreaterOrEqual(t, len(seen), 40, "the sales, purchasing, inventory, fabrication and finance rules are all registered")
}

func TestSQLRulesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range DefaultRules() {
		key := r.Key()
		assert.False(t, seen[key], "duplicate rule key %s", key)
		seen[key] = true
		assert.LessOrEqual(t, len(key), maxRuleKeyLen, key)
		_, labelled := ruleLabels[key]
		assert.True(t, labelled, "rule %s has no label for the email", key)

		sr, ok := r.(sqlRule)
		if !ok {
			continue // the three hand-written rules
		}
		assert.Equal(t, 1, strings.Count(sr.title, "%s"), "%s: the title needs exactly one %%s for the record's label", key)
		assert.Contains(t, []Severity{SeverityInfo, SeverityWarning, SeverityCritical}, sr.severity, key)
		assert.NotEmpty(t, strings.TrimSpace(sr.query), key)
		assert.True(t, sr.audience.superAdmins || sr.audience.owner || sr.audience.approvers != "",
			"%s would be sent to nobody", key)
		if sr.audience.approvers != "" {
			_, ok := approvalchain.ForWorkflowKey(sr.audience.approvers)
			assert.True(t, ok, "%s names unknown approval module %q", key, sr.audience.approvers)
		}
		// Every bound argument must be used: Postgres rejects a statement whose
		// highest parameter is not referenced.
		last := 3 + len(sr.args)
		assert.Contains(t, sr.query+sqlRuleWrapper, fmt.Sprintf("$%d", last), "%s never uses its last argument $%d", key, last)
	}
}

func TestThresholdsAreSane(t *testing.T) {
	assert.Greater(t, longLookbackDays, lookbackDays, "the state-rule window must be wider than the event-rule window")
	assert.GreaterOrEqual(t, minHistory, 2)
	assert.Greater(t, discountFloorPercent, 0)
}

func TestDiscountThresholdsAreSane(t *testing.T) {
	assert.Greater(t, discountBaselineMinLines, 1)
	assert.Greater(t, discountAlwaysFlagPercent, discountFloorPercent)
	assert.LessOrEqual(t, discountAlwaysFlagPercent, 100)
}
