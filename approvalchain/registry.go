// Package approvalchain is the single source of truth for which workflows
// have a configurable module approval chain, which lkp_record_status
// code(s) gate them, and (via engine.go) the generic Approve/GetInfo
// implementation every relational module built on this registry shares.
// Every module registered here stores its approver config in a table shaped
// exactly like estimate_approver: record_type_id, record_status_id,
// approver_employee_id, is_active, created_at, created_by (see
// database/migrations/tenant/schema.sql). That uniform shape is what lets
// store.go and engine.go serve every module generically instead of one
// handler per module.
//
// Estimate, Quote and Sales Order predate engine.go and keep their own
// copy-pasted approval.go (proven correct, already shipped) rather than
// being retrofitted onto it -- their registry entries below are still kept
// fully accurate (including Record) since GetApprovalChain/SetApprovalChain
// (controllers/approvalchain.go, backing Configure > Workflows) reads from
// this registry regardless of which modules use the engine.
//
// CRM (lead/prospect/customer) is intentionally not registered here — it has
// no single "Approved" status to gate, and keeps its existing per-status
// config (crm_workflow_approver, controllers/crm_admin.go) untouched.
package approvalchain

// Gate identifies one point in a module's status flow where sign-off from
// the module's configured approvers is required before a record may leave
// that status, and where Approve auto-advances the record to once approval
// is fully resolved. Most modules have exactly one gate; Fabrication Job has
// two (Templating and QC Pending), so this is a slice rather than a single
// value.
type Gate struct {
	StatusCode       string // lkp_record_status.record_status_code, scoped to RecordTypeCode -- the gated status
	TargetStatusCode string // the status Approve moves the record to once resolved, e.g. "APPV"
}

// ModuleConfig maps one workflows.key to the relational module's approval
// gate(s), approver/approval table names, and record table shape.
type ModuleConfig struct {
	RecordTypeCode string // lkp_record_type.record_type_code
	ApproverTable  string // e.g. "estimate_approver" -- uniform 4-column shape, see package doc
	ApprovalTable  string // e.g. "estimate_approval" -- per-approver sign-off rows
	Record         RecordSpec
	Gates          []Gate
}

// HasGate reports whether statusCode is one of this module's configured
// gates.
func (m ModuleConfig) HasGate(statusCode string) bool {
	_, ok := m.GateFor(statusCode)
	return ok
}

// GateFor returns the configured gate whose StatusCode matches statusCode,
// and false if that status isn't gated for this module.
func (m ModuleConfig) GateFor(statusCode string) (Gate, bool) {
	for _, g := range m.Gates {
		if g.StatusCode == statusCode {
			return g, true
		}
	}
	return Gate{}, false
}

