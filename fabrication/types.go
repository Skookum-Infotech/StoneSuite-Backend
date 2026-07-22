package fabrication

import "time"

// CustomerRef is the light customer reference on a Job response.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SiteAddress is the frozen job-site snapshot.
type SiteAddress struct {
	CustomerName string `json:"customerName"`
	AddrLine1    string `json:"addrLine1"`
	AddrLine2    string `json:"addrLine2"`
	City         string `json:"city"`
	StateID      *int   `json:"stateId"`
	Zip          string `json:"zip"`
	Phone        string `json:"phone"`
}

// Job is the full API response for a fabrication job header. OwnerUserID backs
// the controller's IDOR scope check and is never serialized.
type Job struct {
	ID             string      `json:"id"`
	Number         string      `json:"jobNumber"`
	Status         string      `json:"status"`     // human label
	StatusCode     string      `json:"statusCode"` // lkp_record_status code
	ApprovalStatus string      `json:"approvalStatus"`
	SalesOrderID   string      `json:"salesOrderId"`
	Customer       CustomerRef `json:"customer"`
	OwnerUserID    string      `json:"-"`

	HeldFromStatusCode string `json:"heldFromStatusCode,omitempty"`
	CancelRequested    bool   `json:"cancelRequested"`

	Site SiteAddress `json:"site"`

	TemplateDate        string `json:"templateDate,omitempty"`
	FabricationStart    string `json:"fabricationStart,omitempty"`
	PromisedInstallDate string `json:"promisedInstallDate,omitempty"`
	ActualInstallDate   string `json:"actualInstallDate,omitempty"`

	OwnerEmployeeID       *int `json:"ownerEmployeeId"`
	TemplaterEmployeeID   *int `json:"templaterEmployeeId"`
	FabricatorEmployeeID  *int `json:"fabricatorEmployeeId"`
	InstallCrewEmployeeID *int `json:"installCrewEmployeeId"`

	Notes        string         `json:"notes,omitempty"`
	CustomFields map[string]any `json:"customFields,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Pieces    []JobItem `json:"pieces,omitempty"`
	Steps     []Step    `json:"steps,omitempty"`
}

// JobItem is one fabricated piece.
type JobItem struct {
	ID                 string  `json:"id"`
	PieceNumber        int     `json:"pieceNumber"`
	PieceName          string  `json:"pieceName"`
	PieceType          string  `json:"pieceType"`
	LengthMM           float64 `json:"lengthMm"`
	WidthMM            float64 `json:"widthMm"`
	ThicknessMM        float64 `json:"thicknessMm"`
	SinkCutoutCount    int     `json:"sinkCutoutCount"`
	CooktopCutoutCount int     `json:"cooktopCutoutCount"`
	SeamCount          int     `json:"seamCount"`
	Status             string  `json:"status"`
	// SalesOrderItemUUID is the linked sales-order line, when this piece has
	// one — round-tripped so an edit that doesn't touch the link (frontend
	// sends back whatever it read here) doesn't silently clear it.
	SalesOrderItemUUID string `json:"salesOrderItemUuid,omitempty"`
}

// Step is one of the 16 checklist rows.
type Step struct {
	Code        string         `json:"code"`
	Sequence    int            `json:"sequence"`
	Status      string         `json:"status"`
	Notes       string         `json:"notes,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	StartedAt   *time.Time     `json:"startedAt,omitempty"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
}

// PieceInput is one piece on create/update.
type PieceInput struct {
	PieceNumber        int     `json:"pieceNumber"`
	PieceName          string  `json:"pieceName"`
	PieceType          string  `json:"pieceType"`
	LengthMM           float64 `json:"lengthMm"`
	WidthMM            float64 `json:"widthMm"`
	ThicknessMM        float64 `json:"thicknessMm"`
	SinkCutoutCount    int     `json:"sinkCutoutCount"`
	CooktopCutoutCount int     `json:"cooktopCutoutCount"`
	SeamCount          int     `json:"seamCount"`
	SalesOrderItemUUID string  `json:"salesOrderItemUuid"`
}

// jobFields is the header payload shared by create and update.
type jobFields struct {
	SiteCustomerName      string         `json:"siteCustomerName"`
	SiteAddrLine1         string         `json:"siteAddrLine1"`
	SiteAddrLine2         string         `json:"siteAddrLine2"`
	SiteCity              string         `json:"siteCity"`
	SiteStateID           *int           `json:"siteStateId"`
	SiteZip               string         `json:"siteZip"`
	SitePhone             string         `json:"sitePhone"`
	TemplateDate          string         `json:"templateDate"`
	FabricationStart      string         `json:"fabricationStart"`
	PromisedInstallDate   string         `json:"promisedInstallDate"`
	OwnerEmployeeID       *int           `json:"ownerEmployeeId"`
	TemplaterEmployeeID   *int           `json:"templaterEmployeeId"`
	FabricatorEmployeeID  *int           `json:"fabricatorEmployeeId"`
	InstallCrewEmployeeID *int           `json:"installCrewEmployeeId"`
	Notes                 string         `json:"notes"`
	CustomFields          map[string]any `json:"customFields"`
	Pieces                []PieceInput   `json:"pieces"`
}

// CreateJobInput is the create-request payload. A job always originates from a
// sales order (spec §2.2).
type CreateJobInput struct {
	SalesOrderUUID string `json:"salesOrderUuid"`
	jobFields
}

// UpdateJobInput mirrors CreateJobInput minus the sales order (fixed at create).
type UpdateJobInput struct {
	jobFields
}

// Page is one page of a keyset-paginated job search (list rows omit pieces/steps).
type Page struct {
	Records    []Job
	NextCursor string
	HasMore    bool
}

// Slab is the API response for a serialized physical slab.
type Slab struct {
	ID              string  `json:"id"`
	Serial          string  `json:"serial"`
	VendorID        *int    `json:"vendorId"`
	SupplierCode    string  `json:"supplierCode,omitempty"`
	InventoryItemID string  `json:"inventoryItemId"`
	WarehouseID     int     `json:"warehouseId"`
	BundleID        string  `json:"bundleId,omitempty"`
	LengthMM        float64 `json:"lengthMm"`
	WidthMM         float64 `json:"widthMm"`
	ThicknessMM     float64 `json:"thicknessMm"`
	Area            float64 `json:"area"`
	Form            string  `json:"form"`   // full | cut
	Status          string  `json:"status"` // available|reserved|consumed|scrapped
	ParentSlabID    *string `json:"parentSlabId,omitempty"`
	Grade           string  `json:"grade,omitempty"`
	Finish          string  `json:"finish,omitempty"`
}
