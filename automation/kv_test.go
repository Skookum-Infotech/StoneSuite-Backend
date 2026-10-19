package automation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKVMirrorRequiresAllValues(t *testing.T) {
	tests := []struct{ name, acct, tok, ns string }{
		{"no account", "", "t", "n"},
		{"no token", "a", "", "n"},
		{"no namespace", "a", "t", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewKVMirror(tt.acct, tt.tok, tt.ns)
			assert.Nil(t, m)
			assert.False(t, m.Configured())
			assert.Error(t, m.Set(context.Background(), true))
		})
	}
	assert.True(t, NewKVMirror("a", "t", "n").Configured())
}

func TestKVMirrorSet(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		status  int
		want    string
		wantErr bool
	}{
		{"on", true, http.StatusOK, KVOn, false},
		{"off", false, http.StatusOK, KVOff, false},
		{"cloudflare rejects", true, http.StatusForbidden, KVOn, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth, gotBody, gotMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"success":false}`))
			}))
			defer srv.Close()
			m := NewKVMirror("acct1", "secret-token-not-real", "ns1")
			m.baseURL = srv.URL

			err := m.Set(context.Background(), tt.enabled)

			assert.Equal(t, http.MethodPut, gotMethod)
			assert.Equal(t, "/accounts/acct1/storage/kv/namespaces/ns1/values/"+KVEnabledKey, gotPath)
			assert.Equal(t, "Bearer secret-token-not-real", gotAuth)
			assert.Equal(t, tt.want, gotBody)
			if tt.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "secret-token-not-real")
				assert.Contains(t, err.Error(), "403")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
