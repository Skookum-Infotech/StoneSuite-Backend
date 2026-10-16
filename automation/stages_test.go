package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPlanStages(t *testing.T) {
	cadence := []int{-3, 0, 7, 14}
	due := d("2026-11-10")
	early := d("2026-11-01")

	tests := []struct {
		name      string
		offsets   []int
		anchor    time.Time
		today     time.Time
		enabledOn time.Time
		want      []StagePlan
	}{
		{"before first stage returns nothing", cadence, due, d("2026-11-06"), early, nil},
		{"first stage due today", cadence, due, d("2026-11-07"), early,
			[]StagePlan{{0, d("2026-11-07"), StageActionPlan}}},
		{"two days after first stage still plan", cadence, due, d("2026-11-09"), early,
			[]StagePlan{{0, d("2026-11-07"), StageActionPlan}}},
		{"second stage due supersedes first", cadence, due, d("2026-11-10"), early,
			[]StagePlan{{0, d("2026-11-07"), StageActionSuperseded}, {1, d("2026-11-10"), StageActionPlan}}},
		{"catch-up supersedes earlier, future omitted", cadence, due, d("2026-11-18"), early,
			[]StagePlan{
				{0, d("2026-11-07"), StageActionSuperseded},
				{1, d("2026-11-10"), StageActionSuperseded},
				{2, d("2026-11-17"), StageActionPlan},
			}},
		{"last stage plan", cadence, due, d("2026-11-25"), early,
			[]StagePlan{
				{0, d("2026-11-07"), StageActionSuperseded},
				{1, d("2026-11-10"), StageActionSuperseded},
				{2, d("2026-11-17"), StageActionSuperseded},
				{3, d("2026-11-24"), StageActionPlan},
			}},
		{"latest too old is stale", cadence, due, d("2026-12-10"), early,
			[]StagePlan{
				{0, d("2026-11-07"), StageActionSuperseded},
				{1, d("2026-11-10"), StageActionSuperseded},
				{2, d("2026-11-17"), StageActionSuperseded},
				{3, d("2026-11-24"), StageActionStale},
			}},
		{"stages before enable date are pre_enable", cadence, due, d("2026-11-25"), d("2026-11-20"),
			[]StagePlan{
				{0, d("2026-11-07"), StageActionPreEnable},
				{1, d("2026-11-10"), StageActionPreEnable},
				{2, d("2026-11-17"), StageActionPreEnable},
				{3, d("2026-11-24"), StageActionPlan},
			}},
		{"empty offsets", nil, due, d("2026-11-25"), early, nil},
		{"anchor equals today with offset zero", []int{0}, due, due, early,
			[]StagePlan{{0, due, StageActionPlan}}},
		{"unsorted offsets indexed by position", []int{7, 0}, due, d("2026-11-18"), early,
			[]StagePlan{
				{0, d("2026-11-17"), StageActionPlan},
				{1, d("2026-11-10"), StageActionSuperseded},
			}},
		{"enabled on the stage date is pre_enable", cadence, due, d("2026-11-10"), d("2026-11-10"),
			[]StagePlan{
				{0, d("2026-11-07"), StageActionPreEnable},
				{1, d("2026-11-10"), StageActionPreEnable},
			}},
		{"later today, stages on or before enable date stay pre_enable", cadence, due, d("2026-11-11"), d("2026-11-10"),
			[]StagePlan{
				{0, d("2026-11-07"), StageActionPreEnable},
				{1, d("2026-11-10"), StageActionPreEnable},
			}},
		{"stage dated day after enable plans", cadence, d("2026-11-14"), d("2026-11-11"), d("2026-11-10"),
			[]StagePlan{{0, d("2026-11-11"), StageActionPlan}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanStages(tc.anchor, tc.offsets, tc.today, tc.enabledOn)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPlanStages_DSTLocalDates(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	assert.NoError(t, err)
	// Stage crosses the 2026-11-01 fall-back transition; the date must stay calendar-exact.
	anchor := time.Date(2026, 10, 30, 0, 0, 0, 0, loc)
	today := time.Date(2026, 11, 2, 0, 0, 0, 0, loc)
	enabled := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	got := PlanStages(anchor, []int{3}, today, enabled)
	if assert.Len(t, got, 1) {
		assert.Equal(t, StageActionPlan, got[0].Action)
		y, m, dd := got[0].Date.Date()
		assert.Equal(t, 2026, y)
		assert.Equal(t, time.November, m)
		assert.Equal(t, 2, dd)
	}
}
