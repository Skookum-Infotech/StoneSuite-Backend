package query

import "time"

// dateLayouts are accepted inbound date/datetime formats. Values are bound to
// the query as time.Time so comparisons against timestamptz columns (and
// ::timestamptz-cast JSONB) are type-correct rather than text-vs-timestamp.
var dateLayouts = []string{time.RFC3339, "2006-01-02"}

// coerceScalar converts a JSON value to the bindable Go type for dt.
func coerceScalar(field string, dt DataType, v any) (any, error) {
	switch dt {
	case TypeNumber:
		f, ok := toFloat(v)
		if !ok {
			return nil, invalid(field, "expected a number")
		}
		return f, nil
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, invalid(field, "expected a boolean")
		}
		return b, nil
	case TypeDate:
		t, err := toDate(field, v)
		if err != nil {
			return nil, err
		}
		return t, nil
	default: // string, email, enum
		s, ok := v.(string)
		if !ok {
			return nil, invalid(field, "expected a string")
		}
		return s, nil
	}
}

// coerceString requires a plain string value (contains/startswith).
func coerceString(field string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", invalid(field, "expected a string")
	}
	return s, nil
}

// coerceList converts a JSON array into a typed slice for `= ANY($n)`.
func coerceList(field string, dt DataType, v any) (any, error) {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		return nil, invalid(field, "expected a non-empty list")
	}
	switch dt {
	case TypeNumber:
		out := make([]float64, len(raw))
		for i, e := range raw {
			f, ok := toFloat(e)
			if !ok {
				return nil, invalid(field, "list must contain numbers")
			}
			out[i] = f
		}
		return out, nil
	default: // string, email, enum
		out := make([]string, len(raw))
		for i, e := range raw {
			s, ok := e.(string)
			if !ok {
				return nil, invalid(field, "list must contain strings")
			}
			out[i] = s
		}
		return out, nil
	}
}

// coercePair converts a 2-element JSON array into (lo, hi) for BETWEEN.
func coercePair(field string, dt DataType, v any) (any, any, error) {
	raw, ok := v.([]any)
	if !ok || len(raw) != 2 {
		return nil, nil, invalid(field, "between expects a [lo, hi] list")
	}
	lo, err := coerceScalar(field, dt, raw[0])
	if err != nil {
		return nil, nil, err
	}
	hi, err := coerceScalar(field, dt, raw[1])
	if err != nil {
		return nil, nil, err
	}
	return lo, hi, nil
}

// toFloat accepts JSON numbers (float64) and numeric strings.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// toDate parses a date/datetime value into time.Time.
func toDate(field string, v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, invalid(field, "expected a date string")
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, invalid(field, "unrecognized date format")
}
