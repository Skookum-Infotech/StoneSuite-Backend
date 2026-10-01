// Package mytransactions lists the records the signed-in user created or last
// updated, merged across every business module into one newest-first feed (the
// "My Transactions" page).
//
// It is a read-only browse surface like auditstore, distinct from the record
// filter engine in query/: one UNION ALL over the registry below, built only
// from the registry's static table/column names, with every client-supplied
// value (employee id, search term, cursor, limit) bound as a $n parameter.
//
// registry is the single source of truth for which modules participate. A
// module missing from it is silently absent from the page — see
// registry_test.go, which cross-checks it against globalsearch's registry so a
// newly added module cannot be forgotten.
package mytransactions

import (
	"stonesuite-backend/authz"
)

// Role values: why a record is on the caller's list.
const (
	// RoleAll is the filter value for "created or updated"; it is never a row's role.
	RoleAll = "all"
	// RoleCreated marks a record the caller created.
	RoleCreated = "created"
	// RoleUpdated marks a record the caller last updated but did not create.
	RoleUpdated = "updated"
)

// Source describes one module's table and how to read its list-row fields.
// Every string is static SQL authored in this package — never request data —
// and expressions reference the base table as alias t plus any aliases Joins
// introduces. An empty expression means the module has no such field (the row
// shows empty text, or NULL for the amount).
type Source struct {
	Key      string         // registry key == globalsearch provider key == the row's "type"
	Label    string         // singular display name, e.g. "Sales Order"
	Resource authz.Resource // RBAC resource gating read access
	Domain   string         // frontend route segment 1: crm|sales|purchases|inventory|finance
	Module   string         // frontend route segment 2 (not always == Key, e.g. installation)

	Table string // base table, aliased t
	Joins string // extra JOIN clauses appended after the base table
	Where string // extra static predicate (e.g. the CRM stage), "" for none

	ID         string // SQL expression: record uuid (the id the detail page routes on)
	Number     string // SQL expression: human reference (doc number, sku, code, serial)
	Name       string // SQL expression: title (customer/vendor/item/account name)
	Account    string // SQL expression: customer/vendor the record is with
	StatusName string // SQL expression: status label
	StatusCode string // SQL expression: stable status code (drives badge colour)
	Amount     string // SQL expression: single monetary total, "" when none

	// Columns on the base table.
	CreatedBy string // employee id of the creator
	UpdatedBy string // employee id of the last updater; "" when the table has none
	CreatedAt string
	UpdatedAt string
	DeletedAt string // soft-delete marker; deleted rows never list
	Owner     string // owner column the module's own "own" scope narrows on; "" when it has none

	// AuditUpdated reads "updated by me" from the audit trail instead of
	// UpdatedBy, for the CRM table which records created_by but no updated_by.
	AuditUpdated bool
}

// Sources returns every module on the My Transactions page, in a stable order.
func Sources() []Source {
	out := make([]Source, 0, 26)
	out = append(out, crmSources()...)
	out = append(out, salesSources()...)
	out = append(out, purchasingSources()...)
	out = append(out, inventorySources()...)
	out = append(out, financeSources()...)
	return out
}

// sourceByKey finds a registered source by its key.
func sourceByKey(key string) (Source, bool) {
	for _, s := range Sources() {
		if s.Key == key {
			return s, true
		}
	}
	return Source{}, false
}

// recordStatusJoin joins a document table to the shared record-status lookup.
func recordStatusJoin(statusCol string) string {
	return "LEFT JOIN lkp_record_status s ON s.record_status_id = t." + statusCol
}

// customerJoin joins a sales document to its customer for the account name.
func customerJoin(customerCol string) string {
	return "LEFT JOIN customer cu ON cu.customer_id = t." + customerCol
}

// doc builds the standard relational-document source: the clone-twin column
// skeleton (<p>_uuid, <p>_number, <p>_status → lkp_record_status, <p>_created_by,
// <p>_updated_by, <p>_owner_id, soft delete). Callers override what differs.
func doc(key, label string, res authz.Resource, domain, module, table, p string) Source {
	return Source{
		Key: key, Label: label, Resource: res, Domain: domain, Module: module,
		Table:      table,
		Joins:      recordStatusJoin(p + "_status"),
		ID:         "t." + p + "_uuid",
		Number:     "t." + p + "_number",
		StatusName: "s.record_status_name",
		StatusCode: "s.record_status_code",
		CreatedBy:  p + "_created_by",
		UpdatedBy:  p + "_updated_by",
		CreatedAt:  p + "_created_at",
		UpdatedAt:  p + "_updated_at",
		DeletedAt:  p + "_deleted_at",
		Owner:      p + "_owner_id",
	}
}

// salesDoc is doc for a customer-facing sales document: the account is the
// joined customer name and the amount is the stored total column ("" when the
// document carries no single total).
func salesDoc(key, label string, res authz.Resource, module, table, p, amountCol string) Source {
	s := doc(key, label, res, "sales", module, table, p)
	s.Joins += " " + customerJoin(p+"_customer_id")
	s.Account = "cu.customer_name"
	if amountCol != "" {
		s.Amount = "t." + amountCol
	}
	return s
}

// purchaseDoc is doc for a vendor-facing purchasing document: the account is
// the vendor-name snapshot the document stores on itself.
func purchaseDoc(key, label string, res authz.Resource, table, p, amountCol string) Source {
	s := doc(key, label, res, "purchases", key, table, p)
	s.Account = "t." + p + "_vendor_name"
	if amountCol != "" {
		s.Amount = "t." + amountCol
	}
	return s
}
