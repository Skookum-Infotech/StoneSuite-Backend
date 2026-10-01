package provisioning

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingProvider is a DBProvider that records DropDatabase calls.
type recordingProvider struct {
	dropped []string
	dropErr error
}

func (p *recordingProvider) CreateDatabase(context.Context, string) error { return nil }
func (p *recordingProvider) DSNFor(string) (string, error)                { return "", nil }
func (p *recordingProvider) DropDatabase(_ context.Context, dbName string) error {
	p.dropped = append(p.dropped, dbName)
	return p.dropErr
}

func TestDropTenantDatabase_DelegatesToProvider(t *testing.T) {
	prov := &recordingProvider{}
	p := New(nil, prov, nil, nil)

	require.NoError(t, p.DropTenantDatabase(context.Background(), "tenant_acme"))

	assert.Equal(t, []string{"tenant_acme"}, prov.dropped)
}

func TestDropTenantDatabase_WrapsProviderError(t *testing.T) {
	boom := errors.New("connection refused")
	prov := &recordingProvider{dropErr: boom}
	p := New(nil, prov, nil, nil)

	err := p.DropTenantDatabase(context.Background(), "tenant_acme")

	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "tenant_acme", "the error names the database it failed on")
}

// A tenant row's db_name is data; a wrong or hand-edited value must never be
// able to drop the control plane, the admin database, or anything else that
// isn't a provisioned tenant database.
func TestDropTenantDatabase_OnlyDropsProvisionedTenantDatabases(t *testing.T) {
	tests := []struct {
		name   string
		dbName string
	}{
		{"empty", ""},
		{"postgres admin db", "postgres"},
		{"control plane style name", "stonesuite"},
		{"prefix only", "tenant_"},
		{"prefix as a substring", "my_tenant_acme"},
		{"uppercase prefix", "TENANT_acme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prov := &recordingProvider{}
			p := New(nil, prov, nil, nil)

			err := p.DropTenantDatabase(context.Background(), tc.dbName)

			require.Error(t, err)
			assert.Empty(t, prov.dropped, "the provider must not be called")
		})
	}
}

func TestValidateTenantDatabaseName(t *testing.T) {
	tests := []struct {
		name    string
		dbName  string
		wantErr bool
	}{
		{"provisioned tenant database", "tenant_acme", false},
		{"multi-word slug", "tenant_north_wind", false},
		{"empty", "", true},
		{"postgres admin db", "postgres", true},
		{"prefix only", "tenant_", true},
		{"prefix as a substring", "my_tenant_acme", true},
		{"uppercase prefix", "TENANT_acme", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTenantDatabaseName(tc.dbName)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.dbName)
				return
			}
			require.NoError(t, err)
		})
	}
}
