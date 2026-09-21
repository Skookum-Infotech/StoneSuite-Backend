package crmstore

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/workflow"
)

// TestCRMTransitionAllowed is the full status-move matrix: the Lead flow (New ->
// Qualified | Unqualified, both final), the Prospect flow (free between its
// working statuses, Pending Conversion only from them) and the free-form
// Customer rule (any own-stage status except the current one and Draft).
func TestCRMTransitionAllowed(t *testing.T) {
	tests := []struct {
		name                     string
		curType, curStatus       string
		targetType, targetStatus string
		want                     bool
	}{
		// Lead: New -> Qualified | Unqualified, both final.
		{"new lead -> qualified", "LEAD", "LNEW", "LEAD", "LQUA", true},
		{"new lead -> unqualified", "LEAD", "LNEW", "LEAD", "LUNQ", true},
		{"lead with no status is treated as new", "LEAD", "", "LEAD", "LQUA", true},
		{"qualified is final: -> unqualified", "LEAD", "LQUA", "LEAD", "LUNQ", false},
		{"unqualified is final: -> qualified", "LEAD", "LUNQ", "LEAD", "LQUA", false},
		{"qualified cannot return to new", "LEAD", "LQUA", "LEAD", "LNEW", false},
		{"new lead -> new (same status)", "LEAD", "LNEW", "LEAD", "LNEW", false},
		{"new lead cannot skip to a prospect status", "LEAD", "LNEW", "PROS", "PDIS", false},
		{"qualified lead cannot jump to a customer status", "LEAD", "LQUA", "CUST", "CCLW", false},

		// Prospect: New -> a working status or Lost. Working statuses move freely
		// and reach Lost or Pending Conversion. Lost reopens to a working status.
		// Pending Conversion goes back to a working status or Lost. Nothing returns
		// to New and nothing leaves the stage.
		{"new prospect -> in discussion", "PROS", "PNEW", "PROS", "PDIS", true},
		{"new prospect -> contacted", "PROS", "PNEW", "PROS", "PPUR", true},
		{"new prospect -> lost", "PROS", "PNEW", "PROS", "PCLL", true},
		{"new prospect cannot skip to pending conversion", "PROS", "PNEW", "PROS", "PPCV", false},
		{"prospect with no status is treated as new", "PROS", "", "PROS", "PDIS", true},
		{"prospect with no status cannot skip to pending conversion", "PROS", "", "PROS", "PPCV", false},
		{"working status -> another working status", "PROS", "PDIS", "PROS", "PNEG", true},
		{"working status can move backwards too", "PROS", "PIDM", "PROS", "PPRP", true},
		{"working status -> lost", "PROS", "PNEG", "PROS", "PCLL", true},
		{"working status -> pending conversion", "PROS", "PPRP", "PROS", "PPCV", true},
		{"working status -> its own current status", "PROS", "PDIS", "PROS", "PDIS", false},
		{"working status cannot return to new", "PROS", "PDIS", "PROS", "PNEW", false},
		{"lost is reopened to a working status", "PROS", "PCLL", "PROS", "PDIS", true},
		{"lost cannot go straight to pending conversion", "PROS", "PCLL", "PROS", "PPCV", false},
		{"pending conversion goes back to a working status", "PROS", "PPCV", "PROS", "PNEG", true},
		{"pending conversion -> lost", "PROS", "PPCV", "PROS", "PCLL", true},
		{"pending conversion cannot return to new", "PROS", "PPCV", "PROS", "PNEW", false},
		{"pending conversion -> itself", "PROS", "PPCV", "PROS", "PPCV", false},
		{"prospect cannot be moved into a customer status", "PROS", "PPCV", "CUST", "CCLW", false},
		{"prospect cannot enter customer at draft", "PROS", "PDIS", "CUST", "CDRF", false},
		{"prospect cannot fall back to a lead status", "PROS", "PDIS", "LEAD", "LQUA", false},

		// Customer
		{"draft customer -> closed won", "CUST", "CDRF", "CUST", "CCLW", true},
		{"closed won -> renewal", "CUST", "CCLW", "CUST", "CREN", true},
		{"renewal -> closed won", "CUST", "CREN", "CUST", "CCLW", true},
		{"customer -> closed lost", "CUST", "CCLW", "CUST", "CCLL", true},
		{"customer cannot return to draft", "CUST", "CCLW", "CUST", "CDRF", false},
		{"customer -> its own current status", "CUST", "CCLW", "CUST", "CCLW", false},
		{"customer cannot fall back to a prospect status", "CUST", "CCLW", "PROS", "PDIS", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crmTransitionAllowed(tc.curType, tc.curStatus, tc.targetType, tc.targetStatus))
		})
	}
}

