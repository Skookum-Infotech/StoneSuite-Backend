// Package drawing holds the server-side model and validation for the
// countertop drawing editor's saved document (StoneSuite-WebUI's
// src/types/drawing.ts). These types mirror that file field-for-field so a
// saved/restored Drawing round-trips through JSON without loss, even though
// this package only inspects Outline/Pieces/Areas for validation.
package drawing

// CurrentSchemaVersion mirrors the frontend's DRAWING_SCHEMA_VERSION
// (src/types/drawing.ts) -- the server stamps every saved Drawing with it.
const CurrentSchemaVersion = 1

// Point is a 2D coordinate in millimetres.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PieceEdge is a tenant-defined edge marking on one side of a piece.
type PieceEdge struct {
	Index          int      `json:"index"`
	Code           string   `json:"code,omitempty"`
	OverhangMm     *float64 `json:"overhangMm,omitempty"`
	SplashHeightMm *float64 `json:"splashHeightMm,omitempty"`
	// WaterfallHeightMm is a panel dropping from this side.
	WaterfallHeightMm *float64 `json:"waterfallHeightMm,omitempty"`
	// ArcHeightMm bows the side into a circular arc (signed; positive bulges to
	// the left of the side's direction). Written by the Border and Corner tools.
	ArcHeightMm *float64 `json:"arcHeightMm,omitempty"`
	// BorderID/BorderStyle tag every side one Border reshape made; CornerID/
	// CornerStyle do the same for one Corner change. The editor uses them to find
	// the group again when it is made standard.
	BorderID    string `json:"borderId,omitempty"`
	BorderStyle string `json:"borderStyle,omitempty"`
	CornerID    string `json:"cornerId,omitempty"`
	CornerStyle string `json:"cornerStyle,omitempty"`
	// EdgeTypeID is the drawing_edge_type (Edges tool) painted onto this side.
	EdgeTypeID string `json:"edgeTypeId,omitempty"`
}

// Seam is a straight join line between two points on a piece.
type Seam struct {
	From Point `json:"from"`
	To   Point `json:"to"`
}

// Cutout is a sink/cooktop/outlet/faucet/custom opening in a piece.
//
// Center and Outline are in the piece's own local coordinates. Every field
// the frontend can send must be listed here: encoding/json silently drops
// anything it has no field for, which would lose it on the next save.
type Cutout struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Shape           string   `json:"shape,omitempty"`
	Center          Point    `json:"center"`
	WidthMm         *float64 `json:"widthMm,omitempty"`
	DepthMm         *float64 `json:"depthMm,omitempty"`
	DiameterMm      *float64 `json:"diameterMm,omitempty"`
	RotationDeg     *float64 `json:"rotationDeg,omitempty"`
	Outline         []Point  `json:"outline,omitempty"`
	InventoryItemID string   `json:"inventoryItemId,omitempty"`
	EdgeCode        string   `json:"edgeCode,omitempty"`
	Label           string   `json:"label,omitempty"`
	// HideName / HideMeasurements leave the name / the size text off the drawing;
	// the opening itself is always drawn.
	HideName         bool `json:"hideName,omitempty"`
	HideMeasurements bool `json:"hideMeasurements,omitempty"`
	// ShapeCode names the picture the opening is drawn as (the frontend's
	// cutoutShapes.ts, e.g. "sink.farmhouse"); empty is the plain rectangle.
	ShapeCode string `json:"shapeCode,omitempty"`
	// FaucetHoleCount / FaucetHoleRadiusMm describe the faucet holes drilled with
	// a sink (radius in mm).
	FaucetHoleCount    *int           `json:"faucetHoleCount,omitempty"`
	FaucetHoleRadiusMm *float64       `json:"faucetHoleRadiusMm,omitempty"`
	DistanceLines      *DistanceLines `json:"distanceLines,omitempty"`
}

// DistanceLines says, per side of a cutout, how its distance to the piece's
// edge is measured: "center", "border" or "hidden".
type DistanceLines struct {
	Top    string `json:"top"`
	Right  string `json:"right"`
	Bottom string `json:"bottom"`
	Left   string `json:"left"`
}

// Piece is one countertop piece within an Area.
type Piece struct {
	ID               string      `json:"id"`
	Label            string      `json:"label"`
	Shape            string      `json:"shape"`
	Kind             string      `json:"kind,omitempty"`
	Outline          []Point     `json:"outline"`
	Position         Point       `json:"position"`
	RotationDeg      float64     `json:"rotationDeg"`
	ThicknessMm      float64     `json:"thicknessMm"`
	CornerRadiusMm   *float64    `json:"cornerRadiusMm,omitempty"`
	SalesOrderLineID string      `json:"salesOrderLineId,omitempty"`
	Edges            []PieceEdge `json:"edges"`
	Seams            []Seam      `json:"seams"`
	Cutouts          []Cutout    `json:"cutouts"`
	// Slab is where the Material tab laid this piece on a slab; nil when it has
	// not been placed. A piece sits on one slab at a time.
	Slab *SlabPlacement `json:"slab,omitempty"`
}

// SlabPlacement is a piece's spot on one inventory slab. The piece's outline is
// rotated by RotationDeg about its own origin, then translated so the top-left
// of its bounding box sits at (X, Y) millimetres from the slab's top-left corner.
// SlabID is the inventory unit's uuid; the slab itself is held for the order by
// the inventory module, not stored in the drawing.
type SlabPlacement struct {
	SlabID      string  `json:"slabId"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	RotationDeg float64 `json:"rotationDeg"`
}

// Note is a free-text annotation placed on the canvas.
type Note struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	At      Point  `json:"at"`
	PieceID string `json:"pieceId,omitempty"`
}

// Dimension is a manual measurement between two points, in the area's world
// millimetres. OffsetMm pushes the drawn line away from the measured points.
type Dimension struct {
	ID       string  `json:"id"`
	From     Point   `json:"from"`
	To       Point   `json:"to"`
	OffsetMm float64 `json:"offsetMm,omitempty"`
}

// Area is one tab's worth of pieces, cabinets, and notes.
type Area struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	SortOrder int       `json:"sortOrder"`
	Pieces    []Piece   `json:"pieces"`
	Cabinets  [][]Point `json:"cabinets"`
	Notes     []Note    `json:"notes"`
	// Dimensions are manual measurements; absent on older drawings.
	Dimensions []Dimension `json:"dimensions,omitempty"`
}

// UpdatedBy identifies who last saved a Drawing or Drawing version.
type UpdatedBy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Drawing is the whole saved document for one sales order.
type Drawing struct {
	SchemaVersion       int       `json:"schemaVersion"`
	SalesOrderID        string    `json:"salesOrderId"`
	Version             int       `json:"version"`
	DisplayUnit         string    `json:"displayUnit"`
	FractionDenominator int       `json:"fractionDenominator"`
	Areas               []Area    `json:"areas"`
	UpdatedAt           string    `json:"updatedAt"`
	UpdatedBy           UpdatedBy `json:"updatedBy"`
}
