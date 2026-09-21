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
// Qualified | Unqualified, both final) and the free-form Prospect/Customer rule
// (own or later stage, never back to an entry status, never to the current one).
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

		// Prospect: own or later stage, minus its current status and entry statuses.
		{"new prospect -> in discussion", "PROS", "PNEW", "PROS", "PDIS", true},
		{"prospect sideways within its stage", "PROS", "PDIS", "PROS", "PNEG", true},
		{"prospect -> closed lost", "PROS", "PDIS", "PROS", "PCLL", true},
		{"prospect cannot return to new", "PROS", "PDIS", "PROS", "PNEW", false},
		{"prospect -> its own current status", "PROS", "PDIS", "PROS", "PDIS", false},
		{"prospect advances into a customer status", "PROS", "PPUR", "CUST", "CCLW", true},
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

// TestFilterCRMTargets checks what the status dropdown is handed: it must be
// exactly the moves crmTransitionAllowed permits, and never nil.
func TestFilterCRMTargets(t *testing.T) {
	all := []workflow.StatusInfo{
		{StateID: "12", StateKey: "LNEW", WorkflowKey: "lead"},
		{StateID: "1", StateKey: "LQUA", WorkflowKey: "lead"},
		{StateID: "2", StateKey: "LUNQ", WorkflowKey: "lead"},
		{StateID: "13", StateKey: "PNEW", WorkflowKey: "prospect"},
		{StateID: "3", StateKey: "PDIS", WorkflowKey: "prospect"},
		{StateID: "8", StateKey: "PCLL", WorkflowKey: "prospect"},
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
		{"new prospect offers every working status", "PROS", "PNEW", []string{"PDIS", "PCLL", "CCLW"}},
		{"prospect skips its own status and entry statuses", "PROS", "PDIS", []string{"PCLL", "CCLW"}},
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

// TestCRMStatusIsTerminal covers the pill's confirm-click rule: the original
// "Closed ..." name rule plus any status a stage flow gives no way out of.
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
		{"closed lost by name", "PROS", "PCLL", "Prospect Closed Lost", true},
		{"closed won by name", "CUST", "CCLW", "Customer Closed Won", true},
		{"prospect in discussion has moves", "PROS", "PDIS", "Prospect In Discussion", false},
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

// TestCheckConvertFrom: only a Qualified lead may be converted; stages with no
// requirement convert from any status.
func TestCheckConvertFrom(t *testing.T) {
	tests := []struct {
		name       string
		typeCode   string
		statusCode string
		wantErr    bool
	}{
		{"qualified lead may convert", "LEAD", "LQUA", false},
		{"new lead may not", "LEAD", "LNEW", true},
		{"unqualified lead may not", "LEAD", "LUNQ", true},
		{"lead with no status may not", "LEAD", "", true},
		{"prospect converts from any status", "PROS", "PDIS", false},
		{"prospect with no status still converts", "PROS", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkConvertFrom(tc.typeCode, tc.statusCode)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			var ce ClientError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, msgConvertNeedsQualified, ce.Msg)
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
	for stage, required := range crmConvertFromStatus {
		if flow, ok := crmStageFlows[stage]; ok {
			assert.Truef(t, flow.Known(required), "stage %s: convert-from status %s is not in its flow", stage, required)
		}
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
	for stage, code := range crmConvertFromStatus {
		needed = append(needed, codeInStage{code, stage})
	}

	for _, n := range needed {
		pattern := fmt.Sprintf(`\('%s',\s*'[^']+',\s*%d,`, n.code, stageTypeID[n.stage])
		assert.Regexpf(t, pattern, block, "status %s is not seeded under stage %s", n.code, n.stage)
	}
}
