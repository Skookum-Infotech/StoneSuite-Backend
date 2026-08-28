package feedback

import (
	"fmt"
	"strings"
)

// ValidateCreate checks a reporter's new-ticket submission. Pure and
// side-effect free so it is fully table-driven testable.
func ValidateCreate(in CreateInput) error {
	if !ValidReporterKind(in.ReporterKind) {
		return fmt.Errorf("invalid reporter kind %q", in.ReporterKind)
	}
	if !ValidCategory(in.Category) {
		return fmt.Errorf("invalid category %q", in.Category)
	}
	if !ValidArea(in.Area) {
		return fmt.Errorf("invalid area %q", in.Area)
	}
	if strings.TrimSpace(in.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if len(in.Description) > MaxDescriptionLength {
		return fmt.Errorf("description must be %d characters or fewer", MaxDescriptionLength)
	}
	if in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5) {
		return fmt.Errorf("rating must be between 1 and 5")
	}
	return nil
}

// ValidateComment checks a reply body before it is persisted.
func ValidateComment(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("comment body is required")
	}
	if len(body) > MaxCommentLength {
		return fmt.Errorf("comment must be %d characters or fewer", MaxCommentLength)
	}
	return nil
}

// ValidateInternalNotes checks the admin-only notes field before it is persisted.
// Empty is allowed (clearing the notes).
func ValidateInternalNotes(notes string) error {
	if len(notes) > MaxInternalNotesLength {
		return fmt.Errorf("internal notes must be %d characters or fewer", MaxInternalNotesLength)
	}
	return nil
}
