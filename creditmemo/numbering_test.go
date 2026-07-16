package creditmemo

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		name string
		id   int64
		want string
	}{
		{"first", 1, "CRDT-000001"},
		{"two digits", 42, "CRDT-000042"},
		{"pad boundary", 999999, "CRDT-999999"},
		{"overflow past padding", 1000000, "CRDT-1000000"},
		{"zero", 0, "CRDT-000000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatNumber(tc.id); got != tc.want {
				t.Errorf("FormatNumber(%d) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// The prefix must match the CRDT record_type_code seeded in lkp_record_type,
// which is what the store resolves the record type by.
func TestNumberPrefixMatchesRecordTypeCode(t *testing.T) {
	if numberPrefix != "CRDT" {
		t.Errorf("numberPrefix = %q, want %q (lkp_record_type.record_type_code)", numberPrefix, "CRDT")
	}
}
