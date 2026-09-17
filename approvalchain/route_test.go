package approvalchain

import "testing"

func TestResourceRoute(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		recordID string
		want     string
	}{
		{name: "sales domain resource", resource: "invoice", recordID: "rec-1", want: "/sales/invoice/rec-1"},
		{name: "purchases domain resource", resource: "vendor_bill", recordID: "rec-2", want: "/purchases/vendor_bill/rec-2"},
		{name: "installation module differs from domain", resource: "installation", recordID: "rec-3", want: "/sales/installation/rec-3"},
		{name: "unregistered resource falls back to empty", resource: "quote", recordID: "rec-4", want: ""},
		{name: "unknown resource falls back to empty", resource: "not-a-resource", recordID: "rec-5", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resourceRoute(tt.resource, tt.recordID)
			if got != tt.want {
				t.Errorf("resourceRoute(%q, %q) = %q, want %q", tt.resource, tt.recordID, got, tt.want)
			}
		})
	}
}
