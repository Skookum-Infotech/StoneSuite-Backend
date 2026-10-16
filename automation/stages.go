package automation

import "time"

// StageAction is what the planner should do with one cadence stage.
type StageAction string

// Stage actions returned by PlanStages.
const (
	StageActionPlan       StageAction = "plan"
	StageActionSuperseded StageAction = "superseded"
	StageActionStale      StageAction = "stale"
	StageActionPreEnable  StageAction = "pre_enable"
)

// maxStageAgeDays is how old (in calendar days) the latest due stage may be
// and still be sent; beyond it the stage is stale and skipped.
const maxStageAgeDays = 2

const hoursPerDay = 24

// StagePlan is the decision for one cadence stage (Stage is its index in the
// cadence slice) dated Date.
type StagePlan struct {
	Stage  int
	Date   time.Time
	Action StageAction
}

// calendarDays returns the whole calendar days from a to b, comparing only the
// Y/M/D of each value in its own location.
func calendarDays(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	ua := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	ub := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua) / (hoursPerDay * time.Hour))
}

// PlanStages classifies each cadence stage already due as of today (tenant-local
// calendar dates). Future stages are omitted. Stages dated before enabledOn are
// pre_enable; of the rest, the latest is plan when at most two days old else
// stale, and earlier ones are superseded. Results are ordered by stage index.
func PlanStages(anchor time.Time, offsets []int, today, enabledOn time.Time) []StagePlan {
	var out []StagePlan
	latest := -1 // index into out of the latest post-enable stage
	for i, off := range offsets {
		date := anchor.AddDate(0, 0, off)
		if calendarDays(date, today) < 0 {
			continue
		}
		sp := StagePlan{Stage: i, Date: date, Action: StageActionSuperseded}
		if calendarDays(enabledOn, date) < 0 {
			sp.Action = StageActionPreEnable
		} else if latest < 0 || calendarDays(out[latest].Date, date) >= 0 {
			latest = len(out)
		}
		out = append(out, sp)
	}
	if latest >= 0 {
		if calendarDays(out[latest].Date, today) <= maxStageAgeDays {
			out[latest].Action = StageActionPlan
		} else {
			out[latest].Action = StageActionStale
		}
	}
	return out
}
