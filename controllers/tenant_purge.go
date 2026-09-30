package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// Tenant lifecycle actions accepted by TenantLifecycle.
const (
	lifecycleSuspend     = "suspend"
	lifecycleRestore     = "restore"
	lifecycleDelete      = "delete"
	lifecycleForceDelete = "force-delete"
	lifecyclePurge       = "purge"
)

// lifecycleActions is the set of actions TenantLifecycle applies to a tenant
// (approve/reject and the invites/jobs sub-resources are routed before it).
var lifecycleActions = map[string]bool{
	lifecycleSuspend:     true,
	lifecycleRestore:     true,
	lifecycleDelete:      true,
	lifecycleForceDelete: true,
	lifecyclePurge:       true,
}

// Names of the purge steps, reported to the caller when one fails so the
// platform admin can see how far a retry will have to resume from.
const (
	purgeStepPreflight   = "preflight"
	purgeStepMarkDeleted = "mark-deleted"
	purgeStepStorage     = "storage"
	purgeStepDatabase    = "database"
	purgeStepRecords     = "records"
)

// maxPurgeBodyBytes caps the purge request body ({"confirmSlug": "..."}).
const maxPurgeBodyBytes = 1 << 10

// errPurgeUnsafeDatabase means the tenant's recorded database name isn't one a
// provisioned tenant could have, so it will not be dropped.
var errPurgeUnsafeDatabase = errors.New("tenant database name is not a provisioned tenant database")

// errPurgeUnavailable means a resource the tenant owns cannot be torn down
// because the matching client (R2, Cloudflare, provisioner) isn't configured.
var errPurgeUnavailable = errors.New("tenant teardown is not configured on this server")

// purgeRequest is the body of POST /api/platform/tenants/{id}/purge.
type purgeRequest struct {
	ConfirmSlug string `json:"confirmSlug"`
}

// lifecycleGuard decides whether a platform-admin lifecycle action may run
// against this tenant. It returns (0, "") to allow it, or the HTTP status and
// user-facing message of the refusal. It is the single server-side enforcement
// of "never destroy the platform owner" and of the typed confirmation, so the
// UI's own checks are a convenience, not the safeguard.
func lifecycleGuard(t *tenancy.Tenant, action, confirmSlug string) (int, string) {
	switch action {
	case lifecycleSuspend, lifecycleDelete, lifecycleForceDelete, lifecyclePurge:
		if t.IsPlatformOwner {
			return http.StatusForbidden, "The platform owner workspace cannot be suspended or deleted."
		}
	}
	if action != lifecyclePurge {
		return 0, ""
	}
	if t.Status == tenancy.StatusProvisioning {
		return http.StatusConflict, "This customer is still being provisioned. Wait for provisioning to finish, then try again."
	}
	if confirmSlug == "" || confirmSlug != t.Slug {
		return http.StatusBadRequest, "The confirmation does not match this customer's slug."
	}
	return 0, ""
}

// purgeOps are the destructive operations a purge performs. They are injected
// so the ordering and the abort-before-destroying rules can be tested without a
// database, Cloudflare or R2. A nil emptyBucket, deleteBucket or dropDatabase
// means that teardown isn't configured on this server.
type purgeOps struct {
	// validateDatabase, when set, vets the tenant's database name during the
	// preflight so a bad name is refused before any step runs.
	validateDatabase func(dbName string) error
	markDeleted      func(ctx context.Context, tenantID string) error
	evictPool        func(tenantID string)
	emptyBucket      func(ctx context.Context, bucket string) error
	deleteBucket     func(ctx context.Context, bucket string) error
	dropDatabase     func(ctx context.Context, dbName string) error
	deleteRecords    func(ctx context.Context, tenantID string) error
}

// runPurge permanently removes a tenant and everything it owns. It returns the
// name of the step that failed (empty on success) and the error.
//
// Order matters. It first checks that every teardown the tenant needs is
// available, so a misconfigured server fails before anything is destroyed.
// Then it marks the tenant deleted (immediately unservable), drops external
// resources (bucket, database), and only last deletes the control-plane row —
// that row is what lets the admin retry, so it must outlive every other step.
// Each step is idempotent and the first failure stops the run.
func runPurge(ctx context.Context, t *tenancy.Tenant, ops purgeOps) (string, error) {
	hasBucket := t.R2Bucket != ""
	hasDatabase := t.DBName != ""
	if (hasBucket && (ops.emptyBucket == nil || ops.deleteBucket == nil)) ||
		(hasDatabase && ops.dropDatabase == nil) {
		return purgeStepPreflight, errPurgeUnavailable
	}
	if hasDatabase && ops.validateDatabase != nil {
		if err := ops.validateDatabase(t.DBName); err != nil {
			return purgeStepPreflight, fmt.Errorf("%w: %v", errPurgeUnsafeDatabase, err)
		}
	}

	if err := ops.markDeleted(ctx, t.ID); err != nil {
		return purgeStepMarkDeleted, fmt.Errorf("mark tenant deleted: %w", err)
	}
	ops.evictPool(t.ID)

	if hasBucket {
		if err := ops.emptyBucket(ctx, t.R2Bucket); err != nil {
			return purgeStepStorage, fmt.Errorf("empty bucket %q: %w", t.R2Bucket, err)
		}
		if err := ops.deleteBucket(ctx, t.R2Bucket); err != nil {
			return purgeStepStorage, fmt.Errorf("delete bucket %q: %w", t.R2Bucket, err)
		}
	}
	if hasDatabase {
		if err := ops.dropDatabase(ctx, t.DBName); err != nil {
			return purgeStepDatabase, err
		}
	}
	if err := ops.deleteRecords(ctx, t.ID); err != nil {
		return purgeStepRecords, fmt.Errorf("delete tenant records: %w", err)
	}
	return "", nil
}

