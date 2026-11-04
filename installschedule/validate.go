package installschedule

import (
	"fmt"
	"time"
	"unicode/utf8"

	"stonesuite-backend/dateonly"
)

const (
	backdateDays   = 1
	dateLayoutLen  = len(layoutDate)
	fieldCrew      = "crew"
	fieldNotes     = "notes"
	fieldVisitType = "visitType"
	fieldJob       = "fabricationJobId"
	fieldReason    = "reason"
	fieldChanges   = "changes"
)

// ValidateCrew checks crew size, ids, roles, uniqueness and the single-lead rule.
func ValidateCrew(crew []CrewInput) error {
	if len(crew) > MaxCrew {
		return &ValidationError{Field: fieldCrew, Message: fmt.Sprintf("at most %d crew members", MaxCrew)}
	}
	seen := make(map[int]struct{}, len(crew))
	leads := 0
	for _, c := range crew {
		if c.EmployeeID <= 0 {
			return &ValidationError{Field: fieldCrew, Message: "employeeId must be positive"}
		}
		if _, dup := seen[c.EmployeeID]; dup {
			return &ValidationError{Field: fieldCrew, Message: "duplicate employee"}
		}
		seen[c.EmployeeID] = struct{}{}
		switch c.Role {
		case CrewLead:
			leads++
		case CrewMemberRole:
		default:
			return &ValidationError{Field: fieldCrew, Message: "unknown role"}
		}
	}
	if leads > 1 {
		return &ValidationError{Field: fieldCrew, Message: "at most one lead"}
	}
	return nil
}

func validVisitType(v VisitType) bool {
	return v == VisitTemplate || v == VisitInstall || v == VisitService
}

// ValidateCreate checks the pure (no DB, no zone) rules of a create request against tenant today.
func ValidateCreate(in CreateInput, today time.Time) error {
	if in.FabricationJobUUID == "" {
		return &ValidationError{Field: fieldJob, Message: "required"}
	}
	if !validVisitType(in.VisitType) {
		return &ValidationError{Field: fieldVisitType, Message: "must be template, install or service"}
	}
	if utf8.RuneCountInString(in.Notes) > MaxNotes {
		return &ValidationError{Field: fieldNotes, Message: fmt.Sprintf("at most %d characters", MaxNotes)}
	}
	if len(in.StartLocal) < dateLayoutLen {
		return &ValidationError{Field: fieldStart, Message: "required"}
	}
	startDay, err := dateonly.Parse(in.StartLocal[:dateLayoutLen])
	if err != nil {
		return &ValidationError{Field: fieldStart, Message: "invalid date"}
	}
	if err := validateNotBackdated(startDay, today); err != nil {
		return err
	}
	return ValidateCrew(in.Crew)
}

// validateNotBackdated rejects a start date earlier than tenant today minus the backdate allowance.
func validateNotBackdated(startDay dateonly.Date, today time.Time) error {
	earliest := dateonly.New(today).AddDate(0, 0, -backdateDays)
	if startDay.Before(earliest) {
		return &ValidationError{Field: fieldStart, Message: fmt.Sprintf("cannot be more than %d day in the past", backdateDays)}
	}
	return nil
}

// ValidateReason checks a cancel/reschedule reason is at most MaxReason characters.
func ValidateReason(s string) error {
	if utf8.RuneCountInString(s) > MaxReason {
		return &ValidationError{Field: fieldReason, Message: fmt.Sprintf("at most %d characters", MaxReason)}
	}
	return nil
}
