// Package tenancy implements multi-tenant routing: it loads tenants from the
// shared control-plane database and hands out per-tenant connection pools so
// each request talks only to its own tenant's isolated database.
package tenancy

import "time"

// Tenant status values (control-plane tenants.status).
const (
	StatusInvited      = "invited"
	StatusSubmitted    = "submitted" // customer filled the onboarding form; awaiting approval
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusSuspended    = "suspended"
	StatusRejected     = "rejected" // onboarding application declined by the platform owner
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

	// Metadata holds the onboarding submission (company profile, contacts, and
	// any dynamic custom fields) as a JSON object string.
	Metadata string

	DeletedAt       *time.Time
	HardDeleteAfter *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Servable reports whether requests for this tenant may be served. A tenant is
// only servable when it is active, its migrations are current, and it has a
// provisioned database. A tenant with no DB (seeded but not yet provisioned)
// is treated as unservable so callers get a friendly "still being set up"
// message instead of a raw connection error.
func (t *Tenant) Servable() bool {
	return t.Status == StatusActive && t.MigrationStatus == MigrationOK && t.DBName != ""
}
