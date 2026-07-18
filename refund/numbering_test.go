package refund

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "RFND-000001", 42: "RFND-000042", 1234567: "RFND-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}

// TestNumberPrefixMatchesRecordTypeCode pins numberPrefix to the seeded
// lkp_record_type code ('RFND', record_type id 10) so the two never drift —
// see database/migrations/tenant/schema.sql:700.
func TestNumberPrefixMatchesRecordTypeCode(t *testing.T) {
	if numberPrefix != "RFND" {
		t.Errorf("numberPrefix = %q, want %q (must match the seeded lkp_record_type.record_type_code)", numberPrefix, "RFND")
	}
}
