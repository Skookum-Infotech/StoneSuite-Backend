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
// working statuses, Pending Conversion only from them) and the Customer flow
// (Draft -> Active, then Credit Hold / Inactive and back).
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
		{"qualified lead cannot jump to a customer status", "LEAD", "LQUA", "CUST", "CACT", false},

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
		{"prospect cannot be moved into a customer status", "PROS", "PPCV", "CUST", "CACT", false},
		{"prospect cannot enter customer at draft", "PROS", "PDIS", "CUST", "CDRF", false},
		{"prospect cannot fall back to a lead status", "PROS", "PDIS", "LEAD", "LQUA", false},

		// Customer: Draft -> Active. Active <-> Credit Hold, Active <-> Inactive,
		// Credit Hold -> Inactive. Nothing returns to Draft.
		{"draft customer -> active", "CUST", "CDRF", "CUST", "CACT", true},
		{"customer with no status is treated as draft", "CUST", "", "CUST", "CACT", true},
		{"draft customer cannot go straight to inactive", "CUST", "CDRF", "CUST", "CINA", false},
		{"draft customer cannot go straight to credit hold", "CUST", "CDRF", "CUST", "CCHD", false},
		{"active -> credit hold", "CUST", "CACT", "CUST", "CCHD", true},
		{"active -> inactive", "CUST", "CACT", "CUST", "CINA", true},
		{"credit hold -> active (release hold)", "CUST", "CCHD", "CUST", "CACT", true},
		{"credit hold -> inactive", "CUST", "CCHD", "CUST", "CINA", true},
		{"inactive -> active", "CUST", "CINA", "CUST", "CACT", true},
		{"inactive cannot go straight to credit hold", "CUST", "CINA", "CUST", "CCHD", false},
		{"customer cannot return to draft", "CUST", "CACT", "CUST", "CDRF", false},
		{"inactive customer cannot return to draft", "CUST", "CINA", "CUST", "CDRF", false},
		{"customer -> its own current status", "CUST", "CACT", "CUST", "CACT", false},
		{"customer cannot fall back to a prospect status", "CUST", "CACT", "PROS", "PDIS", false},
		{"the retired closed won status is not a target", "CUST", "CACT", "CUST", "CCLW", false},
		{"the retired closed lost status is not a target", "CUST", "CACT", "CUST", "CCLL", false},
		{"the retired renewal status is not a target", "CUST", "CACT", "CUST", "CREN", false},
		{"a customer in a retired status has no way on", "CUST", "CCLW", "CUST", "CACT", false},
		{"a stage nobody defined allows nothing", "XXXX", "AAAA", "XXXX", "BBBB", false},
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
		{StateID: "9", StateKey: "CACT", WorkflowKey: "customer"},
		{StateID: "10", StateKey: "CINA", WorkflowKey: "customer"},
		{StateID: "11", StateKey: "CCHD", WorkflowKey: "customer"},
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
		// Every customer status is set by a Quick Action button, so the dropdown
		// lists none of them and the status shows as a plain pill.
		{"a draft customer offers nothing", "CUST", "CDRF", []string{}},
		{"an active customer offers nothing", "CUST", "CACT", []string{}},
		{"a customer on credit hold offers nothing", "CUST", "CCHD", []string{}},
		{"an inactive customer offers nothing", "CUST", "CINA", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterCRMTargets(tc.curType, tc.curStatus, all)
			assert.NotNil(t, got, "must serialise as [] not null")
			assert.Equal(t, tc.want, keys(got))
		})
	}
}

