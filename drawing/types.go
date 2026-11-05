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
