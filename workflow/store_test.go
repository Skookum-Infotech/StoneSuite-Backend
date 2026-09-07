package workflow

import (
	"errors"
	"testing"
)

func TestDisableDependencyError(t *testing.T) {
	tests := []struct {
		name    string
		wf      disableDependencyError
		wantMsg string
	}{
		{
			"single upstream",
			disableDependencyError{workflowName: "Prospect", upstreams: []string{"Lead"}},
			`Cannot disable "Prospect" while "Lead" is still enabled — disable the upstream workflow(s) first.`,
		},
		{
			"multiple upstreams",
			disableDependencyError{workflowName: "Customer", upstreams: []string{"Lead", "Prospect"}},
			`Cannot disable "Customer" while Lead, Prospect are still enabled — disable the upstream workflow(s) first.`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &tc.wf
			if got := err.Error(); got != tc.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tc.wantMsg)
			}
			if !errors.Is(err, ErrDisableDependency) {
				t.Errorf("errors.Is(err, ErrDisableDependency) = false, want true")
			}
		})
	}
}
