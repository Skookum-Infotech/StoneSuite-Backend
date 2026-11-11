// approvalchain/route.go
package approvalchain

import "fmt"

// moduleRoute is a resource's two frontend route segments: domain (route
// segment 1) and module (route segment 2), e.g. "purchases"/"vendor_bill" ->
// "/purchases/vendor_bill/<id>". This package cannot import globalsearch
// (globalsearch's sales providers import estimate, which imports this
// package back -- a cycle), so it keeps its own small copy of the same
// resource->route mapping globalsearch/registry.go already maintains for
// search, covering only the resources this package's registry actually
// uses with DisplayName set (see registry.go's ModuleConfig.Resource).
// Keep this in sync with the corresponding Domain/Module fields in
// globalsearch/registry.go when either changes.
type moduleRoute struct {
	Domain, Module string
}

var moduleRoutes = map[string]moduleRoute{
	"purchase_order": {Domain: "purchases", Module: "purchase_order"},
	"requisition":    {Domain: "purchases", Module: "requisition"},
	"vendor_bill":    {Domain: "purchases", Module: "vendor_bill"},
	"vendor_payment": {Domain: "purchases", Module: "vendor_payment"},
	"vendor_credit":  {Domain: "purchases", Module: "vendor_credit"},
	"expense":        {Domain: "purchases", Module: "expense"},
	"installation":   {Domain: "sales", Module: "installation"},
	"invoice":        {Domain: "sales", Module: "invoice"},
	"payment":        {Domain: "sales", Module: "payment"},
	"credit_memo":    {Domain: "sales", Module: "credit_memo"},
	"refund":         {Domain: "sales", Module: "refund"},
	"bin_transfer":   {Domain: "inventory", Module: "bin_transfer"},
}

// listOnlyRoutes maps the modules that predate the engine (and so are absent
// from moduleRoutes, whose record deep links stay unchanged) to their frontend
// list pages, so a delete notification still has somewhere useful to link.
var listOnlyRoutes = map[string]moduleRoute{
	"quote":       {Domain: "sales", Module: "quote"},
	"estimate":    {Domain: "sales", Module: "estimate"},
	"sales_order": {Domain: "sales", Module: "sales_order"},
}

// resourceRoute builds a notification's deep-link path for resource+recordID,
// or "" if resource has no known route -- an unmapped resource must never
// block or fail the notification it's building (see sendApprovalNotification).
func resourceRoute(resource, recordID string) string {
	r, ok := moduleRoutes[resource]
	if !ok {
		return ""
	}
	return fmt.Sprintf("/%s/%s/%s", r.Domain, r.Module, recordID)
}

// listRoute builds the module's list-page path for resource, or "" if resource
// has no known route. Used where the record itself no longer exists (delete).
func listRoute(resource string) string {
	r, ok := moduleRoutes[resource]
	if !ok {
		r, ok = listOnlyRoutes[resource]
	}
	if !ok {
		return ""
	}
	return fmt.Sprintf("/%s/%s", r.Domain, r.Module)
}

// RecordRoute is the exported deep-link builder for resource+recordID, or ""
// when the resource has no known route.
func RecordRoute(resource, recordID string) string {
	return resourceRoute(resource, recordID)
}