// TestProspectWorkingStatusesMoveFreely pins the requirement itself, so a change
// to the flow table cannot quietly break it: every working status reaches every
// other one, Lost and Pending Conversion, and none goes back to New.
func TestProspectWorkingStatusesMoveFreely(t *testing.T) {
	require.NotEmpty(t, prospectWorking)
	for _, from := range prospectWorking {
		for _, to := range prospectWorking {
			assert.Equalf(t, from != to, crmTransitionAllowed("PROS", from, "PROS", to), "%s -> %s", from, to)
		}
		for _, to := range []string{statusProspectLost, statusProspectPendingConversion} {
			assert.Truef(t, crmTransitionAllowed("PROS", from, "PROS", to), "%s -> %s", from, to)
		}
		assert.Falsef(t, crmTransitionAllowed("PROS", from, "PROS", statusProspectNew), "%s -> New", from)
	}
}

// TestFilterCRMTargets checks what the status dropdown is handed: exactly the
// moves crmTransitionAllowed permits, minus the action-only statuses, and never
// nil. The candidate list mirrors what AvailableTransitions passes in -- the
// record's own stage AND every later one -- so it also proves a prospect is
// never offered a customer status.
func TestFilterCRMTargets(t *testing.T) {
	all := []workflow.StatusInfo{
		{StateID: "12", StateKey: "LNEW", WorkflowKey: "lead"},
		{StateID: "1", StateKey: "LQUA", WorkflowKey: "lead"},
		{StateID: "2", StateKey: "LUNQ", WorkflowKey: "lead"},
		{StateID: "13", StateKey: "PNEW", WorkflowKey: "prospect"},
		{StateID: "3", StateKey: "PDIS", WorkflowKey: "prospect"},
		{StateID: "4", StateKey: "PNEG", WorkflowKey: "prospect"},
		{StateID: "8", StateKey: "PCLL", WorkflowKey: "prospect"},
		{StateID: "15", StateKey: "PPCV", WorkflowKey: "prospect"},
		{StateID: "14", StateKey: "CDRF", WorkflowKey: "customer"},
		{StateID: "9", StateKey: "CCLW", WorkflowKey: "customer"},
	}
	keys := func(in []workflow.StatusInfo) []string {
		out := make([]string, 0, len(in))
		for _, s := range in {
			out = append(out, s.StateKey)
		}
		return out
	}
	tests := []struct {
		name               string
		curType, curStatus string
		want               []string
	}{
		{"new lead offers only qualified and unqualified", "LEAD", "LNEW", []string{"LQUA", "LUNQ"}},
		{"qualified lead offers nothing", "LEAD", "LQUA", []string{}},
		{"unqualified lead offers nothing", "LEAD", "LUNQ", []string{}},
		{"new prospect offers the working statuses and lost, not pending conversion", "PROS", "PNEW", []string{"PDIS", "PNEG", "PCLL"}},
		{"a working prospect offers the others and lost, not pending conversion", "PROS", "PDIS", []string{"PNEG", "PCLL"}},
		{"pending conversion can be undone from the dropdown", "PROS", "PPCV", []string{"PDIS", "PNEG", "PCLL"}},
		{"a lost prospect can be reopened", "PROS", "PCLL", []string{"PDIS", "PNEG"}},
		{"customer offers nothing when only entry and current remain", "CUST", "CCLW", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterCRMTargets(tc.curType, tc.curStatus, all)
			assert.NotNil(t, got, "must serialise as [] not null")
			assert.Equal(t, tc.want, keys(got))
		})
	}
}

