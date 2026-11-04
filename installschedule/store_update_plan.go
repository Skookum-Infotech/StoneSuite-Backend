package installschedule

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// updatePlan is the effective result of applying an UpdateInput to the stored appointment.
type updatePlan struct {
	iv             Interval
	timeChanged    bool
	visitType      VisitType
	notes          string
	fieldsChanged  bool
	crew           []CrewInput
	crewChanged    bool
	added, removed []int
}

// planUpdate validates in and diffs it against cur; it touches no database.
func planUpdate(cur *Appointment, in UpdateInput, loc *time.Location, now time.Time) (updatePlan, error) {
	if err := ValidateReason(in.Reason); err != nil {
		return updatePlan{}, err
	}
	p := updatePlan{
		iv: Interval{
			Start: cur.StartsAt, End: cur.EndsAt, AllDay: cur.AllDay,
			LocalStartDate: cur.LocalStartDate, LocalEndDate: cur.LocalEndDate, TZ: cur.Timezone,
		},
		visitType: cur.VisitType,
		notes:     cur.Notes,
	}
	if err := p.planTime(cur, in, loc, now); err != nil {
		return updatePlan{}, err
	}
	if err := p.planFields(cur, in); err != nil {
		return updatePlan{}, err
	}
	if err := p.planCrew(cur, in); err != nil {
		return updatePlan{}, err
	}
	if !p.timeChanged && !p.fieldsChanged && !p.crewChanged {
		return updatePlan{}, &ValidationError{Field: fieldChanges, Message: "no changes"}
	}
	return p, nil
}

// planTime resolves the new interval when any time field was supplied.
func (p *updatePlan) planTime(cur *Appointment, in UpdateInput, loc *time.Location, now time.Time) error {
	if in.StartLocal == nil && in.EndLocal == nil && in.AllDay == nil {
		return nil
	}
	allDay := cur.AllDay
	if in.AllDay != nil {
		allDay = *in.AllDay
	}
	startS, endS := cur.StartLocal, cur.EndLocal
	if cur.AllDay {
		startS, endS = cur.LocalStartDate.String(), cur.LocalEndDate.String()
	}
	if in.StartLocal != nil {
		startS = *in.StartLocal
	}
	if in.EndLocal != nil {
		endS = *in.EndLocal
	}
	startS, endS = fitBound(startS, allDay), fitBound(endS, allDay)

	iv, err := ResolveInterval(startS, endS, allDay, loc)
	if err != nil {
		return err
	}
	if iv.Start.Equal(cur.StartsAt) && iv.End.Equal(cur.EndsAt) && iv.AllDay == cur.AllDay {
		return nil
	}
	// A bound the client did not send keeps its stored instant rather than being rebuilt in the current zone.
	if allDay == cur.AllDay {
		if in.StartLocal == nil {
			iv.Start, iv.LocalStartDate = cur.StartsAt, cur.LocalStartDate
		}
		if in.EndLocal == nil {
			iv.End, iv.LocalEndDate = cur.EndsAt, cur.LocalEndDate
		}
	}
	if !iv.Start.Equal(cur.StartsAt) {
		if err := validateNotBackdated(iv.LocalStartDate, now.In(loc)); err != nil {
			return err
		}
	}
	p.iv, p.timeChanged = iv, true
	return nil
}

// fitBound trims a wall clock to a date for all-day visits and pads a date to midnight for timed ones.
func fitBound(s string, allDay bool) string {
	switch {
	case allDay && len(s) > dateLayoutLen:
		return s[:dateLayoutLen]
	case !allDay && len(s) == dateLayoutLen:
		return s + "T00:00"
	}
	return s
}

// planFields applies notes and visit type changes.
func (p *updatePlan) planFields(cur *Appointment, in UpdateInput) error {
	if in.Notes != nil {
		if utf8.RuneCountInString(*in.Notes) > MaxNotes {
			return &ValidationError{Field: fieldNotes, Message: fmt.Sprintf("at most %d characters", MaxNotes)}
		}
		if *in.Notes != cur.Notes {
			p.notes, p.fieldsChanged = *in.Notes, true
		}
	}
	if in.VisitType != nil {
		if !validVisitType(*in.VisitType) {
			return &ValidationError{Field: fieldVisitType, Message: "must be template, install or service"}
		}
		if *in.VisitType != cur.VisitType {
			p.visitType, p.fieldsChanged = *in.VisitType, true
		}
	}
	return nil
}

// planCrew diffs a replacement crew (already checked by ValidateCrew) and computes added and removed employee ids.
func (p *updatePlan) planCrew(cur *Appointment, in UpdateInput) error {
	curRoles := make(map[int]CrewRole, len(cur.Crew))
	for _, m := range cur.Crew {
		curRoles[m.EmployeeID] = m.Role
		p.crew = append(p.crew, CrewInput{EmployeeID: m.EmployeeID, Role: m.Role})
	}
	if in.Crew == nil {
		return nil
	}
	p.crew = *in.Crew
	next := make(map[int]struct{}, len(p.crew))
	for _, c := range p.crew {
		next[c.EmployeeID] = struct{}{}
		role, had := curRoles[c.EmployeeID]
		switch {
		case !had:
			p.added = append(p.added, c.EmployeeID)
			p.crewChanged = true
		case role != c.Role:
			p.crewChanged = true
		}
	}
	for _, m := range cur.Crew {
		if _, kept := next[m.EmployeeID]; !kept {
			p.removed = append(p.removed, m.EmployeeID)
			p.crewChanged = true
		}
	}
	p.added, p.removed = sortedUniqueIDs(p.added), sortedUniqueIDs(p.removed)
	return nil
}

// conflictEmployees returns the employees whose availability the change affects: the whole crew when the
// time moved, otherwise only the newly added members.
func (p *updatePlan) conflictEmployees() []int {
	if p.timeChanged {
		return crewIDs(p.crew)
	}
	return p.added
}

// needsConflictCheck reports whether the change can introduce an overlap.
func (p *updatePlan) needsConflictCheck() bool {
	return p.timeChanged || len(p.added) > 0
}
