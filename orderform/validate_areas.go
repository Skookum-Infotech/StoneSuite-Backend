package orderform

import (
	"math"
	"strconv"
	"strings"

	"stonesuite-backend/duplicate"
)

func normalizeAreas(in []Area) ([]Area, error) {
	if len(in) == 0 {
		return nil, invalid("Add at least one area to your order.")
	}
	if len(in) > MaxAreas {
		return nil, invalid("An order can have at most %d areas.", MaxAreas)
	}
	out := make([]Area, 0, len(in))
	seenKind := map[string]bool{}
	seenName := map[string]bool{}
	for _, a := range in {
		kind, ok := areaKinds[a.Kind]
		if !ok {
			return nil, invalid("Unknown area type %q.", a.Kind)
		}
		if !kind.repeatable && seenKind[a.Kind] {
			return nil, invalid("%s can only be added once.", kind.label)
		}
		seenKind[a.Kind] = true

		n, err := normalizeArea(a, kind)
		if err != nil {
			return nil, err
		}
		// Area names become Kit/Package names, so two that read the same would
		// merge their lines under one heading.
		key := duplicate.Key(n.Name)
		if seenName[key] {
			return nil, invalid("Another area is already named %q.", n.Name)
		}
		seenName[key] = true
		out = append(out, n)
	}
	return out, nil
}

// areaText reads one text input of an area: kept only when the kind shows the
// field (as the portal's own payload mapper does), tidied, and length-checked.
func areaText(kind areaKind, areaName, field, value string) (string, error) {
	if !kind.has(field) {
		return "", nil
	}
	v := clean(value, false)
	if tooLong(v, maxNameLen) {
		return "", invalid("%s: %s can be at most %d characters.", areaName, field, maxNameLen)
	}
	return v, nil
}

func normalizeArea(a Area, kind areaKind) (Area, error) {
	n := Area{Kind: a.Kind, Name: clean(a.Name, false), Notes: clean(a.Notes, true), FaucetSpread: []string{}}
	if n.Name == "" {
		return Area{}, invalid("Give every area a name.")
	}
	if tooLong(n.Name, maxAreaNameLen) {
		return Area{}, invalid("An area name can be at most %d characters.", maxAreaNameLen)
	}
	if tooLong(n.Notes, maxNotesLen) {
		return Area{}, invalid("%s: notes can be at most %d characters.", n.Name, maxNotesLen)
	}

	targets := []struct {
		dst   *string
		field string
		value string
	}{
		{&n.Color, fieldColor, a.Color},
		{&n.Finish, fieldFinish, a.Finish},
		{&n.Edge, fieldEdge, a.Edge},
		{&n.SinkModel, fieldSinkModel, a.SinkModel},
		{&n.CooktopModel, fieldCooktopModel, a.CooktopModel},
		{&n.Location, fieldLocation, a.Location},
	}
	for _, t := range targets {
		v, err := areaText(kind, n.Name, t.field, t.value)
		if err != nil {
			return Area{}, err
		}
		*t.dst = v
	}
	if n.Color == "" {
		return Area{}, invalid("%s: Color is required.", n.Name)
	}

	height, err := normalizeHeight(n.Name, a.BacksplashHeight, kind)
	if err != nil {
		return Area{}, err
	}
	n.BacksplashHeight = height

	if kind.has(fieldWaterfallQty) {
		if a.WaterfallQty < 0 || a.WaterfallQty > maxQty {
			return Area{}, invalid("%s: Waterfall Qty must be between 0 and %d.", n.Name, maxQty)
		}
		n.WaterfallQty = a.WaterfallQty
	}
	if kind.has(fieldPopUpOutlet) {
		if a.PopUpOutlet < 0 || a.PopUpOutlet > maxQty {
			return Area{}, invalid("%s: Pop-up Outlet must be between 0 and %d.", n.Name, maxQty)
		}
		n.PopUpOutlet = a.PopUpOutlet
	}
	n.HalfWall = kind.has(fieldHalfWall) && a.HalfWall

	spread, ok := orderSpread(kind.scale, a.FaucetSpread)
	if !ok {
		return Area{}, invalid("%s: Faucet Spread has a position this area does not offer.", n.Name)
	}
	if spread != nil {
		n.FaucetSpread = spread
	}
	return n, nil
}

// normalizeHeight reads the Backsplash Height input: blank (or 0) is none, else
// inches between 0 and 96, kept to two decimals as the column holds.
func normalizeHeight(areaName, raw string, kind areaKind) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !kind.has(fieldBacksplashHeight) {
		return "", nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxBacksplashHeightIn {
		return "", invalid("%s: Backsplash Height must be between 0 and %d inches.", areaName, maxBacksplashHeightIn)
	}
	v = math.Round(v*100) / 100
	if v == 0 {
		return "", nil
	}
	return strconv.FormatFloat(v, 'f', -1, 64), nil
}