// registry is keyed by workflows.key (see the seed INSERT INTO workflows
// statements in database/migrations/tenant/schema.sql for the canonical key
// list). Adding a new module here requires that module to already have its
// own <name>_approver and <name>_approval tables of the uniform shape
// described in the package doc -- this registry does not create tables.
var registry = map[string]ModuleConfig{
	"estimate": {
		RecordTypeCode: "ESTM", ApproverTable: "estimate_approver", ApprovalTable: "estimate_approval",
		Record: RecordSpec{
			Table: "estimate", HistoryTable: "estimate_history",
			IDColumn: "estimate_id", UUIDColumn: "estimate_uuid", StatusColumn: "estimate_status",
			ApprovalStatusColumn: "estimate_approval_status", ApprovedByColumn: "estimate_approved_by",
			UpdatedAtColumn: "estimate_updated_at", UpdatedByColumn: "estimate_updated_by",
			RecordVersionColumn: "estimate_record_version", DeletedAtColumn: "estimate_deleted_at",
		},
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"quote": {
		RecordTypeCode: "QUOT", ApproverTable: "quote_approver", ApprovalTable: "quote_approval",
		Record: RecordSpec{
			Table: "quote", HistoryTable: "quote_history",
			IDColumn: "quote_id", UUIDColumn: "quote_uuid", StatusColumn: "quote_status",
			ApprovalStatusColumn: "quote_approval_status", ApprovedByColumn: "quote_approved_by",
			UpdatedAtColumn: "quote_updated_at", UpdatedByColumn: "quote_updated_by",
			RecordVersionColumn: "quote_record_version", DeletedAtColumn: "quote_deleted_at",
		},
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"sales_order": {
		RecordTypeCode: "SORD", ApproverTable: "sales_order_approver", ApprovalTable: "sales_order_approval",
		Record: RecordSpec{
			Table: "sales_order", HistoryTable: "sales_order_history",
			IDColumn: "sales_order_id", UUIDColumn: "sales_order_uuid", StatusColumn: "sales_order_status",
			ApprovalStatusColumn: "sales_order_approval_status", ApprovedByColumn: "sales_order_approved_by",
			UpdatedAtColumn: "sales_order_updated_at", UpdatedByColumn: "sales_order_updated_by",
			RecordVersionColumn: "sales_order_record_version", DeletedAtColumn: "sales_order_deleted_at",
		},
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"purchase_order": {
		RecordTypeCode: "PORD", ApproverTable: "purchase_order_approver", ApprovalTable: "purchase_order_approval",
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"requisition": {
		RecordTypeCode: "REQN", ApproverTable: "requisition_approver", ApprovalTable: "requisition_approval",
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"vendor_bill": {
		RecordTypeCode: "VBIL", ApproverTable: "vendor_bill_approver", ApprovalTable: "vendor_bill_approval",
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"vendor_payment": {
		RecordTypeCode: "VPAY", ApproverTable: "vendor_payment_approver", ApprovalTable: "vendor_payment_approval",
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"expense": {
		RecordTypeCode: "EXPN", ApproverTable: "expense_approver", ApprovalTable: "expense_approval",
		Gates: []Gate{{StatusCode: "SUBM", TargetStatusCode: "APPV"}},
	},
	"installation": {
		RecordTypeCode: "FJOB", ApproverTable: "fabrication_job_approver", ApprovalTable: "fabrication_job_approval",
		Record: RecordSpec{
			Table: "fabrication_job", HistoryTable: "fabrication_job_history",
			IDColumn: "fabrication_job_id", UUIDColumn: "fabrication_job_uuid", StatusColumn: "fabrication_job_status",
			ApprovalStatusColumn: "job_approval_status", ApprovedByColumn: "job_approved_by",
			UpdatedAtColumn: "fabrication_job_updated_at", UpdatedByColumn: "fabrication_job_updated_by",
			RecordVersionColumn: "fabrication_job_record_version", DeletedAtColumn: "fabrication_job_deleted_at",
		},
		Gates: []Gate{
			{StatusCode: "TMPL", TargetStatusCode: "TAPV"},
			{StatusCode: "QCPD", TargetStatusCode: "QCPS"},
		},
	},
	"invoice": {
		RecordTypeCode: "INVC", ApproverTable: "invoice_approver", ApprovalTable: "invoice_approval",
		Record: RecordSpec{
			Table: "invoice", HistoryTable: "invoice_history",
			IDColumn: "invoice_id", UUIDColumn: "invoice_uuid", StatusColumn: "invoice_status",
			ApprovalStatusColumn: "invoice_approval_status", ApprovedByColumn: "invoice_approved_by",
			UpdatedAtColumn: "invoice_updated_at", UpdatedByColumn: "invoice_updated_by",
			RecordVersionColumn: "invoice_record_version", DeletedAtColumn: "invoice_deleted_at",
		},
		Gates: []Gate{{StatusCode: "PAPV", TargetStatusCode: "APPV"}},
	},
	"payment": {
		RecordTypeCode: "PYMT", ApproverTable: "payment_approver", ApprovalTable: "payment_approval",
		Record: RecordSpec{
			Table: "payment", HistoryTable: "payment_history",
			IDColumn: "payment_id", UUIDColumn: "payment_uuid", StatusColumn: "payment_status",
			ApprovalStatusColumn: "payment_approval_status", ApprovedByColumn: "payment_approved_by",
			UpdatedAtColumn: "payment_updated_at", UpdatedByColumn: "payment_updated_by",
			RecordVersionColumn: "payment_record_version", DeletedAtColumn: "payment_deleted_at",
		},
		Gates: []Gate{{StatusCode: "PEND", TargetStatusCode: "APPV"}},
	},
	"credit_memo": {
		RecordTypeCode: "CRDT", ApproverTable: "credit_memo_approver", ApprovalTable: "credit_memo_approval",
		Record: RecordSpec{
			Table: "credit_memo", HistoryTable: "credit_memo_history",
			IDColumn: "credit_memo_id", UUIDColumn: "credit_memo_uuid", StatusColumn: "credit_memo_status",
			ApprovalStatusColumn: "credit_memo_approval_status", ApprovedByColumn: "credit_memo_approved_by",
			UpdatedAtColumn: "credit_memo_updated_at", UpdatedByColumn: "credit_memo_updated_by",
			RecordVersionColumn: "credit_memo_record_version", DeletedAtColumn: "credit_memo_deleted_at",
		},
		// Credit Memo has no separate Pending status -- the gate sits on
		// Draft itself. Void always escapes (AlwaysAllowedExitCodes), so a
		// draft credit memo can still be voided without approval.
		Gates: []Gate{{StatusCode: "DRFT", TargetStatusCode: "APPV"}},
	},
	"refund": {
		RecordTypeCode: "RFND", ApproverTable: "refund_approver", ApprovalTable: "refund_approval",
		Record: RecordSpec{
			Table: "refund", HistoryTable: "refund_history",
			IDColumn: "refund_id", UUIDColumn: "refund_uuid", StatusColumn: "refund_status",
			ApprovalStatusColumn: "refund_approval_status", ApprovedByColumn: "refund_approved_by",
			UpdatedAtColumn: "refund_updated_at", UpdatedByColumn: "refund_updated_by",
			RecordVersionColumn: "refund_record_version", DeletedAtColumn: "refund_deleted_at",
		},
		Gates: []Gate{{StatusCode: "PEND", TargetStatusCode: "APPV"}},
	},
	"vendor_credit": {
		RecordTypeCode: "VCRD", ApproverTable: "vendor_credit_approver", ApprovalTable: "vendor_credit_approval",
		Record: RecordSpec{
			Table: "vendor_credit", HistoryTable: "vendor_credit_history",
			IDColumn: "vendor_credit_id", UUIDColumn: "vendor_credit_uuid", StatusColumn: "vendor_credit_status",
			ApprovalStatusColumn: "vendor_credit_approval_status", ApprovedByColumn: "vendor_credit_approved_by",
			UpdatedAtColumn: "vendor_credit_updated_at", UpdatedByColumn: "vendor_credit_updated_by",
			RecordVersionColumn: "vendor_credit_record_version", DeletedAtColumn: "vendor_credit_deleted_at",
		},
		// Vendor Credit has no separate Pending status -- the gate sits on
		// Draft itself, mirroring credit_memo. Void always escapes
		// (AlwaysAllowedExitCodes), so a draft vendor credit can still be
		// voided without approval.
		Gates: []Gate{{StatusCode: "DRFT", TargetStatusCode: "APPV"}},
	},
}

// ForWorkflowKey returns the approval-chain configuration for a workflow key
// (workflows.key), and false if that workflow has no configurable chain.
func ForWorkflowKey(key string) (ModuleConfig, bool) {
	cfg, ok := registry[key]
	return cfg, ok
}
