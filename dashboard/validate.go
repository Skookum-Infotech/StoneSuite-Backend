package dashboard

import "stonesuite-backend/authz"

// ValidatePrefs checks each input's WidgetKey resolves to a real catalog
// entry the caller currently holds the grant for, and that layout values are
// within the grid bounds (MinSize/MaxWidth/MaxHeight). Returns the resolved
// UserPref slice ready to persist, or the first problem found: ClientError
// for an unknown key or out-of-bounds value, ForbiddenWidgetError for a real
// key the caller lacks the grant for.
//
// It validates against the live production catalog (via ByKey) rather than
// taking one as a parameter -- unlike Resolve, there is no legitimate caller
// that would want to validate a request against anything but the real
// catalog.
func ValidatePrefs(inputs []PrefInput, grants []authz.Grant) ([]UserPref, error) {
	if len(inputs) == 0 {
		return nil, ClientError{Msg: "widgets must not be empty."}
	}
	out := make([]UserPref, 0, len(inputs))
	for _, in := range inputs {
		w, ok := ByKey(in.WidgetKey)
		if !ok {
			return nil, ClientError{Msg: "Unknown widget key: " + in.WidgetKey}
		}
		decision := authz.DecideAny(grants, []authz.Resource{w.Resource}, w.Action)
		if !decision.Allowed {
			return nil, ForbiddenWidgetError{WidgetKey: in.WidgetKey}
		}
		if in.Position < 0 {
			return nil, ClientError{Msg: "position must be >= 0 for widget " + in.WidgetKey}
		}
		if in.Width < MinSize || in.Width > MaxWidth {
			return nil, ClientError{Msg: "width must be between 1 and 12 for widget " + in.WidgetKey}
		}
		if in.Height < MinSize || in.Height > MaxHeight {
			return nil, ClientError{Msg: "height must be between 1 and 8 for widget " + in.WidgetKey}
		}
		out = append(out, UserPref{
			WidgetKey: in.WidgetKey, Visible: in.Visible,
			Position: in.Position, Width: in.Width, Height: in.Height,
		})
	}
	return out, nil
}

// ValidateConfigUpdates checks each input's WidgetKey resolves to a real
// catalog entry. No grant check: the caller already proved
// workflow_config:configure at the controller before reaching here, a
// workspace-wide capability independent of any one widget's own resource.
func ValidateConfigUpdates(inputs []ConfigInput) error {
	if len(inputs) == 0 {
		return ClientError{Msg: "widgets must not be empty."}
	}
	for _, in := range inputs {
		if _, ok := ByKey(in.WidgetKey); !ok {
			return ClientError{Msg: "Unknown widget key: " + in.WidgetKey}
		}
	}
	return nil
}
