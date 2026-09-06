package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/importer"
	"stonesuite-backend/jobqueue"
	"stonesuite-backend/middleware"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// ImportOps serves the document importer: upload → stage → review → commit.
// See importer/ for the package doc; every handler here goes through
// resourceForKey(workflowKey)'s permission (the same catalog entry a manual
// create-record call checks) plus a strict owner-or-all-scope IDOR guard on
// the underlying job, following authCRMByRecordID's pattern.
//
// Routes (all behind RequireAuth + tenancy resolver):
//
//	POST /api/tenant/import/presign
//	POST /api/tenant/import/jobs
//	GET  /api/tenant/import/jobs
//	GET  /api/tenant/import/jobs/{jobId}
//	GET  /api/tenant/import/jobs/{jobId}/rows
//	PATCH /api/tenant/import/jobs/{jobId}/rows/{rowId}
//	POST /api/tenant/import/jobs/{jobId}/commit
type ImportOps struct {
	r2    *storage.Client // nil when R2 is not configured (graceful degradation)
	queue *jobqueue.Queue
}

// NewImportOps constructs the handler group. r2 may be nil when R2
// credentials are absent; the presign endpoint then returns 503.
func NewImportOps(r2 *storage.Client, queue *jobqueue.Queue) *ImportOps {
	return &ImportOps{r2: r2, queue: queue}
}

const (
	maxImportFileSizeBytes = 25 * 1024 * 1024 // matches attachments.go's maxFileSizeBytes
	importPresignTTL       = 5 * time.Minute
)

// importAllowedExt maps an accepted upload extension to its canonical
// content-type — CSV is import-only (attachments.go's allowlist has no use
// for a headers-and-rows format), so this is a separate table rather than a
// reuse of allowedExt/allowedMIME.
var importAllowedExt = map[string]string{
	".csv":  "text/csv",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pdf":  "application/pdf",
}

// r2ForTenant returns an R2 client scoped to the tenant's dedicated bucket.
func (h *ImportOps) r2ForTenant(tenant *tenancy.Tenant) *storage.Client {
	return h.r2.WithBucket(tenant.R2Bucket)
}

// importAuth resolves JWT + tenant pool + tenant record, and checks the
// resourceForKey(workflowKey) permission for action — the same gate a manual
// create-record call on that workflow uses. Returns pool, tenant, identityID,
// the caller's RBAC scope for that resource, and ok.
func importAuth(w http.ResponseWriter, r *http.Request, workflowKey string, action authz.Action) (*pgxpool.Pool, *tenancy.Tenant, string, authz.Scope, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, "", "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, nil, "", "", false
	}
	resource := resourceForKey(workflowKey)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" "+workflowKey+".")
		return nil, nil, "", "", false
	}
	return pool, tenant, payload.ID, decision.Scope, true
}

// jobForCaller loads jobID, verifies it belongs to this tenant and is an
// import job, then enforces the same "own vs all" scope authCRMByRecordID
// uses for CRM records: a caller whose grant is scoped to "own" may only act
// on jobs they themselves started, resolved via the job's stored
// ActorIdentityID (jobs have no separate ownership column — the payload is
// the only place this lives). Denial is always 404, never 403, so a guessed
// job id cannot be distinguished from one belonging to someone else.
// Returns the job, its decoded payload, and ok.
func jobForCaller(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, queue *jobqueue.Queue, tenant *tenancy.Tenant, identityID string, jobID string, action authz.Action) (*jobqueue.Job, importer.JobPayload, bool) {
	job, err := queue.Get(r.Context(), jobID)
	if errors.Is(err, jobqueue.ErrNoJob) {
		fail(w, http.StatusNotFound, "Import job not found.")
		return nil, importer.JobPayload{}, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load import job.")
		return nil, importer.JobPayload{}, false
	}
	if job.JobType != importer.JobTypeImport || job.TenantID == nil || *job.TenantID != tenant.ID {
		logSecurityEvent(r, "idor_denied", "identity", identityID, "job", jobID)
		fail(w, http.StatusNotFound, "Import job not found.")
		return nil, importer.JobPayload{}, false
	}

	var jp importer.JobPayload
	if err := json.Unmarshal(job.Payload, &jp); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load import job.")
		return nil, importer.JobPayload{}, false
	}

	resource := resourceForKey(jp.WorkflowKey)
	decision, err := authz.Check(r.Context(), pool, identityID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, importer.JobPayload{}, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" "+jp.WorkflowKey+".")
		return nil, importer.JobPayload{}, false
	}
	if decision.Scope != authz.ScopeAll && jp.ActorIdentityID != identityID {
		logSecurityEvent(r, "idor_denied", "identity", identityID, "job", jobID, "resource", string(resource), "action", string(action))
		fail(w, http.StatusNotFound, "Import job not found.")
		return nil, importer.JobPayload{}, false
	}
	return job, jp, true
}

// ---- POST /api/tenant/import/presign ---------------------------------------

