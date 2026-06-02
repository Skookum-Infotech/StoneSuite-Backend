// Package tenancy implements multi-tenant routing: it loads tenants from the
// shared control-plane database and hands out per-tenant connection pools so
// each request talks only to its own tenant's isolated database.
package tenancy

import "time"

// Tenant status values (control-plane tenants.status).
const (
	StatusInvited      = "invited"
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusSuspended    = "suspended"
	StatusDeleted      = "deleted"
)

// Migration status values (control-plane tenants.migration_status).
const (
	MigrationPending = "pending"
	MigrationOK      = "ok"
	MigrationFailed  = "failed"
)

// Tenant is the control-plane view of a customer organization, including the
// routing info needed to reach its isolated database.
type Tenant struct {
	ID              string
	Slug            string
	DisplayName     string
	Status          string
	IsPlatformOwner bool

	DBName          string
	DBConnectionRef string // secret-manager key or encrypted DSN (decrypted at use)
	Region          string

	SchemaVersion   int
	MigrationStatus string

	DeletedAt       *time.Time
	HardDeleteAfter *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Servable reports whether requests for this tenant may be served. A tenant is
// only servable when it is active and its database migrations are current.
func (t *Tenant) Servable() bool {
	return t.Status == StatusActive && t.MigrationStatus == MigrationOK
}
