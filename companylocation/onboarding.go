package companylocation

import (
	"encoding/json"
	"strings"
)

// OnboardingKeyPrefix is the flat metadata key prefix OnboardingForm.tsx uses
// for the applicant's primary location: location_name, location_phone, and
// location_address_line1/_line2/_suite/_city/_country/_state/_zip.
const OnboardingKeyPrefix = "location"

// FromOnboardingMetadata extracts the applicant's primary location from a
// tenant's onboarding metadata JSON (tenancy.Tenant.Metadata). ok is false
// when the metadata is empty, malformed, or has no usable location name --
// the caller then leaves the tenant with zero locations (the Locations tab's
// normal empty state) rather than writing a row that would fail Validate.
func FromOnboardingMetadata(metadataJSON string) (in Input, ok bool) {
	if metadataJSON == "" {
		return Input{}, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(metadataJSON), &raw); err != nil {
		return Input{}, false
	}

	str := func(key string) string {
		v, _ := raw[key].(string)
		return strings.TrimSpace(v)
	}
	const addr = OnboardingKeyPrefix + "_address"

	in = Input{
		Name:  str(OnboardingKeyPrefix + "_name"),
		Phone: str(OnboardingKeyPrefix + "_phone"),
		Address: Address{
			Line1:   str(addr + "_line1"),
			Line2:   str(addr + "_line2"),
			Suite:   str(addr + "_suite"),
			City:    str(addr + "_city"),
			Country: str(addr + "_country"),
			State:   str(addr + "_state"),
			Zip:     str(addr + "_zip"),
		},
	}
	return in, in.Name != ""
}
