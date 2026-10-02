package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/aisettings"
	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// Machine-readable codes on document-extraction responses.
const (
	codeAIDisabled   = "ai_disabled"
	codeDailyLimit   = "daily_limit"
	codeStorageFull  = "storage_full"
	codeUploadMiss   = "upload_missing"
	codeAlreadyUsed  = "already_used"
	codeExpired      = "expired"
	codeInvalidState = "invalid_state"
)

// Limits and security-event names of the document-extraction handlers.
const (
	// maxDocExtractBodyBytes bounds every small JSON request body here.
	maxDocExtractBodyBytes = 64 << 10
	sha256HexLen           = 64
	// eventDocExtractIDOR is logged when an extraction id exists for another owner.
	eventDocExtractIDOR = "doc_extract_idor"
	// auditActionCreatedFromDocument is the audit action written on the created record's own trail.
	auditActionCreatedFromDocument = "created_from_document"
)

// docExtractResources maps the supported doc types to the RBAC resource that
// gates them. Only sales_order is accepted in this slice.
var docExtractResources = map[docextract.DocType]authz.Resource{
	docextract.DocTypeSalesOrder: authz.ResourceSalesOrder,
}

// docExtractUnsupported are valid doc types whose handlers are not built yet.
var docExtractUnsupported = map[docextract.DocType]struct{}{
	docextract.DocTypePurchaseOrder: {},
	docextract.DocTypeVendorBill:    {},
}

// docExtractUploads maps an accepted upload extension to its content type.
var docExtractUploads = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

// docExtractStore is the slice of docextractjob.Store the handlers use.
type docExtractStore interface {
	Create(ctx context.Context, p docextractjob.CreateParams) (*docextractjob.Extraction, error)
	Get(ctx context.Context, id, ownerIdentityID string) (*docextractjob.Extraction, error)
	GetInternal(ctx context.Context, id string) (*docextractjob.Extraction, error)
	ListReadyForOwner(ctx context.Context, ownerIdentityID string) ([]docextractjob.Extraction, error)
	Discard(ctx context.Context, id, ownerIdentityID string) (string, error)
	SetNotify(ctx context.Context, id, ownerIdentityID string, notify bool) error
	MarkQueued(ctx context.Context, id, ownerIdentityID, clientSHA256, jobID string) (string, bool, error)
	CompleteCAS(ctx context.Context, id, ownerIdentityID, recordUUID string) error
	CountCreatedToday(ctx context.Context) (int, error)
	SumPendingStagingBytes(ctx context.Context) (int64, error)
}

// docExtractObjects is the slice of the tenant-bucket R2 client the handlers use.
type docExtractObjects interface {
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
	Head(ctx context.Context, key string) (storage.ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// docExtractQueue enqueues the extraction job (jobqueue.Queue).
type docExtractQueue interface {
	Enqueue(ctx context.Context, jobType string, tenantID string, payload any, idempotencyKey string) (string, error)
}

// DocExtractSettings is the handler tuning, injected so the package never
// reads the global config.AppConfig.
type DocExtractSettings struct {
	Enabled         bool
	MaxBytes        int64
	DailyCap        int
	StagingMaxBytes int64
	StagingTTL      time.Duration
	PresignTTL      time.Duration
}

// DocExtractSettingsFromApp builds DocExtractSettings from the app config.
func DocExtractSettingsFromApp(c config.Config) DocExtractSettings {
	return DocExtractSettings{
		Enabled:         c.DocExtractEnabled,
		MaxBytes:        c.DocExtractMaxBytes,
		DailyCap:        c.DocExtractDailyCap,
		StagingMaxBytes: c.DocExtractStagingMaxBytes,
		StagingTTL:      c.DocExtractStagingTTL,
		PresignTTL:      c.DocExtractPresignTTL,
	}
}

// DocExtractOps serves "create from document": upload -> extract -> review.
//
// Routes (all behind tenantChain; portal tokens never reach them because
// middleware.RequireAuth confines kind="portal" tokens to /api/portal/):
//
//	POST /api/tenant/document-extractions
//	GET  /api/tenant/document-extractions?status=ready
//	GET  /api/tenant/document-extractions/{id}
//	POST /api/tenant/document-extractions/{id}/presign
//	POST /api/tenant/document-extractions/{id}/start
//	POST /api/tenant/document-extractions/{id}/notify
//	POST /api/tenant/document-extractions/{id}/discard
//	POST /api/tenant/document-extractions/{id}/complete
//
// Every handler checks the rollout flag (off -> 404), then the caller's
// Require(<docType resource>, create); an extraction is owner-only and any
// other owner's id is a 404.
type DocExtractOps struct {
	cfg      DocExtractSettings
	r2       *storage.Client
	queue    docExtractQueue
	cpPool   *pgxpool.Pool
	settings *aisettings.Cache

	// Seams, defaulted by the constructor, replaced in unit tests.
	identify    func(r *http.Request) (*docExtractCaller, int, string)
	check       func(ctx context.Context, c *docExtractCaller, res authz.Resource, act authz.Action) (authz.Decision, error)
	newStore    func(pool *pgxpool.Pool) docExtractStore
	objectsFor  func(tenant *tenancy.Tenant) docExtractObjects
	aiAvailable func(ctx context.Context, caller *docExtractCaller) (bool, error)
}

// NewDocExtractOps constructs the handler group. r2 may be nil (the endpoints
// then answer 503); settings is the process-wide AI-toggle cache.
func NewDocExtractOps(r2 *storage.Client, queue docExtractQueue, cpPool *pgxpool.Pool, settings *aisettings.Cache, cfg DocExtractSettings) *DocExtractOps {
	h := &DocExtractOps{cfg: cfg, r2: r2, queue: queue, cpPool: cpPool, settings: settings}
	h.newStore = func(pool *pgxpool.Pool) docExtractStore { return docextractjob.NewStore(pool) }
	h.identify = h.identifyFromContext
	h.check = checkFromPool
	h.objectsFor = h.r2Objects
	h.aiAvailable = h.settingsAvailable
	return h
}

// r2Objects returns the tenant's bucket client, or a nil interface (never a
// typed nil) when R2 or the tenant bucket is not configured.
func (h *DocExtractOps) r2Objects(tenant *tenancy.Tenant) docExtractObjects {
	c := h.r2.WithBucket(tenant.R2Bucket)
	if c == nil {
		return nil
	}
	return c
}

// settingsAvailable reports platform && tenant AI availability.
func (h *DocExtractOps) settingsAvailable(ctx context.Context, caller *docExtractCaller) (bool, error) {
	st, err := h.settings.Status(ctx, h.cpPool, caller.pool, caller.tenant.ID)
	if err != nil {
		return false, err
	}
	return st.Available, nil
}

// enabled answers 404 and returns false when the rollout flag is off.
func (h *DocExtractOps) enabled(w http.ResponseWriter) bool {
	if !h.cfg.Enabled {
		fail(w, http.StatusNotFound, "Not found.")
		return false
	}
	return true
}
