package dashboard

import "errors"

// UserPref is one caller's saved visibility/layout for one widget, whether
// loaded from dashboard_user_widget or freshly validated from a request.
type UserPref struct {
	WidgetKey string
	Visible   bool
	Position  int
	Width     int
	Height    int
}

// ResolvedWidget is one item in the GET /dashboard/widgets response: catalog
// metadata plus the caller's effective grant scope and their own
// visibility/layout (or the catalog default when they have none saved).
type ResolvedWidget struct {
	Key          string `json:"key"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	DataEndpoint string `json:"dataEndpoint"`
	Scope        string `json:"scope"`
	Visible      bool   `json:"visible"`
	Position     int    `json:"position"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// PrefInput is one widget's requested visibility/layout from a
// PUT /dashboard/widgets/preferences request body.
type PrefInput struct {
	WidgetKey string `json:"widgetKey"`
	Visible   bool   `json:"visible"`
	Position  int    `json:"position"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// ConfigInput is one widget's requested tenant-wide enabled flag from a
// PUT /dashboard/config request body.
type ConfigInput struct {
	WidgetKey string `json:"widgetKey"`
	Enabled   bool   `json:"enabled"`
}

// ConfigEntry is one item in the GET /dashboard/config response: catalog
// metadata plus the tenant's effective enabled flag for it.
type ConfigEntry struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
}

// ClientError signals a client-caused validation failure that a controller
// maps to HTTP 400, mirroring crmactivity.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// ForbiddenWidgetError signals a request named a real catalog widget the
// caller does not currently hold the grant for -- distinct from ClientError
// so the controller can log it as a security event.
type ForbiddenWidgetError struct{ WidgetKey string }

func (e ForbiddenWidgetError) Error() string {
	return "not authorized for widget: " + e.WidgetKey
}

// IsForbiddenWidgetError reports whether err is a ForbiddenWidgetError, and
// returns the offending key.
func IsForbiddenWidgetError(err error) (string, bool) {
	var fe ForbiddenWidgetError
	if errors.As(err, &fe) {
		return fe.WidgetKey, true
	}
	return "", false
}
