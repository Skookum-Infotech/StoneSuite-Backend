package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func approvalRule() ruleRow {
	r := ruleRow{ID: "rule-1"}
	r.Config.Cadence = []ARCadenceStage{{OffsetDays: 2}, {OffsetDays: 4}, {OffsetDays: 6}}
	return r
}

func TestApprovalInsertsFor(t *testing.T) {
	loc := time.UTC
	today := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
	env := JobEnv{Now: today.Add(10 * time.Hour), Today: today, Loc: loc}
	spec := approvalSpecs()[0]
	alice := approvalRecipient{UserID: "u-a", Email: "a@x.test"}
	bob := approvalRecipient{UserID: "u-b", Email: "b@x.test"}
	rec := func(days int) ApprovalRecord {
		return ApprovalRecord{UUID: "rec-1", Entered: today.AddDate(0, 0, -days)}
	}
	oldEnabled := today.AddDate(0, 0, -30)

	tests := []struct {
		name   string
		rule   func() ruleRow
		target approvalTarget
		wantN  int
		check  func(t *testing.T, rows []deliveryInsert)
	}{
		{"3 days: stage 0 plan", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(3), Rcpts: []approvalRecipient{alice}}, 1,
			func(t *testing.T, rows []deliveryInsert) {
				assert.Equal(t, statusPlanned, rows[0].Status)
				assert.Equal(t, 0, rows[0].Stage)
				assert.Equal(t, "rec-1", rows[0].RecordID)
				require.NotNil(t, rows[0].UserID)
				assert.Equal(t, "u-a", *rows[0].UserID)
			}},
		{"5 days: stage 0 superseded, stage 1 plan", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(5), Rcpts: []approvalRecipient{alice}}, 2,
			func(t *testing.T, rows []deliveryInsert) {
				assert.Equal(t, statusSkipped, rows[0].Status)
				assert.Equal(t, string(StageActionSuperseded), *rows[0].Reason)
				assert.Equal(t, statusPlanned, rows[1].Status)
			}},
		{"9 days: stale", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(9), Rcpts: []approvalRecipient{alice}}, 3,
			func(t *testing.T, rows []deliveryInsert) {
				assert.Equal(t, string(StageActionStale), *rows[2].Reason)
				assert.Equal(t, statusSkipped, rows[2].Status)
			}},
		{"enabled today: pre_enable", func() ruleRow { r := approvalRule(); r.EnabledOn = today; return r },
			approvalTarget{Rec: rec(3), Rcpts: []approvalRecipient{alice}}, 1,
			func(t *testing.T, rows []deliveryInsert) {
				assert.Equal(t, string(StageActionPreEnable), *rows[0].Reason)
			}},
		{"two recipients: distinct keys, own digests", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(3), Rcpts: []approvalRecipient{alice, bob}}, 2,
			func(t *testing.T, rows []deliveryInsert) {
				assert.NotEqual(t, rows[0].Key, rows[1].Key)
				assert.True(t, strings.Contains(rows[0].Key, "u-a") && strings.Contains(rows[1].Key, "u-b"))
				assert.NotEqual(t, *rows[0].Digest, *rows[1].Digest)
				assert.Equal(t, DigestKey("rule-1", "a@x.test", today), *rows[0].Digest)
			}},
		{"no recipient: one no_email skipped row", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(3)}, 1,
			func(t *testing.T, rows []deliveryInsert) {
				assert.Equal(t, statusSkipped, rows[0].Status)
				assert.Equal(t, reasonNoEmail, *rows[0].Reason)
				assert.Empty(t, rows[0].Email)
				assert.Nil(t, rows[0].UserID)
				assert.Contains(t, rows[0].Key, "~none")
			}},
		{"recipient without email: no_email", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(3), Rcpts: []approvalRecipient{{UserID: "u-c"}}}, 1,
			func(t *testing.T, rows []deliveryInsert) {
				assert.Equal(t, reasonNoEmail, *rows[0].Reason)
				assert.Nil(t, rows[0].Digest)
			}},
		{"1 day old: nothing due", func() ruleRow { r := approvalRule(); r.EnabledOn = oldEnabled; return r },
			approvalTarget{Rec: rec(1), Rcpts: []approvalRecipient{alice}}, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := approvalInsertsFor(spec, []approvalTarget{tc.target}, env, tc.rule())
			require.Len(t, rows, tc.wantN)
			if tc.check != nil {
				tc.check(t, rows)
			}
		})
	}
}
