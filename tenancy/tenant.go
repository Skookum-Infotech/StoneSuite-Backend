// Package tenancy implements multi-tenant routing: it loads tenants from the
// shared control-plane database and hands out per-tenant connection pools so
// each request talks only to its own tenant's isolated database.
package tenancy

import (
	"time"

	"stonesuite-backend/models"
)

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

// Database design versions (control-plane tenants.design_version). Selects
// which CRM data design serves a tenant's requests; see crmstore.For.
const (
	DesignV1 = "v1" // JSONB workflow_records engine (original)
	DesignV2 = "v2" // relational lkp_* + crm_record design
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

	// DesignVersion selects the CRM data design (DesignV1 / DesignV2).
	DesignVersion string

	// R2Bucket is the Cloudflare R2 bucket provisioned for this tenant's file
	// attachments (ss-{slug}). Must be non-empty — attachment endpoints return
	// 503 when this field is blank.
	R2Bucket string

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

// UnservableReason explains why a tenant cannot be served: a stable
// machine-readable code (models.CodeWorkspace*) and a user-facing message.
// Both are empty for a servable tenant.
func UnservableReason(t *Tenant) (code, message string) {
	if t.Servable() {
		return "", ""
	}
	switch t.Status {
	case StatusSuspended:
		code = models.CodeWorkspaceSuspended
	case StatusDeleted:
		code = models.CodeWorkspaceDeleted
	default:
		code = models.CodeWorkspaceUnavailable
	}
	return code, tenantUnservableMessage(t)
}

// clone returns a deep copy so cached rows are never shared with callers.
func (t *Tenant) clone() *Tenant {
	cp := *t
	if t.DeletedAt != nil {
		d := *t.DeletedAt
		cp.DeletedAt = &d
	}
	if t.HardDeleteAfter != nil {
		h := *t.HardDeleteAfter
		cp.HardDeleteAfter = &h
	}
	return &cp
}
