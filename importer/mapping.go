package importer

import (
	"errors"
	"fmt"
	"strings"

	"stonesuite-backend/workflow"
)

// ApplyColumnMapping turns one tabular row (a CSV/XLSX parse.Document.Rows
// entry: column header -> cell text) into MappedFields, using mapping
// (column header -> "core:<key>" | "cf:<key>" | "" to ignore). A column
// absent from mapping, mapped to "", or mapped to an unrecognized prefix is
// silently skipped — see ValidateMapping for surfacing a bad mapping to the
// caller once, at job-creation time, rather than per-row.
func ApplyColumnMapping(row map[string]string, mapping map[string]string) MappedFields {
	out := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	for col, val := range row {
		target := mapping[col]
		switch {
		case target == "":
			continue
		case strings.HasPrefix(target, corePrefix):
			out.Core[strings.TrimPrefix(target, corePrefix)] = val
		case strings.HasPrefix(target, cfPrefix):
			out.Custom[strings.TrimPrefix(target, cfPrefix)] = val
		}
	}
	return out
}

// ValidateMapping checks that every non-empty mapping target names either
// the core-field prefix or a custom field key that actually exists on the
// workflow (defs) — catching a typo'd cf: key once, at job-creation time,
// rather than as a confusing per-row error later. Core field keys have no
// fixed schema this package can check against (crmstore.CreateInput.CoreFields
// is a plain map); an unknown core key is instead rejected per-row by
// CreateRecord's own validation at commit time.
func ValidateMapping(mapping map[string]string, defs []workflow.FieldDefinition) error {
	known := make(map[string]bool, len(defs))
	for _, d := range defs {
		known[d.Key] = true
	}
	for col, target := range mapping {
		if target == "" {
			continue
		}
		switch {
		case strings.HasPrefix(target, corePrefix):
			// see doc comment: intentionally not validated here.
		case strings.HasPrefix(target, cfPrefix):
			key := strings.TrimPrefix(target, cfPrefix)
			if !known[key] {
				return fmt.Errorf("column %q maps to unknown custom field %q", col, key)
			}
		default:
			return fmt.Errorf("column %q has an invalid mapping target %q (must start with %q or %q)",
				col, target, corePrefix, cfPrefix)
		}
	}
	return nil
}

// stageErrors returns human-readable partial-validation errors for custom
// (required-field checks are deferred to commit time, via
// workflow.ValidateCustomFields, since a value might legitimately be filled
// in during review) — a hint shown to a reviewer before commit, not a
// blocking check.
func stageErrors(defs []workflow.FieldDefinition, custom map[string]any) []string {
	return validationMessages(workflow.ValidateCustomFieldsPartial(defs, custom))
}

// validationMessages flattens a workflow.ValidationErrors (or any other
// error) into plain strings for storage in import_rows.errors.
func validationMessages(err error) []string {
	if err == nil {
		return nil
	}
	var verrs workflow.ValidationErrors
	if errors.As(err, &verrs) {
		out := make([]string, len(verrs))
		for i, v := range verrs {
			out[i] = v.Field + ": " + v.Message
		}
		return out
	}
	return []string{err.Error()}
}
