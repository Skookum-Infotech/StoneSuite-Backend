package importer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// ApplyColumnMapping turns one tabular row (a CSV/XLSX parse.Document.Rows
// entry: column header -> cell text) into MappedFields, using mapping
// (column header -> "core:<key>" | "cf:<key>" | "" to ignore). A column
// absent from mapping, mapped to "", or mapped to an unrecognized prefix is
// silently skipped — see ValidateMapping for surfacing a bad mapping to the
// caller once, at job-creation time, rather than per-row.
//
// defs is the target workflow's custom field definitions: a cell's text is
// coerced to its cf: field's DataType (e.g. "5000" -> 5000.0 for a number
// field) via coerceCustomValue — workflow.ValidateCustomFields requires an
// actual JSON number/bool, not a numeric string, since manual create-record
// calls send typed JSON, so leaving every cell as a string would make any
// non-string/enum/email custom field impossible to import.
func ApplyColumnMapping(row map[string]string, mapping map[string]string, defs []workflow.FieldDefinition) MappedFields {
	defByKey := make(map[string]workflow.FieldDefinition, len(defs))
	for _, d := range defs {
		defByKey[d.Key] = d
	}
	out := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	for col, val := range row {
		target := mapping[col]
		switch {
		case target == "":
			continue
		case strings.HasPrefix(target, corePrefix):
			out.Core[strings.TrimPrefix(target, corePrefix)] = val
		case strings.HasPrefix(target, cfPrefix):
			key := strings.TrimPrefix(target, cfPrefix)
			out.Custom[key] = coerceCustomValue(defByKey[key], val)
		}
	}
	return out
}

// coerceCustomValue converts one cell's raw text to the Go type
// workflow.ValidateCustomFields expects for def.DataType. An empty cell is
// left as an empty string — isEmpty treats that as "not provided", the same
// as a missing key, rather than becoming a false/zero value that IS
// provided. A cell that fails to parse is also left as the raw string, so
// validation reports its own normal "must be a number"/"must be true or
// false" message instead of this package inventing one.
func coerceCustomValue(def workflow.FieldDefinition, raw string) any {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	switch def.DataType {
	case workflow.TypeNumber:
		if n, err := strconv.ParseFloat(raw, 64); err == nil {
			return n
		}
	case workflow.TypeBool:
		if b, err := strconv.ParseBool(raw); err == nil {
			return b
		}
	}
	return raw
}

// ValidateMapping checks that every non-empty mapping target names either a
// recognized core field or a custom field key that actually exists on the
// workflow (defs) — catching a typo'd key once, at job-creation time, rather
// than as a confusing per-row error later, or (for core: on DesignV2 — see
// below) not at all.
//
// designVersion selects how strictly core: targets are checked:
//   - DesignV2 (tenancy.DesignV2): CoreFields has a fixed schema
//     (crmstore.KnownCoreFieldKeys, the customerFields registry). A key
//     outside it is accepted by ApplyColumnMapping and then silently never
//     written by relationalStore.insertCustomer — the row still stages and
//     commits "successfully" with that column simply empty. This was
//     previously undetected: mapping "Email" to core:email instead of
//     core:customer_contact_email produced a green commit summary with the
//     email quietly missing from every created record.
//   - Anything else (DesignV1 and its zero value): core_fields is
//     unrestricted JSONB with no fixed schema (see crmstore's package doc) —
//     validating core: here would invent a restriction that doesn't exist on
//     the write path, so every core: target is accepted as before.
func ValidateMapping(mapping map[string]string, defs []workflow.FieldDefinition, designVersion string) error {
	knownCustom := make(map[string]bool, len(defs))
	for _, d := range defs {
		knownCustom[d.Key] = true
	}
	var knownCore map[string]bool
	if designVersion == tenancy.DesignV2 {
		knownCore = crmstore.KnownCoreFieldKeys()
	}
	for col, target := range mapping {
		if target == "" {
			continue
		}
		switch {
		case strings.HasPrefix(target, corePrefix):
			key := strings.TrimPrefix(target, corePrefix)
			if knownCore != nil && !knownCore[key] {
				return fmt.Errorf("column %q maps to unrecognized core field %q", col, key)
			}
		case strings.HasPrefix(target, cfPrefix):
			key := strings.TrimPrefix(target, cfPrefix)
			if !knownCustom[key] {
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