// TestActionOnlyStatusesAreAcceptedButNeverListed: a button (Pending Conversion,
// or a customer's Make Active / Make Inactive / Credit Hold) goes through
// TransitionRecord, so an action-only status has to pass the predicate, yet the
// status dropdown must never list it.
func TestActionOnlyStatusesAreAcceptedButNeverListed(t *testing.T) {
	// One status each button is pressed from, per action-only status.
	from := map[string]struct{ stage, key, status string }{
		statusProspectPendingConversion: {"PROS", "prospect", statusProspectInDiscussion},
		statusCustomerActive:            {"CUST", "customer", statusCustomerDraft},
		statusCustomerInactive:          {"CUST", "customer", statusCustomerActive},
		statusCustomerCreditHold:        {"CUST", "customer", statusCustomerActive},
	}
	require.Len(t, from, len(crmActionOnlyStatuses), "every action-only status needs a source in this test")
	for code := range crmActionOnlyStatuses {
		src, ok := from[code]
		require.Truef(t, ok, "no source status for %s", code)
		t.Run(code, func(t *testing.T) {
			candidate := []workflow.StatusInfo{{StateID: "1", StateKey: code, WorkflowKey: src.key}}
			assert.Empty(t, filterCRMTargets(src.stage, src.status, candidate), "must not be listed")
			assert.True(t, crmTransitionAllowed(src.stage, src.status, src.stage, code), "must stay a legal target")
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
		{"a status named closed is terminal by name", "CUST", "XCLS", "Closed Something", true},
		{"prospect in discussion has moves", "PROS", "PDIS", "In Discussion", false},
		{"pending conversion is not an end point", "PROS", "PPCV", "Pending Conversion", false},
		{"new prospect has moves", "PROS", "PNEW", "New", false},
		{"customer draft has moves", "CUST", "CDRF", "Draft", false},
		{"active customer has moves", "CUST", "CACT", "Active", false},
		{"inactive customer can be made active again", "CUST", "CINA", "Inactive", false},
		{"customer on credit hold can be released", "CUST", "CCHD", "Credit Hold", false},
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
		{"a stage with no rule converts from any status", "CUST", "CACT", ""},
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
	for stage, code := range crmApprovedStatusCode {
		flow := crmStageFlows[stage]
		assert.Truef(t, flow.Known(code), "stage %s: the status approval settles in (%s) is not in its flow", stage, code)
	}
	for stage, code := range crmRejectedStatusCode {
		flow := crmStageFlows[stage]
		assert.Truef(t, flow.Known(code), "stage %s: the status rejection settles in (%s) is not in its flow", stage, code)
		assert.Equalf(t, crmInitialStatusCode[stage], code, "stage %s: a rejected record goes back to where it started", stage)
	}
	for stage, code := range crmEditedStatusCode {
		assert.Truef(t, crmStageFlows[stage].Known(code), "stage %s: the status an edit resets to (%s) is not in its flow", stage, code)
	}
	assert.Equal(t, "CACT", statusCustomerActive, "workflow.CustomerStatusActive is what makes a customer usable on other records")
}

// TestApprovalSettlesCustomerStatus: approving a customer makes it Active, the one
// status it can be used in, and rejecting sends it back to Draft. A rejected
// customer that is edited and approved must therefore be able to come out
// Active, and no other stage has its status moved by approval.
func TestApprovalSettlesCustomerStatus(t *testing.T) {
	assert.Equal(t, map[string]string{"CUST": "CACT"}, crmApprovedStatusCode)
	assert.Equal(t, map[string]string{"CUST": "CDRF"}, crmRejectedStatusCode)
	assert.Empty(t, crmApprovedStatusCode["LEAD"])
	assert.Empty(t, crmApprovedStatusCode["PROS"])
	assert.Empty(t, crmRejectedStatusCode["LEAD"])
	assert.Empty(t, crmRejectedStatusCode["PROS"])
	// The Draft a rejection returns to is the same Draft a customer is created in,
	// and leaves only by being made Active.
	assert.True(t, crmTransitionAllowed("CUST", crmRejectedStatusCode["CUST"], "CUST", crmApprovedStatusCode["CUST"]))
}

// TestSettledStatusSQL: the approve and reject UPDATEs share one expression, so
// it must stay scoped to the record's own stage, skip retired statuses, and fall
// back to the record's current status when the bound code is empty.
func TestSettledStatusSQL(t *testing.T) {
	sql := settledStatusSQL("$3")
	assert.Contains(t, sql, "cs.crm_status_code = $3")
	assert.Contains(t, sql, "cs.crm_status_record_type = customer.record_type")
	assert.Contains(t, sql, "cs.crm_status_deleted_at IS NULL")
	assert.Contains(t, sql, "customer_crm_status)", "must fall back to the current status")
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
	for stage, code := range crmApprovedStatusCode {
		needed = append(needed, codeInStage{code, stage})
	}
	for stage, code := range crmRejectedStatusCode {
		needed = append(needed, codeInStage{code, stage})
	}
	for stage, code := range crmEditedStatusCode {
		needed = append(needed, codeInStage{code, stage})
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

// TestCustomerStatusRework guards the customer status rework from both sides, as
// TestProspectStatusRename does for the prospect one: the seed carries the new
// statuses and no longer the retired ones (a fresh tenant), and a migration moves
// customers off the retired ones and hides them (a tenant seeded before). If the
// two disagree, fresh and existing tenants end up with different statuses.
func TestCustomerStatusRework(t *testing.T) {
	raw, err := os.ReadFile("../database/migrations/tenant/schema.sql")
	require.NoError(t, err)
	schema := string(raw)
	seed := regexp.MustCompile(`(?s)INSERT INTO lkp_crm_status\b.*?ON CONFLICT`).FindString(schema)
	require.NotEmpty(t, seed, "the lkp_crm_status seed block was not found in schema.sql")

	for code, name := range map[string]string{"CACT": "Active", "CINA": "Inactive", "CCHD": "Credit Hold"} {
		assert.Regexpf(t, fmt.Sprintf(`\('%s',\s*'%s',\s*3,`, code, name), seed, "%s (%s) must be seeded for customers", name, code)
	}
	for _, retired := range []string{"CCLW", "CCLL", "CREN"} {
		assert.NotContainsf(t, seed, "'"+retired+"'", "%s is retired and must not be seeded", retired)
		assert.Containsf(t, schema, "'"+retired+"'", "%s must still be named by the migration that retires it", retired)
	}

	// Which status each customer lands in.
	assert.Contains(t, schema, "WHEN cu.customer_approval_status IN ('pending', 'rejected') THEN 'CDRF'",
		"a customer still in the approval process must become Draft, not Active")
	assert.Contains(t, schema, "WHEN rs.crm_status_code = 'CCLL' THEN 'CINA'", "Closed Lost becomes Inactive")
	assert.Contains(t, schema, "ELSE 'CACT' END", "Closed Won and Renewal become Active")
	assert.Contains(t, schema, "AND t.target_id IS NOT NULL",
		"a missing target status must leave the customer alone rather than clear its status")

	// Retiring is a soft delete, and only once nothing points at the status.
	assert.Contains(t, schema, "SET crm_status_is_active = FALSE, crm_status_deleted_at = NOW()")
	assert.Contains(t, schema, "AND NOT EXISTS (SELECT 1 FROM customer c WHERE c.customer_crm_status = cs.crm_status_id)")
}

// TestEditedStatusReset: an edit sends a customer back to Draft, from whatever
// status it was in, but only when something actually changed, and never for a
// lead or prospect.
func TestEditedStatusReset(t *testing.T) {
	tests := []struct {
		name          string
		typeCode, cur string
		changed       bool
		want          string // "" means the status is kept
	}{
		{"an edited active customer goes back to draft", "CUST", "CACT", true, "CDRF"},
		{"an edited customer on credit hold goes back to draft", "CUST", "CCHD", true, "CDRF"},
		{"an edited inactive customer goes back to draft", "CUST", "CINA", true, "CDRF"},
		{"an edited customer with no status goes to draft", "CUST", "", true, "CDRF"},
		{"an already-draft customer stays where it is", "CUST", "CDRF", true, ""},
		{"saving an untouched form is not an edit", "CUST", "CACT", false, ""},
		{"an edited lead keeps its status", "LEAD", "LQUA", true, ""},
		{"an edited prospect keeps its status", "PROS", "PDIS", true, ""},
		{"an edited pending-conversion prospect keeps its status", "PROS", "PPCV", true, ""},
		{"a stage nobody defined keeps its status", "XXXX", "AAAA", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, editedStatusReset(tc.typeCode, tc.cur, tc.changed))
		})
	}
}

// TestEditedStatusIsWhereApprovalStarts: the status an edit sends a customer to
// is the one it is created in, and from it approval (or Make Active) can make it
// usable again -- an edit must never strand a customer.
func TestEditedStatusIsWhereApprovalStarts(t *testing.T) {
	assert.Equal(t, map[string]string{"CUST": "CDRF"}, crmEditedStatusCode)
	assert.Equal(t, crmInitialStatusCode["CUST"], crmEditedStatusCode["CUST"])
	assert.Equal(t, crmRejectedStatusCode["CUST"], crmEditedStatusCode["CUST"], "an edit and a rejection both land in the same place")
	assert.True(t, crmTransitionAllowed("CUST", crmEditedStatusCode["CUST"], "CUST", crmApprovedStatusCode["CUST"]))
	assert.True(t, crmStageFlows["CUST"].Known(crmEditedStatusCode["CUST"]))
}

// TestCRMFieldsChanged decides whether a save is an edit. It compares fields the
// way they are stored, so a form that sends back what it loaded -- with blanks
// for unset fields and numbers as strings -- is not a change.
func TestCRMFieldsChanged(t *testing.T) {
	stored := map[string]any{
		"customer_name":         "Acme",
		"customer_credit_limit": "1000.00",
		"customer_type":         "3",
		"customer_is_child":     false,
	}
	tests := []struct {
		name                      string
		beforeCore, afterCore     map[string]any
		beforeCustom, afterCustom map[string]any
		want                      bool
	}{
		{"identical", stored, stored, nil, nil, false},
		{"an unset field sent back blank", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": "1000.00", "customer_type": "3", "customer_is_child": false,
			"customer_dba_name": "", "customer_lead_score": "", "customer_expected_close_date": "",
		}, nil, nil, false},
		{"a number sent back as a JSON number", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": float64(1000), "customer_type": float64(3), "customer_is_child": false,
		}, nil, nil, false},
		{"a field the registry does not store is ignored", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": "1000.00", "customer_type": "3", "customer_is_child": false,
			"approval_status": "approved",
		}, nil, nil, false},
		{"a text field changed", stored, map[string]any{
			"customer_name": "Acme Inc", "customer_credit_limit": "1000.00", "customer_type": "3", "customer_is_child": false,
		}, nil, nil, true},
		{"a number changed", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": "2500", "customer_type": "3", "customer_is_child": false,
		}, nil, nil, true},
		{"a lookup changed", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": "1000.00", "customer_type": "4", "customer_is_child": false,
		}, nil, nil, true},
		{"a checkbox changed", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": "1000.00", "customer_type": "3", "customer_is_child": true,
		}, nil, nil, true},
		{"a field was filled in", stored, map[string]any{
			"customer_name": "Acme", "customer_credit_limit": "1000.00", "customer_type": "3", "customer_is_child": false,
			"customer_dba_name": "Acme Stone",
		}, nil, nil, true},
		{"a field was cleared", stored, map[string]any{
			"customer_name": "", "customer_credit_limit": "1000.00", "customer_type": "3", "customer_is_child": false,
		}, nil, nil, true},
		{"no custom fields either side", stored, stored, nil, map[string]any{}, false},
		{"same custom fields", stored, stored, map[string]any{"finish": "polished", "grade": float64(2)}, map[string]any{"grade": float64(2), "finish": "polished"}, false},
		{"a custom field changed", stored, stored, map[string]any{"finish": "polished"}, map[string]any{"finish": "honed"}, true},
		{"a custom field added", stored, stored, nil, map[string]any{"finish": "honed"}, true},
		{"a custom field removed", stored, stored, map[string]any{"finish": "honed"}, map[string]any{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crmFieldsChanged(tc.beforeCore, tc.afterCore, tc.beforeCustom, tc.afterCustom))
		})
	}
}
