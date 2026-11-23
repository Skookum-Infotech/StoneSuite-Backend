// controllers/customer_portal_links.go
package controllers

import (
	"fmt"

	"stonesuite-backend/services"
)

// portalViewableResources are the document types a customer-portal session can
// actually open: the frontend's CUSTOMER_ALLOWED_PATH_PREFIXES
// (src/config/customerPortal.ts) holds exactly these four under /sales/, and
// the API serves them under /api/portal/. Any other path bounces the customer
// to the Sales Orders list, so an email must never link to one.
//
// Keep in step with that frontend list: a resource added here needs a portal
// page and API first.
var portalViewableResources = map[string]bool{
	custResSalesOrder: true,
	custResInvoice:    true,
	custResPayment:    true,
	custResRefund:     true,
}

// portalViewable reports whether the portal has a page for resource.
func portalViewable(resource string) bool {
	return portalViewableResources[resource]
}

// portalDocLink is the absolute portal link to one record, or "" when the
// portal has no page for that resource (a quote, estimate, credit memo, ...)
// or the frontend origin is not configured. An empty link makes the email
// template omit its button, which is the honest outcome: a dead "View in
// portal" button is worse than none.
func portalDocLink(resource, recordUUID string) string {
	if !portalViewable(resource) || recordUUID == "" {
		return ""
	}
	return services.AppURL(fmt.Sprintf(custLinkFormat, resource, recordUUID))
}

// approvedBodyFormat is the customer-facing "your document was approved" copy.
// Two %s verbs: lower-cased document kind, then number.
const (
	approvedBodyWithPortal = "Your %s %s has been approved. You can view the approved %s in your customer portal anytime."
	approvedBodyNoPortal   = "Your %s %s has been approved. Please contact us if you have any questions."
)

// approvedBody is the approval email's paragraph. It only mentions the portal
// for resources the portal can show.
func approvedBody(resource, kind, number string) string {
	if portalViewable(resource) {
		return fmt.Sprintf(approvedBodyWithPortal, kind, number, kind)
	}
	return fmt.Sprintf(approvedBodyNoPortal, kind, number)
}