// WithR2Client wires the R2 object-storage client so a purge can empty a
// tenant's bucket before Cloudflare deletes it.
func (h *TenantOps) WithR2Client(r2 *storage.Client) *TenantOps {
	h.R2 = r2
	return h
}

// evictTenantPool closes the cached database pool for a tenant that has just
// been suspended, deleted or purged (Router.Evict is a no-op for an unknown id).
func (h *TenantOps) evictTenantPool(tenantID string) {
	if h.Router != nil {
		h.Router.Evict(tenantID)
	}
}

// purgeOps wires runPurge to this handler's real clients. Teardown that isn't
// configured is left nil, which runPurge reports as errPurgeUnavailable when
// the tenant actually owns that resource.
func (h *TenantOps) purgeOps() purgeOps {
	ops := purgeOps{
		validateDatabase: provisioning.ValidateTenantDatabaseName,
		markDeleted: func(ctx context.Context, id string) error {
			return h.CP.MarkTenantDeleted(ctx, id, time.Now())
		},
		evictPool:     h.evictTenantPool,
		deleteRecords: h.CP.PurgeTenant,
	}
	if h.R2.IsConfigured() {
		ops.emptyBucket = func(ctx context.Context, bucket string) error {
			_, err := h.R2.WithBucket(bucket).EmptyBucket(ctx)
			return err
		}
	}
	if h.CF != nil && h.CF.IsConfigured() {
		ops.deleteBucket = h.CF.DeleteBucket
	}
	if h.Prov != nil {
		ops.dropDatabase = h.Prov.DropTenantDatabase
	}
	return ops
}

// readPurgeConfirmation reads the typed slug from the request body. An empty
// body is fine here (lifecycleGuard then rejects the missing confirmation);
// malformed JSON is a client error, already reported to w when ok is false.
func readPurgeConfirmation(w http.ResponseWriter, r *http.Request) (confirmSlug string, ok bool) {
	var body purgeRequest
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPurgeBodyBytes)).Decode(&body)
	if err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "Expected a JSON body with confirmSlug.")
		return "", false
	}
	return body.ConfirmSlug, true
}

// purgeTenant runs the purge and writes the response. On failure it says which
// step stopped; every step is idempotent, so the admin simply runs it again.
func (h *TenantOps) purgeTenant(w http.ResponseWriter, r *http.Request, admin middleware.UserContextPayload, tenant *tenancy.Tenant) {
	step, err := runPurge(r.Context(), tenant, h.purgeOps())
	if err != nil {
		log.Printf("ERROR: purge of tenant %s (%s) stopped at step %s: %v", tenant.Slug, tenant.ID, step, err)
		if errors.Is(err, errPurgeUnsafeDatabase) {
			fail(w, http.StatusConflict,
				"This customer's database name doesn't look like a provisioned tenant database, so it won't be dropped automatically. Nothing was changed.")
			return
		}
		if errors.Is(err, errPurgeUnavailable) {
			fail(w, http.StatusServiceUnavailable,
				"Permanent deletion isn't available: this server has no storage or database teardown configured for this customer. Nothing was changed.")
			return
		}
		fail(w, http.StatusInternalServerError,
			"Permanent deletion stopped at the "+step+" step. Run it again to resume; nothing further was removed.")
		return
	}
	// The tenant is gone, so the audit row carries no tenant_id; its details
	// are the surviving record of what was removed.
	if err := h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, "", "tenant.purge", purgeAuditDetails(tenant)); err != nil {
		log.Printf("WARNING: audit log for purge of tenant %s failed: %v", tenant.Slug, err)
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Tenant purge applied."})
}

// purgeAuditDetails is what the audit log keeps about a purged tenant. The row
// it is written to has no tenant_id (the tenant no longer exists), so this is
// the only surviving record of which workspace was removed.
func purgeAuditDetails(t *tenancy.Tenant) string {
	b, err := json.Marshal(map[string]string{
		"tenantId":    t.ID,
		"slug":        t.Slug,
		"displayName": t.DisplayName,
		"dbName":      t.DBName,
		"r2Bucket":    t.R2Bucket,
	})
	if err != nil {
		return "{}"
	}
	return string(b)
}
