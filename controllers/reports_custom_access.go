package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"stonesuite-backend/authz"
	"stonesuite-backend/reports"
)

// reportModuleResources are the per-module grants a custom-report source can
// require; custom_report:read only unlocks the builder, never the data.
var reportModuleResources = []authz.Resource{
	authz.ResourceReportSales,
	authz.ResourceReportCRM,
	authz.ResourceReportPurchases,
	authz.ResourceReportInventory,
}

// customAccess is what one caller may run: which module grants they hold, at
// what scope, and whether the tenant has the relational CRM design.
type customAccess struct {
	decisions map[authz.Resource]authz.Decision
	designV2  bool
}

// buildCustomAccess resolves grants against each module's report permission.
// Pure, so the rules are unit-testable without a database.
func buildCustomAccess(grants []authz.Grant, designV2 bool) customAccess {
	a := customAccess{decisions: map[authz.Resource]authz.Decision{}, designV2: designV2}
	for _, r := range reportModuleResources {
		a.decisions[r] = authz.DecideAny(grants, []authz.Resource{r}, authz.ActionRead)
	}
	return a
}

func (a customAccess) allowed(r authz.Resource) bool { return a.decisions[r].Allowed }

func (a customAccess) scope(r authz.Resource) authz.Scope { return a.decisions[r].Scope }

// canUse reports whether the caller may run src: their module grant, and a V2
// tenant for the CRM sources.
func (a customAccess) canUse(src reports.Source) bool {
	if src.RequiresV2 && !a.designV2 {
		return false
	}
	return a.allowed(src.Resource)
}

// sources lists the sources the caller can run, in menu order.
func (a customAccess) sources() []reports.Source {
	return reports.VisibleSources(a.allowed, a.designV2)
}

// scopeArgsFor turns a grant scope into the row restriction for a query. An
// identity with no employee row gets owner id 0, which matches no row.
func scopeArgsFor(scope authz.Scope, employeeID int) reports.ScopeArgs {
	if scope == authz.ScopeAll {
		return reports.ScopeArgs{}
	}
	return reports.ScopeArgs{Restricted: true, OwnerEmployeeID: employeeID}
}

// parseRunLimit reads the optional row limit of a run request: absent means
// the preview limit, and anything above the export cap is capped.
func parseRunLimit(raw *int) (int, error) {
	if raw == nil {
		return reports.PreviewRowLimit, nil
	}
	if *raw < 1 {
		return 0, &reports.ValidationError{Field: "limit", Message: "limit must be a positive number."}
	}
	return min(*raw, reports.ExportRowLimit), nil
}

// validateReportName trims a report name and enforces its length in
// characters, not bytes.
func validateReportName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", &reports.ValidationError{Field: "name", Message: "Give the report a name."}
	}
	if len([]rune(name)) > reports.MaxReportName {
		return "", &reports.ValidationError{Field: "name", Message: fmt.Sprintf("Report names can be at most %d characters.", reports.MaxReportName)}
	}
	return name, nil
}

const copySuffix = " (copy)"

// copyName names the private copy of a report, keeping it within the limit.
func copyName(name string) string {
	runes := []rune(name)
	if room := reports.MaxReportName - len([]rune(copySuffix)); len(runes) > room {
		runes = runes[:room]
	}
	return string(runes) + copySuffix
}

// customReportErrorStatus maps a store/validation error to the HTTP status and
// message the client sees; anything unexpected stays opaque.
func customReportErrorStatus(err error) (int, string) {
	var verr *reports.ValidationError
	switch {
	case errors.As(err, &verr):
		return http.StatusBadRequest, verr.Message
	case errors.Is(err, reports.ErrReportNotFound):
		return http.StatusNotFound, "Report not found."
	}
	return http.StatusInternalServerError, "Could not complete that request."
}
