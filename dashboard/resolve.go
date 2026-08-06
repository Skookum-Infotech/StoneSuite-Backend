package dashboard

import (
	"sort"

	"stonesuite-backend/authz"
)

// Resolve filters the catalog down to what the caller may see -- tenant
// override first, then the caller's grants -- and overlays the caller's
// saved preferences on top of catalog defaults. Pure: grants/overrides/prefs
// are all pre-loaded by the controller, so this has no I/O and is fully
// unit-testable. Result is sorted by effective Position ascending, Key as
// the tiebreaker.
//
// Only two things ever remove a widget from the result: the caller lacking
// the grant, or the tenant disabling it (which beats every role, including a
// wildcard grant). A widget the caller has personally hidden (visible=false)
// still appears, so a "manage widgets" panel can offer it back.
func Resolve(catalog []Widget, grants []authz.Grant, overrides map[string]bool, prefs map[string]UserPref) []ResolvedWidget {
	out := make([]ResolvedWidget, 0, len(catalog))
	for _, w := range catalog {
		if enabled, ok := overrides[w.Key]; ok && !enabled {
			continue
		}
		decision := authz.DecideAny(grants, []authz.Resource{w.Resource}, w.Action)
		if !decision.Allowed {
			continue
		}
		rw := ResolvedWidget{
			Key: w.Key, Title: w.Title, Description: w.Description,
			Category: string(w.Category), Type: string(w.Type), DataEndpoint: w.DataEndpoint,
			Scope:    string(decision.Scope),
			Visible:  w.DefaultVisible,
			Position: w.DefaultPosition,
			Width:    w.DefaultWidth,
			Height:   w.DefaultHeight,
		}
		if pref, ok := prefs[w.Key]; ok {
			rw.Visible = pref.Visible
			rw.Position = pref.Position
			rw.Width = pref.Width
			rw.Height = pref.Height
		}
		out = append(out, rw)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Key < out[j].Key
	})
	return out
}
