// Package importer turns an uploaded CSV/XLSX/DOCX/PDF file into staged
// candidate CRM records for review, then commits reviewed rows as real
// records through the exact same crmstore.Store.CreateRecord /
// workflow.ValidateCustomFields path a manual create-record call uses — the
// importer cannot produce a record the normal create-record API would
// reject.
//
// Parsing (go-rag's parse package) and the async worker run outside the
// request path, on the shared control-plane job queue (jobqueue), following
// provisioning's pattern: a worker pool claims only its own job type
// (JobTypeImport), so an import backlog can never starve tenant
// provisioning, or vice versa (see jobqueue.Queue.ClaimNext's per-type
// fairness).
package importer

// JobTypeImport is this package's jobqueue job type.
const JobTypeImport = "import"

// Field-reference prefixes a ColumnMapping target or an LLM extraction
// result key may use — the same "core:"/"cf:" namespace convention the
// query/filter resolvers already use, for consistency.
const (
	corePrefix = "core:"
	cfPrefix   = "cf:"
)

// Row status values for import_rows.status.
const (
	StatusPending   = "pending"
	StatusCommitted = "committed"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

// JobPayload is stored as one async_jobs row's JSON payload
// (jobqueue.Queue.Enqueue) for an import job. TenantID is deliberately not
// part of this struct — jobqueue.Job already carries it as its own column,
// and duplicating it here would just be a second, potentially stale copy.
type JobPayload struct {
	WorkflowKey     string `json:"workflow_key"`
	StorageKey      string `json:"storage_key"`
	FileName        string `json:"file_name"`
	ActorIdentityID string `json:"actor_identity_id"`
	// ColumnMapping maps a tabular source's column header (CSV/XLSX) to a
	// target field reference: "core:<key>" (crmstore.CreateInput.CoreFields),
	// "cf:<key>" (CustomFields, validated against the workflow's
	// FieldDefinition list), or "" / absent to ignore that column. Unused
	// for DOCX/PDF, which extract fields directly via an LLM call instead —
	// see ExtractFields.
	ColumnMapping map[string]string `json:"column_mapping"`
}

// MappedFields is the shape one staged row maps its parsed/extracted data
// onto: core (built-in) and custom (workflow-defined) fields, kept separate
// because they commit through crmstore.CreateInput's two separate maps.
type MappedFields struct {
	Core   map[string]any `json:"core"`
	Custom map[string]any `json:"custom"`
}
