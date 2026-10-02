// Package docextractjob is the application glue around the dependency-free
// docextract core: the document_extractions store (tenant DB), the resolvers
// that map extracted text onto customers/items/terms, duplicate and reference
// checks, learned-alias feedback, and the single-goroutine worker + sweeper
// that run on the shared control-plane job queue.
//
// Every query here runs on a tenant pool, so tenant scoping is structural: the
// connection is the scope and no query filters on tenant_id.
package docextractjob

// JobTypeDocExtract is this package's jobqueue job type.
const JobTypeDocExtract = "doc_extract"

// Extraction statuses (document_extractions.status).
const (
	StatusAwaitingUpload = "awaiting_upload"
	StatusQueued         = "queued"
	StatusRunning        = "running"
	StatusReady          = "ready"
	StatusFailed         = "failed"
	StatusDiscarded      = "discarded"
	StatusUsed           = "used"
	StatusAttached       = "attached"
)

// Failure codes the worker records beyond the docextract input codes.
const (
	FailUploadMissing   = "upload_missing"
	FailUploadCorrupted = "upload_corrupted"
	FailTransient       = "transient_error"
	FailInternal        = "internal_error"
)

// Extraction methods recorded beyond docextract's parser / parser+llm.
const (
	MethodCache = "cache"
	methodNone  = "none"
)

// JobPayload is the async_jobs payload of a doc_extract job. The tenant id is
// the job's own column and is deliberately not repeated here.
type JobPayload struct {
	ExtractionID string `json:"extraction_id"`
}
