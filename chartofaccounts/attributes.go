package chartofaccounts

import (
	"fmt"
	"sort"
	"strings"
)

// BankAccountNumberKey is the one attribute encrypted at rest and never
// returned in full (AD-10). masking.go keys off this constant.
const BankAccountNumberKey = "accountNumber"

// attrField describes one allowed key in an account's attributes JSONB.
type attrField struct {
	required bool
}

// attrSchema is the FIXED, developer-defined field schema per account_type.
// This is deliberately NOT the workflow custom-fields mechanism (AD-9): there
// is no workflow_field_definitions row, no <=15 cap, and no per-tenant
// configurability. Keys absent from a type's map are rejected outright.
var attrSchema = map[string]map[string]attrField{
	"general":     {},
	"ar":          {},
	"ap":          {},
	"inventory":   {},
	"cash":        {"location": {}},
	"tax":         {"taxRegistrationNumber": {}, "jurisdiction": {}},
	"fixed_asset": {"assetTag": {}, "usefulLifeYears": {}},
	"bank": {
		"bankName":           {required: true},
		BankAccountNumberKey: {required: true},
		"branch":             {},
		"routingNumber":      {},
		"swift":              {},
	},
	"credit_card": {
		"issuer":  {required: true},
		"last4":   {required: true},
		"network": {},
	},
}

// ValidAccountTypes returns every permitted account_type, sorted. It must stay
// in sync with chk_coa_type in database/migrations/tenant/schema.sql.
func ValidAccountTypes() []string {
	out := make([]string, 0, len(attrSchema))
	for k := range attrSchema {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateAttributes checks attrs against the fixed schema for accountType and
// returns the normalised map. Every value must be a non-blank string. Unknown
// keys, missing required keys, and non-string values are all ClientErrors so
// the controller renders them as 400 naming the offending field.
func ValidateAttributes(accountType string, attrs map[string]any) (map[string]any, error) {
	schema, ok := attrSchema[accountType]
	if !ok {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Unknown account type %q. Valid types: %s.",
			accountType, strings.Join(ValidAccountTypes(), ", "))}
	}

	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if _, allowed := schema[k]; !allowed {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Attribute %q is not valid for a %q account.", k, accountType)}
		}
		s, isStr := v.(string)
		if !isStr {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Attribute %q must be a string.", k)}
		}
		if strings.TrimSpace(s) == "" {
			// A blank value is the same as omitting the key. Required keys are
			// caught by the loop below.
			continue
		}
		out[k] = strings.TrimSpace(s)
	}

	// Deterministic ordering so the error message is stable across runs.
	required := make([]string, 0, len(schema))
	for k, f := range schema {
		if f.required {
			required = append(required, k)
		}
	}
	sort.Strings(required)
	for _, k := range required {
		if _, present := out[k]; !present {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Attribute %q is required for a %q account.", k, accountType)}
		}
	}
	return out, nil
}