// TestActionOnlyStatusesAreAcceptedButNeverListed: the header button goes
// through TransitionRecord, so an action-only status has to pass the predicate,
// yet the status dropdown must never list it.
func TestActionOnlyStatusesAreAcceptedButNeverListed(t *testing.T) {
	require.NotEmpty(t, crmActionOnlyStatuses)
	for code := range crmActionOnlyStatuses {
		t.Run(code, func(t *testing.T) {
			candidate := []workflow.StatusInfo{{StateID: "1", StateKey: code, WorkflowKey: "prospect"}}
			assert.Empty(t, filterCRMTargets("PROS", statusProspectInDiscussion, candidate), "must not be listed")
			assert.True(t, crmTransitionAllowed("PROS", statusProspectInDiscussion, "PROS", code), "must stay a legal target")
		})
	}
}

// TestCRMStatusIsTerminal covers the pill's confirm-click rule: the original
// "Closed ..." name rule, a lost prospect by code, plus any status a stage flow
// gives no way out of.
func TestCRMStatusIsTerminal(t *testing.T) {
	tests := []struct {
		name       string
		typeCode   string
		code       string
		statusName string
		want       bool
	}{
		{"lead qualified is final", "LEAD", "LQUA", "Lead Qualified", true},
		{"lead unqualified is final", "LEAD", "LUNQ", "Lead Unqualified", true},
		{"lead new has moves", "LEAD", "LNEW", "New", false},
		{"a lost prospect is terminal by code, whatever it is called", "PROS", "PCLL", "Lost", true},
		{"closed won by name", "CUST", "CCLW", "Customer Closed Won", true},
		{"customer closed lost by name", "CUST", "CCLL", "Customer Closed Lost", true},
		{"prospect in discussion has moves", "PROS", "PDIS", "In Discussion", false},
		{"pending conversion is not an end point", "PROS", "PPCV", "Pending Conversion", false},
		{"new prospect has moves", "PROS", "PNEW", "New", false},
		{"customer renewal has moves", "CUST", "CREN", "Customer Renewal", false},
		{"customer draft has moves", "CUST", "CDRF", "Draft", false},
		{"a code the lead flow does not know is not terminal", "LEAD", "XXXX", "Something", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crmStatusIsTerminal(tc.typeCode, tc.code, tc.statusName))
		})
	}
}

// TestCheckConvertFrom: only a Qualified lead and a Pending Conversion prospect
// may be converted; a stage with no requirement converts from any status.
func TestCheckConvertFrom(t *testing.T) {
	tests := []struct {
		name       string
		typeCode   string
		statusCode string
		wantMsg    string // "" means the conversion is allowed
	}{
		{"qualified lead may convert", "LEAD", "LQUA", ""},
		{"new lead may not", "LEAD", "LNEW", msgConvertNeedsQualified},
		{"unqualified lead may not", "LEAD", "LUNQ", msgConvertNeedsQualified},
		{"lead with no status may not", "LEAD", "", msgConvertNeedsQualified},
		{"pending conversion prospect may convert", "PROS", "PPCV", ""},
		{"working prospect may not", "PROS", "PDIS", msgConvertNeedsPendingConversion},
		{"new prospect may not", "PROS", "PNEW", msgConvertNeedsPendingConversion},
		{"lost prospect may not", "PROS", "PCLL", msgConvertNeedsPendingConversion},
		{"prospect with no status may not", "PROS", "", msgConvertNeedsPendingConversion},
		{"a stage with no rule converts from any status", "CUST", "CCLW", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkConvertFrom(tc.typeCode, tc.statusCode)
			if tc.wantMsg == "" {
				assert.NoError(t, err)
				return
			}
			var ce ClientError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, tc.wantMsg, ce.Msg)
			assert.True(t, IsClientError(err), "must surface as a 400")
		})
	}
}

// TestCRMStatusRuleTablesAreConsistent guards the rule tables against an edit
// that leaves one contradicting another.
func TestCRMStatusRuleTablesAreConsistent(t *testing.T) {
	require.Len(t, crmInitialStatusCode, len(crmKeyToCode), "every stage needs exactly one initial status")
	for _, stage := range crmKeyToCode {
		initial, ok := crmInitialStatusCode[stage]
		require.Truef(t, ok, "stage %s has no initial status", stage)
		assert.Contains(t, crmInitialStatusCodes(), initial)
	}
	for stage, flow := range crmStageFlows {
		initial := crmInitialStatusCode[stage]
		assert.NotEmptyf(t, flow[initial], "stage %s: the flow must start at its initial status %s", stage, initial)
		for from, targets := range flow {
			assert.Falsef(t, targets[initial], "stage %s: %s -> %s targets the entry-only initial status", stage, from, initial)
		}
	}
	for stage, rule := range crmConvertRules {
		assert.NotEmptyf(t, rule.msg, "stage %s: a convert rule needs the message it shows", stage)
		if flow, ok := crmStageFlows[stage]; ok {
			assert.Truef(t, flow.Known(rule.from), "stage %s: convert-from status %s is not in its flow", stage, rule.from)
		}
	}
	for code := range crmActionOnlyStatuses {
		assert.NotContainsf(t, crmInitialStatusCodes(), code, "%s is entry-only, so it cannot also be an action-only target", code)
	}
}

