package workflow

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// emailRe is a pragmatic email check (not RFC-perfect, good enough for input).
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ValidationError describes a single field validation failure.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string { return e.Field + ": " + e.Message }

// ValidationErrors aggregates field errors from a single validation pass.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Error()
	}
	return strings.Join(parts, "; ")
}

// ValidateCustomFields checks a record's custom_fields against the workflow's
// field definitions: rejects unknown keys, enforces required, and type/format/
// constraint checks. Returns nil or a ValidationErrors with all problems.
func ValidateCustomFields(defs []FieldDefinition, values map[string]any) error {
	defByKey := make(map[string]FieldDefinition, len(defs))
	for _, d := range defs {
		defByKey[d.Key] = d
	}

	var errs ValidationErrors

	// Reject keys with no definition (the contract is closed).
	for k := range values {
		if _, ok := defByKey[k]; !ok {
			errs = append(errs, ValidationError{Field: k, Message: "unknown field"})
		}
	}

	for _, d := range defs {
		v, present := values[d.Key]
		if !present || isEmpty(v) {
			if d.Required {
				errs = append(errs, ValidationError{Field: d.Key, Message: "is required"})
			}
			continue
		}
		if err := validateValue(d, v); err != nil {
			errs = append(errs, ValidationError{Field: d.Key, Message: err.Error()})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

// validateValue checks a single value against its field definition.
func validateValue(d FieldDefinition, v any) error {
	switch d.DataType {
	case TypeString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		return checkRegex(d, s)

	case TypeEmail:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		if !emailRe.MatchString(s) {
			return fmt.Errorf("must be a valid email")
		}
		return checkRegex(d, s)

	case TypeNumber:
		n, ok := toFloat(v)
		if !ok {
			return fmt.Errorf("must be a number")
		}
		if d.Validation.Min != nil && n < *d.Validation.Min {
			return fmt.Errorf("must be >= %g", *d.Validation.Min)
		}
		if d.Validation.Max != nil && n > *d.Validation.Max {
			return fmt.Errorf("must be <= %g", *d.Validation.Max)
		}
		return nil

	case TypeBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("must be true or false")
		}
		return nil

	case TypeDate:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("must be a date string")
		}
		if !isDate(s) {
			return fmt.Errorf("must be an ISO-8601 date (YYYY-MM-DD) or RFC3339 datetime")
		}
		return nil

	case TypeEnum:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("must be one of the allowed options")
		}
		for _, opt := range d.Options {
			if s == opt {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(d.Options, ", "))

	default:
		return fmt.Errorf("unsupported field type %q", d.DataType)
	}
}

func checkRegex(d FieldDefinition, s string) error {
	if d.Validation.Regex == "" {
		return nil
	}
	re, err := regexp.Compile(d.Validation.Regex)
	if err != nil {
		// A bad regex in the definition shouldn't block input; treat as no constraint.
		return nil
	}
	if !re.MatchString(s) {
		return fmt.Errorf("does not match the required format")
	}
	return nil
}

// toFloat accepts the numeric shapes JSON / pgx may produce.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func isDate(s string) bool {
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	return false
}

// ValidateCustomFieldsPartial validates type/format for any values that are
// present but does NOT enforce required fields. Used during record conversions
// where the caller seeds what it can from the source record; fields the source
// didn't have can be filled in afterward via the edit form.
func ValidateCustomFieldsPartial(defs []FieldDefinition, values map[string]any) error {
	defByKey := make(map[string]FieldDefinition, len(defs))
	for _, d := range defs {
		defByKey[d.Key] = d
	}

	var errs ValidationErrors

	for k := range values {
		if _, ok := defByKey[k]; !ok {
			errs = append(errs, ValidationError{Field: k, Message: "unknown field"})
		}
	}

	for _, d := range defs {
		v, present := values[d.Key]
		if !present || isEmpty(v) {
			continue // skip required check — caller is responsible for filling later
		}
		if err := validateValue(d, v); err != nil {
			errs = append(errs, ValidationError{Field: d.Key, Message: err.Error()})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ValidateFieldDefinition checks a definition itself is well-formed before save.
func ValidateFieldDefinition(d FieldDefinition) error {
	if strings.TrimSpace(d.Key) == "" {
		return fmt.Errorf("field key is required")
	}
	if !validFieldKey.MatchString(d.Key) {
		return fmt.Errorf("field key %q must be lowercase alphanumeric/underscore", d.Key)
	}
	switch d.DataType {
	case TypeString, TypeNumber, TypeDate, TypeBool, TypeEnum, TypeEmail:
	default:
		return fmt.Errorf("unknown data type %q", d.DataType)
	}
	if d.DataType == TypeEnum && len(d.Options) == 0 {
		return fmt.Errorf("enum field %q requires options", d.Key)
	}
	if d.Validation.Regex != "" {
		if _, err := regexp.Compile(d.Validation.Regex); err != nil {
			return fmt.Errorf("invalid regex for %q: %w", d.Key, err)
		}
	}
	return nil
}

var validFieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
