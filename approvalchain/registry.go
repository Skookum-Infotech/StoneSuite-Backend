// Package approvalchain is the single source of truth for which workflows
// have a configurable module approval chain, and which lkp_record_status
// code(s) gate them. Every module registered here stores its config in a
// table shaped exactly like estimate_approver: record_type_id,
// record_status_id, approver_employee_id, is_active, created_at, created_by
// (see database/migrations/tenant/schema.sql). That uniform shape is what
// lets store.go serve every module with one generic implementation instead
// of one handler per module.
//
// CRM (lead/prospect/customer) is intentionally not registered here — it has
// no single "Approved" status to gate, and keeps its existing per-status
// config (crm_workflow_approver, controllers/crm_admin.go) untouched.
package approvalchain

// Gate identifies one point in a module's status flow where sign-off from
// the module's configured approvers is required before a record may leave
// that status. Most modules have exactly one; Fabrication Job has two
// (Templating and QC Pending), so this is a slice rather than a single value.
type Gate struct {
	StatusCode string // lkp_record_status.record_status_code, scoped to RecordTypeCode
}

// ModuleConfig maps one workflows.key to the relational module's approval
// gate(s).
type ModuleConfig struct {
	RecordTypeCode string // lkp_record_type.record_type_code
	ApproverTable  string // e.g. "estimate_approver" -- uniform 4-column shape, see package doc
	Gates          []Gate
}

// HasGate reports whether statusCode is one of this module's configured
// gates.
func (m ModuleConfig) HasGate(statusCode string) bool {
	for _, g := range m.Gates {
		if g.StatusCode == statusCode {
			return true
		}
	}
	return false
}

// registry is keyed by workflows.key (see the seed INSERT INTO workflows
// statements in database/migrations/tenant/schema.sql for the canonical key
// list). Adding a new module here requires that module to already have its
// own <name>_approver table of the uniform shape described in the package
// doc -- this registry does not create tables.
var registry = map[string]ModuleConfig{
	"estimate":       {RecordTypeCode: "ESTM", ApproverTable: "estimate_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"quote":          {RecordTypeCode: "QUOT", ApproverTable: "quote_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"sales_order":    {RecordTypeCode: "SORD", ApproverTable: "sales_order_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"purchase_order": {RecordTypeCode: "PORD", ApproverTable: "purchase_order_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"requisition":    {RecordTypeCode: "REQN", ApproverTable: "requisition_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"vendor_bill":    {RecordTypeCode: "VBIL", ApproverTable: "vendor_bill_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"vendor_payment": {RecordTypeCode: "VPAY", ApproverTable: "vendor_payment_approver", Gates: []Gate{{StatusCode: "PAPV"}}},
	"expense":        {RecordTypeCode: "EXPN", ApproverTable: "expense_approver", Gates: []Gate{{StatusCode: "SUBM"}}},
	"installation":   {RecordTypeCode: "FJOB", ApproverTable: "fabrication_job_approver", Gates: []Gate{{StatusCode: "TMPL"}, {StatusCode: "QCPD"}}},
}

// ForWorkflowKey returns the approval-chain configuration for a workflow key
// (workflows.key), and false if that workflow has no configurable chain.
func ForWorkflowKey(key string) (ModuleConfig, bool) {
	cfg, ok := registry[key]
	return cfg, ok
}
