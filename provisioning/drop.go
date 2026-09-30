package provisioning

import (
	"context"
	"fmt"
	"strings"
)

// ValidateTenantDatabaseName reports whether dbName is one SanitizeDBName could
// have produced (the tenantDBPrefix followed by at least one character). A
// tenant purge checks this up front, before anything is destroyed, so a bad
// db_name can never reach the control plane or admin database.
func ValidateTenantDatabaseName(dbName string) error {
	if !strings.HasPrefix(dbName, tenantDBPrefix) || len(dbName) == len(tenantDBPrefix) {
		return fmt.Errorf("%q is not a provisioned tenant database name", dbName)
	}
	return nil
}

// DropTenantDatabase permanently drops a provisioned tenant database. It is the
// destructive half of a tenant purge, so it re-checks the name with
// ValidateTenantDatabaseName and refuses anything else without contacting the
// provider. Idempotent: the provider treats an already-absent database as
// success.
func (p *Provisioner) DropTenantDatabase(ctx context.Context, dbName string) error {
	if err := ValidateTenantDatabaseName(dbName); err != nil {
		return fmt.Errorf("refusing to drop database: %w", err)
	}
	if err := p.provider.DropDatabase(ctx, dbName); err != nil {
		return fmt.Errorf("drop tenant database %q: %w", dbName, err)
	}
	return nil
}
