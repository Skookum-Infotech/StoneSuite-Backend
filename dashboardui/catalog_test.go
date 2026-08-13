package dashboardui

import "testing"

func TestIsValidWidgetID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"known widget", "kpi-strip", true},
		{"another known widget", "accounting-snapshot", true},
		{"unknown widget", "not-a-widget", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidWidgetID(tt.id); got != tt.want {
				t.Errorf("IsValidWidgetID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestDefaultWidgetIDsAreValid(t *testing.T) {
	for _, id := range DefaultWidgetIDs() {
		if !IsValidWidgetID(id) {
			t.Errorf("default widget id %q is not in the catalog whitelist", id)
		}
	}
}

func TestWidgetIDsReturnsACopy(t *testing.T) {
	got := WidgetIDs()
	got[0] = "mutated"
	if widgetIDs[0] == "mutated" {
		t.Fatal("WidgetIDs() leaked the internal slice -- caller mutation affected package state")
	}
}

func TestDefaultWidgetIDsReturnsACopy(t *testing.T) {
	got := DefaultWidgetIDs()
	got[0] = "mutated"
	if defaultWidgetIDs[0] == "mutated" {
		t.Fatal("DefaultWidgetIDs() leaked the internal slice -- caller mutation affected package state")
	}
}