type importPresignRequest struct {
	WorkflowKey string `json:"workflowKey"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// Presign validates the upload's metadata and returns one presigned PUT URL.
// RBAC: resourceForKey(workflowKey):create — importing creates records.
func (h *ImportOps) Presign(w http.ResponseWriter, r *http.Request) {
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	var req importPresignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.WorkflowKey == "" {
		fail(w, http.StatusBadRequest, "workflowKey is required.")
		return
	}
	_, tenant, _, _, ok := importAuth(w, r, req.WorkflowKey, authz.ActionCreate)
	if !ok {
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

	ext := strings.ToLower(filepath.Ext(req.FileName))
	expectedCT, ok := importAllowedExt[ext]
	if !ok {
		fail(w, http.StatusBadRequest, fmt.Sprintf("File type %q is not supported (allowed: csv, xlsx, docx, pdf).", ext))
		return
	}
	if req.ContentType != expectedCT {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Content type %q does not match file extension %q.", req.ContentType, ext))
		return
	}
	if req.SizeBytes <= 0 || req.SizeBytes > maxImportFileSizeBytes {
		fail(w, http.StatusBadRequest, fmt.Sprintf("File must be between 1 byte and %d MB.", maxImportFileSizeBytes/1024/1024))
		return
	}

	fileUUID, err := newAttachUUID()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate file key.")
		return
	}
	storageKey := fmt.Sprintf("%s/imports/%s%s", tenant.Slug, fileUUID, ext)
	uploadURL, err := h.r2ForTenant(tenant).PresignPut(r.Context(), storageKey, req.ContentType, importPresignTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate upload URL.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"storageKey": storageKey,
		"uploadUrl":  uploadURL,
	})
}

// ---- POST /api/tenant/import/jobs ------------------------------------------

type createImportJobRequest struct {
	WorkflowKey   string            `json:"workflowKey"`
	StorageKey    string            `json:"storageKey"`
	FileName      string            `json:"fileName"`
	ColumnMapping map[string]string `json:"columnMapping"`
}

// CreateJob validates the mapping against the target workflow's field
// definitions and enqueues an import job. RBAC: resourceForKey(workflowKey):create.
func (h *ImportOps) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req createImportJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.WorkflowKey == "" || req.StorageKey == "" || req.FileName == "" {
		fail(w, http.StatusBadRequest, "workflowKey, storageKey, and fileName are required.")
		return
	}
	pool, tenant, identityID, _, ok := importAuth(w, r, req.WorkflowKey, authz.ActionCreate)
	if !ok {
		return
	}
	// storageKey must be one this tenant's own presign call minted — never
	// trust a client-supplied key blindly, or one tenant could point another
	// tenant's job at its own bucket path.
	if !strings.HasPrefix(req.StorageKey, tenant.Slug+"/imports/") {
		fail(w, http.StatusBadRequest, "Invalid storage key.")
		return
	}

	if len(req.ColumnMapping) > 0 {
		def, err := importer.LoadWorkflowDefinition(r.Context(), pool, req.WorkflowKey)
		if err != nil {
			fail(w, http.StatusBadRequest, "Unknown workflow.")
			return
		}
		if err := importer.ValidateMapping(req.ColumnMapping, def.Fields); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	jobPayload := importer.JobPayload{
		WorkflowKey:     req.WorkflowKey,
		StorageKey:      req.StorageKey,
		FileName:        req.FileName,
		ActorIdentityID: identityID,
		ColumnMapping:   req.ColumnMapping,
	}
	jobID, err := h.queue.Enqueue(r.Context(), importer.JobTypeImport, tenant.ID, jobPayload, "import:"+req.StorageKey)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to start import.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "jobId": jobID})
}

// ---- GET /api/tenant/import/jobs -------------------------------------------

// listImportJobsLimit bounds how many of the tenant's most recent jobs (of
// any type) are scanned before filtering to import jobs — generous enough
// that a busy tenant's import history isn't truncated by provisioning noise,
// small enough to stay a single cheap query.
const listImportJobsLimit = 200

type importJobSummary struct {
	ID          string          `json:"id"`
	WorkflowKey string          `json:"workflowKey"`
	FileName    string          `json:"fileName"`
	Status      string          `json:"status"`
	Progress    json.RawMessage `json:"progress,omitempty"`
	LastError   *string         `json:"lastError,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// ListJobs returns the caller's own import jobs targeting workflowKey (or
// every job on it, for an all-scope caller). RBAC: resourceForKey(workflowKey):read.
func (h *ImportOps) ListJobs(w http.ResponseWriter, r *http.Request) {
	workflowKey := r.URL.Query().Get("workflowKey")
	if workflowKey == "" {
		fail(w, http.StatusBadRequest, "workflowKey query parameter is required.")
		return
	}
	_, tenant, identityID, scope, ok := importAuth(w, r, workflowKey, authz.ActionRead)
	if !ok {
		return
	}

	jobs, err := h.queue.ListForTenant(r.Context(), tenant.ID, listImportJobsLimit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list import jobs.")
		return
	}

	out := make([]importJobSummary, 0, len(jobs))
	for _, job := range jobs {
		if job.JobType != importer.JobTypeImport {
			continue
		}
		var jp importer.JobPayload
		if err := json.Unmarshal(job.Payload, &jp); err != nil {
			continue
		}
		if jp.WorkflowKey != workflowKey {
			continue
		}
		if scope != authz.ScopeAll && jp.ActorIdentityID != identityID {
			continue
		}
		out = append(out, importJobSummary{
			ID: job.ID, WorkflowKey: jp.WorkflowKey, FileName: jp.FileName,
			Status: job.Status, Progress: job.Progress, LastError: job.LastError,
			CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "jobs": out})
}

// ---- GET /api/tenant/import/jobs/{jobId} -----------------------------------

// GetJob returns one import job's status and progress.
func (h *ImportOps) GetJob(w http.ResponseWriter, r *http.Request) {
	pool, tenant, identityID, ok := importAuthAny(w, r)
	if !ok {
		return
	}
	job, jp, ok := jobForCaller(w, r, pool, h.queue, tenant, identityID, r.PathValue("jobId"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "job": importJobSummary{
		ID: job.ID, WorkflowKey: jp.WorkflowKey, FileName: jp.FileName,
		Status: job.Status, Progress: job.Progress, LastError: job.LastError,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}})
}

// ---- GET /api/tenant/import/jobs/{jobId}/rows ------------------------------

// ListRows returns every staged row of jobId, for review.
func (h *ImportOps) ListRows(w http.ResponseWriter, r *http.Request) {
	pool, tenant, identityID, ok := importAuthAny(w, r)
	if !ok {
		return
	}
	_, _, ok = jobForCaller(w, r, pool, h.queue, tenant, identityID, r.PathValue("jobId"), authz.ActionRead)
	if !ok {
		return
	}
	rows, err := importer.NewStore(pool).ListByJob(r.Context(), r.PathValue("jobId"))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load staged rows.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "rows": rows})
}

