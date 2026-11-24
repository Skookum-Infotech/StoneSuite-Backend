package expense

import "testing"

func TestBillLineDescription(t *testing.T) {
	tests := []struct {
		name, category, date, description, want string
	}{
		{"with description", "Travel", "2026-11-02", "Taxi to site", "Travel - Taxi to site (2026-11-02)"},
		{"blank description", "Meals", "2026-11-03", "", "Meals (2026-11-03)"},
		{"whitespace description", "Meals", "2026-11-03", "   ", "Meals (2026-11-03)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := billLineDescription(tt.category, tt.date, tt.description); got != tt.want {
				t.Errorf("billLineDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}
