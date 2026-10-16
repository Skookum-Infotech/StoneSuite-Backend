package orderform

import "strings"

// The area catalogue behind the form. It mirrors the portal's own table
// (src/lib/createdOrderAreas.ts in the web app): which inputs each kind of area
// shows, which printed Faucet Spread row it uses, and whether it may repeat. A
// new kind is one entry here and one there.

// Field keys, as the Area struct names them.
const (
	fieldColor            = "color"
	fieldFinish           = "finish"
	fieldEdge             = "edge"
	fieldBacksplashHeight = "backsplashHeight"
	fieldSinkModel        = "sinkModel"
	fieldCooktopModel     = "cooktopModel"
	fieldWaterfallQty     = "waterfallQty"
	fieldPopUpOutlet      = "popUpOutlet"
	fieldHalfWall         = "halfWall"
	fieldLocation         = "location"
)

// spreadScale is which printed Faucet Spread row an area uses.
type spreadScale string

const (
	scaleWide   spreadScale = "wide"   // 12" 8" 4" C 4" 8" 12"
	scaleNarrow spreadScale = "narrow" // 8" 4" 2" C 2" 4" 8"
)

// areaKind is one entry of the catalogue.
type areaKind struct {
	label      string
	fields     []string
	scale      spreadScale
	repeatable bool
}

var coreFields = []string{fieldColor, fieldFinish, fieldEdge, fieldBacksplashHeight, fieldSinkModel}

func withFields(extra ...string) []string {
	out := make([]string, 0, len(coreFields)+len(extra))
	out = append(out, coreFields...)
	return append(out, extra...)
}

var areaKinds = map[string]areaKind{
	"kitchen": {label: "Kitchen", fields: withFields(fieldCooktopModel, fieldWaterfallQty), scale: scaleWide},
	// The printed form's island block has no Backsplash Height.
	"island": {
		label:  "Island",
		fields: []string{fieldColor, fieldFinish, fieldEdge, fieldSinkModel, fieldCooktopModel, fieldWaterfallQty, fieldPopUpOutlet},
		scale:  scaleWide,
	},
	"pantry":      {label: "Pantry", fields: coreFields, scale: scaleWide},
	"hutch":       {label: "Hutch", fields: coreFields, scale: scaleWide},
	"utility":     {label: "Utility", fields: coreFields, scale: scaleWide},
	"master_bath": {label: "Primary / Master Bath", fields: withFields(fieldWaterfallQty, fieldHalfWall), scale: scaleNarrow},
	"powder_1":    {label: "Powder 1", fields: withFields(fieldLocation), scale: scaleNarrow},
	"powder_2":    {label: "Powder 2", fields: withFields(fieldLocation), scale: scaleNarrow},
	"bath":        {label: "Bath", fields: coreFields, scale: scaleNarrow, repeatable: true},
	"custom":      {label: "Custom area", fields: coreFields, scale: scaleNarrow, repeatable: true},
}

// has reports whether the kind shows the field.
func (k areaKind) has(field string) bool {
	for _, f := range k.fields {
		if f == field {
			return true
		}
	}
	return false
}

// spreadPosition is one tick box of a Faucet Spread row.
type spreadPosition struct {
	value string // as the portal sends it: "L12", "C", "R4"
	text  string // how it reads on a document line
}

// spreadRows are the tick boxes of each printed row, left to right.
var spreadRows = map[spreadScale][]spreadPosition{
	scaleWide: {
		{"L12", `12" left`}, {"L8", `8" left`}, {"L4", `4" left`}, {"C", "center"},
		{"R4", `4" right`}, {"R8", `8" right`}, {"R12", `12" right`},
	},
	scaleNarrow: {
		{"L8", `8" left`}, {"L4", `4" left`}, {"L2", `2" left`}, {"C", "center"},
		{"R2", `2" right`}, {"R4", `4" right`}, {"R8", `8" right`},
	},
}

// orderSpread returns the chosen positions in the row's left-to-right order,
// de-duplicated; ok is false when one is not on that row.
func orderSpread(scale spreadScale, chosen []string) (ordered []string, ok bool) {
	want := make(map[string]bool, len(chosen))
	for _, c := range chosen {
		want[c] = true
	}
	for _, pos := range spreadRows[scale] {
		if want[pos.value] {
			ordered = append(ordered, pos.value)
		}
	}
	return ordered, len(ordered) == len(want)
}

// spreadText renders ordered positions for a document line:
// `8" left, center, 8" right`.
func spreadText(scale spreadScale, ordered []string) string {
	text := make(map[string]string, len(spreadRows[scale]))
	for _, pos := range spreadRows[scale] {
		text[pos.value] = pos.text
	}
	parts := make([]string, 0, len(ordered))
	for _, v := range ordered {
		parts = append(parts, text[v])
	}
	return strings.Join(parts, ", ")
}
