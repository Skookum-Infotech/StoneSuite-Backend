package vendorbill

import (
	"context"
	"testing"
)

func TestWellFormedUUID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"canonical lower", "3f2b8c1e-9a4d-4e6f-8b7a-1c2d3e4f5a6b", true},
		{"canonical upper", "3F2B8C1E-9A4D-4E6F-8B7A-1C2D3E4F5A6B", true},
		{"empty", "", false},
		{"not a uuid", "not-a-uuid", false},
		{"hyphenless (postgres would accept, we do not)", "3f2b8c1e9a4d4e6f8b7a1c2d3e4f5a6b", false},
		{"brace-wrapped (postgres would accept, we do not)", "{3f2b8c1e-9a4d-4e6f-8b7a-1c2d3e4f5a6b}", false},
		{"too short", "3f2b8c1e-9a4d-4e6f-8b7a-1c2d3e4f5a6", false},
		{"non-hex digit", "3f2b8c1e-9a4d-4e6f-8b7a-1c2d3e4f5a6g", false},
		{"trailing newline", "3f2b8c1e-9a4d-4e6f-8b7a-1c2d3e4f5a6b\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WellFormedUUID(tt.in); got != tt.want {
				t.Errorf("WellFormedUUID(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCheckLineageVendor(t *testing.T) {
	tests := []struct {
		name         string
		poVendorID   int
		billVendorID int
		wantErr      bool
	}{
		{"same vendor", 7, 7, false},
		{"different vendor", 7, 8, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkLineageVendor(tt.poVendorID, tt.billVendorID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkLineageVendor(%d, %d) err = %v, wantErr %v", tt.poVendorID, tt.billVendorID, err, tt.wantErr)
			}
			if err != nil && !IsClientError(err) {
				t.Errorf("err = %T, want a ClientError so the controller answers 400", err)
			}
		})
	}
}

// A blank or malformed uuid is decided before any query runs, so a nil
// querier proves the short-circuits never reach the database.
func TestResolvePurchaseOrderLineage_ShortCircuits(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantClient bool
	}{
		{"blank means no link", "", false},
		{"whitespace means no link", "   ", false},
		{"malformed is a client error, not a 22P02", "not-a-uuid", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := resolvePurchaseOrderLineage(context.Background(), nil, tt.in, 1)
			if id != nil {
				t.Errorf("id = %v, want nil", *id)
			}
			if tt.wantClient != IsClientError(err) {
				t.Errorf("IsClientError(%v) = %v, want %v", err, IsClientError(err), tt.wantClient)
			}
			if !tt.wantClient && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}
