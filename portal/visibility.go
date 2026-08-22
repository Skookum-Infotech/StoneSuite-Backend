// Package portal provides the customer-facing read surface: the link between a
// login identity and a customer record, the set of documents such a login may
// see, and the message threads it may participate in.
//
// The package is deliberately dependency-free of the document modules. It
// declares WHAT a portal customer may see; the modules implement the queries.
// This lets salesorder, invoice, payment and refund each import portal for the
// visibility table without any import cycle.
package portal

// Module identifies a portal-visible document type. The values match
// portal_message.portal_message_module and are part of the URL surface
// (/api/portal/invoices, etc.), so they are stable identifiers, not labels.
type Module string

const (
	ModuleSalesOrder Module = "sales_order"
	ModuleInvoice    Module = "invoice"
	ModulePayment    Module = "payment"
	ModuleRefund     Module = "refund"
)

// Visibility declares which lifecycle states of a document a customer may see.
//
// RecordTypeCode is the lkp_record_type.record_type_code the module's status
// rows hang off. StatusCodes are lkp_record_status.record_status_code values.
// Both are resolved to ids in SQL at query time rather than hard-coded, because
// those ids are SERIALs whose values depend on seed insertion order.
type Visibility struct {
	RecordTypeCode string
	StatusCodes    []string
}

// visibility is the single table governing what a customer sees.
//
// The rule is: finalized documents only. Anything still being drafted or
// awaiting internal sign-off is working state — a customer must not see a
// figure that staff are still editing, nor learn that an approval is pending.
//
// Deliberately an allowlist, not a denylist. A status code added to
// lkp_record_status later is invisible to the portal until someone adds it
// here, which is the correct failure direction.
var visibility = map[Module]Visibility{
	// Hidden: DRFT (drafting), PAPV (awaiting internal approval).
	// Shown from APPV onward — an approved order is a commitment to the customer.
	ModuleSalesOrder: {
		RecordTypeCode: "SORD",
		StatusCodes:    []string{"APPV", "OPEN", "PART", "FILL", "CANC"},
	},

	// Hidden: DRFT, PAPV, and also APPV — an invoice that is approved but not
	// yet SENT has not been issued to the customer, so it is not yet theirs to
	// see. VOID is shown: a customer who received an invoice should be able to
	// see that it was voided rather than have it silently vanish.
	ModuleInvoice: {
		RecordTypeCode: "INVC",
		StatusCodes:    []string{"SENT", "PART", "PAID", "ODUE", "VOID"},
	},

	// Hidden: PEND — an unconfirmed payment is not yet a fact about the account.
	ModulePayment: {
		RecordTypeCode: "PYMT",
		StatusCodes:    []string{"APPV", "DEPO", "VOID"},
	},

	// Hidden: PEND — a refund not yet approved must not be shown as promised.
	ModuleRefund: {
		RecordTypeCode: "RFND",
		StatusCodes:    []string{"APPV", "SENT", "VOID"},
	},
}

// Visible returns the visibility rule for a module.
//
// The second return is false for an unknown module. Callers must treat that as
// "show nothing" rather than "show everything" — an unrecognized module is a
// programming error, and failing open would expose every document.
func Visible(m Module) (Visibility, bool) {
	v, ok := visibility[m]
	return v, ok
}

// Modules returns every portal-visible module. Order is not guaranteed; callers
// that need determinism (tests, docs) should sort.
func Modules() []Module {
	out := make([]Module, 0, len(visibility))
	for m := range visibility {
		out = append(out, m)
	}
	return out
}

// urlSlugs maps the URL path segment a module is served under to the module.
//
// The two differ deliberately: the Module value is the stored discriminator in
// portal_message.portal_message_module and must stay stable, while the URL
// segment follows the REST convention already used by the document endpoints
// (plural, hyphenated — /api/portal/sales-orders). Without this mapping the
// message endpoints would sit at a different spelling from the documents they
// hang off, which is a trap for every caller.
var urlSlugs = map[string]Module{
	"sales-orders": ModuleSalesOrder,
	"invoices":     ModuleInvoice,
	"payments":     ModulePayment,
	"refunds":      ModuleRefund,
}

// ModuleForURL resolves the {module} path segment used by the message routes.
//
// Fails closed on anything unrecognised, so the path segment can never widen
// past the four portal-visible modules.
func ModuleForURL(slug string) (Module, bool) {
	m, ok := urlSlugs[slug]
	return m, ok
}

// URLSlug returns the path segment a module is served under. Second return is
// false for an unknown module.
func URLSlug(m Module) (string, bool) {
	for slug, mod := range urlSlugs {
		if mod == m {
			return slug, true
		}
	}
	return "", false
}

// ValidModule reports whether s names a portal-visible module by its stored
// value (as opposed to its URL segment — see ModuleForURL). Used when a module
// arrives from data rather than a path, e.g. a stored portal_message row.
func ValidModule(s string) (Module, bool) {
	m := Module(s)
	_, ok := visibility[m]
	return m, ok
}
