package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func TestValidatePrefs(t *testing.T) {
	grants := []authz.Grant{{Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll}}

	tests := []struct {
		name    string
		inputs  []PrefInput
		wantErr string // "" = no error; "client" or "forbidden" otherwise
	}{
		{name: "empty input rejected", inputs: nil, wantErr: "client"},
		{name: "unknown widget key rejected",
			inputs:  []PrefInput{{WidgetKey: "does.not.exist", Width: 4, Height: 2}},
			wantErr: "client"},
		{name: "widget the caller has no grant for is rejected",
			inputs:  []PrefInput{{WidgetKey: "crm.leads", Width: 4, Height: 2}},
			wantErr: "forbidden"},
		{name: "negative position rejected",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Position: -1, Width: 4, Height: 2}},
			wantErr: "client"},
		{name: "width over MaxWidth rejected",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Width: 13, Height: 2}},
			wantErr: "client"},
		{name: "height under MinSize rejected",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Width: 4, Height: 0}},
			wantErr: "client"},
		{name: "valid input accepted",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Visible: true, Position: 2, Width: 6, Height: 4}},
			wantErr: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ValidatePrefs(tt.inputs, grants)
			switch tt.wantErr {
			case "":
				require.NoError(t, err)
				require.Len(t, out, len(tt.inputs))
				assert.Equal(t, tt.inputs[0].WidgetKey, out[0].WidgetKey)
			case "client":
				require.Error(t, err)
				assert.True(t, IsClientError(err), "expected ClientError, got %T: %v", err, err)
			case "forbidden":
				require.Error(t, err)
				key, ok := IsForbiddenWidgetError(err)
				require.True(t, ok, "expected ForbiddenWidgetError, got %T: %v", err, err)
				assert.Equal(t, tt.inputs[0].WidgetKey, key)
			}
		})
	}
}

func TestValidateConfigUpdates(t *testing.T) {
	tests := []struct {
		name    string
		inputs  []ConfigInput
		wantErr bool
	}{
		{name: "empty rejected", inputs: nil, wantErr: true},
		{name: "unknown key rejected", inputs: []ConfigInput{{WidgetKey: "nope"}}, wantErr: true},
		{name: "known key accepted", inputs: []ConfigInput{{WidgetKey: "sales.quotes", Enabled: false}}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfigUpdates(tt.inputs)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, IsClientError(err))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