// TestCRMStatusRulesAreSeeded guards the rules against the seed: every status
// code they key on must be seeded in lkp_crm_status under the stage the rules
// expect. A missing initial status would make every create fail at runtime; a
// flow pointing at an unseeded status would silently offer nothing.
func TestCRMStatusRulesAreSeeded(t *testing.T) {
	raw, err := os.ReadFile("../database/migrations/tenant/schema.sql")
	require.NoError(t, err)
	block := regexp.MustCompile(`(?s)INSERT INTO lkp_crm_status\b.*?ON CONFLICT`).FindString(string(raw))
	require.NotEmpty(t, block, "the lkp_crm_status seed block was not found in schema.sql")

	// lkp_record_type seed order: LEAD, PROS, CUST.
	stageTypeID := map[string]int{"LEAD": 1, "PROS": 2, "CUST": 3}

	type codeInStage struct{ code, stage string }
	var needed []codeInStage
	for stage, code := range crmInitialStatusCode {
		needed = append(needed, codeInStage{code, stage})
	}
	for stage, flow := range crmStageFlows {
		for from, targets := range flow {
			needed = append(needed, codeInStage{from, stage})
			for to := range targets {
				needed = append(needed, codeInStage{to, stage})
			}
		}
	}
	for stage, rule := range crmConvertRules {
		needed = append(needed, codeInStage{rule.from, stage})
	}

	for _, n := range needed {
		pattern := fmt.Sprintf(`\('%s',\s*'[^']+',\s*%d,`, n.code, stageTypeID[n.stage])
		assert.Regexpf(t, pattern, block, "status %s is not seeded under stage %s", n.code, n.stage)
	}
}

// TestProspectStatusRename guards the prospect rename from both sides: the seed
// carries the new names (a fresh tenant) and a guarded UPDATE renames the old
// ones (a tenant seeded before the rename). If the two ever disagree, fresh and
// existing tenants end up with different labels.
func TestProspectStatusRename(t *testing.T) {
	raw, err := os.ReadFile("../database/migrations/tenant/schema.sql")
	require.NoError(t, err)
	schema := string(raw)
	seed := regexp.MustCompile(`(?s)INSERT INTO lkp_crm_status\b.*?ON CONFLICT`).FindString(schema)
	require.NotEmpty(t, seed, "the lkp_crm_status seed block was not found in schema.sql")

	renames := []struct{ code, oldName, newName string }{
		{"PDIS", "Prospect In Discussion", "In Discussion"},
		{"PNEG", "Prospect In Negotiation", "In Negotiation"},
		{"PPRP", "Prospect Proposal", "Proposal Sent"},
		{"PIDM", "Prospect Identified Decision Makers", "Decision Pending"},
		{"PPUR", "Prospect Purchasing", "Contacted"},
		{"PCLL", "Prospect Closed Lost", "Lost"},
	}
	for _, r := range renames {
		t.Run(r.code, func(t *testing.T) {
			seeded := fmt.Sprintf(`\('%s',\s*'%s',\s*2,`, r.code, regexp.QuoteMeta(r.newName))
			assert.Regexp(t, seeded, seed, "the seed must carry the new name")
			assert.NotContains(t, seed, "'"+r.oldName+"'", "the seed must not carry the old name")
			renamed := fmt.Sprintf(`\('%s',\s*'%s',\s*'%s'\)`, r.code, regexp.QuoteMeta(r.oldName), regexp.QuoteMeta(r.newName))
			assert.Regexp(t, renamed, schema, "the UPDATE must rename old -> new")
		})
	}
	assert.Contains(t, schema, "AND cs.crm_status_name = r.old_name",
		"the rename must match on the old name so it runs once and never overwrites a later rename")
}
