// Package dashboardui manages which dashboard widgets each role's members
// may see. The widget catalog itself (title, description, layout size,
// category) is owned by the frontend for rendering -- this package only
// tracks the stable widget ids and validates that a role's allocation
// references real ones, mirroring src/config/dashboardWidgets.ts.
package dashboardui

// widgetIDs is the whitelist of dashboard widget ids the frontend catalog
// can render (src/config/dashboardWidgets.ts) -- kept in sync by hand, since
// it changes rarely. An id outside this set is rejected on write.
var widgetIDs = []string{
	"kpi-strip",
	"pipeline-donut",
	"material-consumption",
	"recent-records",
	"sales-orders-snapshot",
	"top-customers",
	"inventory-alerts",
	"purchases-status",
	"ar-outstanding",
	"accounting-snapshot",
}

// defaultWidgetIDs are allocated to a role with no saved configuration yet
// -- the "core" category in the frontend catalog.
var defaultWidgetIDs = []string{
	"kpi-strip",
	"pipeline-donut",
	"material-consumption",
	"recent-records",
}

var widgetIDSet = buildWidgetIDSet()

func buildWidgetIDSet() map[string]bool {
	m := make(map[string]bool, len(widgetIDs))
	for _, id := range widgetIDs {
		m[id] = true
	}
	return m
}

// IsValidWidgetID reports whether id exists in the widget catalog whitelist.
func IsValidWidgetID(id string) bool {
	return widgetIDSet[id]
}

// WidgetIDs returns a copy of every widget id in the catalog whitelist.
func WidgetIDs() []string {
	out := make([]string, len(widgetIDs))
	copy(out, widgetIDs)
	return out
}

// DefaultWidgetIDs returns a copy of the widgets allocated to a role with no
// saved configuration yet.
func DefaultWidgetIDs() []string {
	out := make([]string, len(defaultWidgetIDs))
	copy(out, defaultWidgetIDs)
	return out
}
