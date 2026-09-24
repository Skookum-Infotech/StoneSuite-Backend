package aisettings

import "testing"

func TestAvailable(t *testing.T) {
	tests := []struct {
		name     string
		platform bool
		tenant   bool
		want     bool
	}{
		{"both on", true, true, true},
		{"platform off", false, true, false},
		{"tenant off", true, false, false},
		{"both off", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Available(tt.platform, tt.tenant); got != tt.want {
				t.Errorf("Available(%v, %v) = %v, want %v", tt.platform, tt.tenant, got, tt.want)
			}
		})
	}
}