// ---- PATCH /api/tenant/import/jobs/{jobId}/rows/{rowId} --------------------

type updateImportRowRequest struct {
	Mapped *importer.MappedFields `json:"mapped,omitempty"`
	Skip   bool                   `json:"skip,omitempty"`
}

// UpdateRow corrects one staged row's mapped fields (resets it to pending for
// the next commit attempt) or marks it skipped. RBAC:
// resourceForKey(workflowKey):update.
func (h *ImportOps) UpdateRow(w http.ResponseWriter, r *http.Request) {
	pool, tenant, identityID, ok := importAuthAny(w, r)
	if !ok {
		return
	}
	jobID := r.PathValue("jobId")
	_, _, ok = jobForCaller(w, r, pool, h.queue, tenant, identityID, jobID, authz.ActionUpdate)
	if !ok {
		return
	}

	rowID := r.PathValue("rowId")
	rows := importer.NewStore(pool)
	row, found, err := rows.GetRow(r.Context(), rowID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load row.")
		return
	}
	if !found || row.JobID != jobID {
		logSecurityEvent(r, "idor_denied", "identity", identityID, "job", jobID, "row", rowID)
		fail(w, http.StatusNotFound, "Row not found.")
		return
	}

	var req updateImportRowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Skip {
		if err := rows.MarkSkipped(r.Context(), rowID); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to skip row.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	if req.Mapped == nil {
		fail(w, http.StatusBadRequest, "mapped or skip is required.")
		return
	}
	if err := rows.UpdateMapped(r.Context(), rowID, *req.Mapped); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update row.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ---- POST /api/tenant/import/jobs/{jobId}/commit ---------------------------

// Commit creates a real CRM record for every reviewed, still-pending staged
// row via importer.CommitJob. RBAC: resourceForKey(workflowKey):create — this
// is where records actually get created.
func (h *ImportOps) Commit(w http.ResponseWriter, r *http.Request) {
	pool, tenant, identityID, ok := importAuthAny(w, r)
	if !ok {
		return
	}
	jobID := r.PathValue("jobId")
	_, jp, ok := jobForCaller(w, r, pool, h.queue, tenant, identityID, jobID, authz.ActionCreate)
	if !ok {
		return
	}

	st, _, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	summary, err := importer.CommitJob(r.Context(), pool, st, importer.NewStore(pool), jobID, jp.WorkflowKey, identityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to commit import.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "summary": summary})
}

// importAuthAny resolves JWT + tenant pool + tenant record for a job-scoped
// endpoint that has no workflowKey until after the job is loaded — RBAC and
// ownership are then enforced by jobForCaller once the job's own workflowKey
// is known.
func importAuthAny(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, *tenancy.Tenant, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, nil, "", false
	}
	return pool, tenant, payload.ID, true
}
