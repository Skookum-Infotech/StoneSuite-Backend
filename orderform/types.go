// Package orderform is the customer portal's countertop order form: what a
// customer files from "Created Orders", how it is stored, and how it becomes a
// Draft Sales Order for staff to finish.
//
// The form is stored exactly as entered (customer_order_form and its areas) so
// the customer's list can show it back. Staff work from the Sales Order built
// from it in the same transaction: one Kit/Package per area, one catalogue item
// per filled field.
package orderform

import (
	"errors"
	"time"
)

// StatusSubmitted is the only status a form has today.
const StatusSubmitted = "submitted"

// Area is one block of the form — Kitchen, Island, Bath 2 — in the shape the
// portal sends and receives. BacksplashHeight is a string because the form's
// input is one: "" means no backsplash.
type Area struct {
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	Color            string   `json:"color"`
	Finish           string   `json:"finish"`
	Edge             string   `json:"edge"`
	BacksplashHeight string   `json:"backsplashHeight"`
	SinkModel        string   `json:"sinkModel"`
	CooktopModel     string   `json:"cooktopModel"`
	WaterfallQty     int      `json:"waterfallQty"`
	PopUpOutlet      int      `json:"popUpOutlet"`
	HalfWall         bool     `json:"halfWall"`
	Location         string   `json:"location"`
	FaucetSpread     []string `json:"faucetSpread"`
	Notes            string   `json:"notes"`
}

// Input is the request body for placing an order. Dates are "yyyy-mm-dd".
type Input struct {
	Builder             string `json:"builder"`
	Community           string `json:"community"`
	Plan                string `json:"plan"`
	Phone               string `json:"phone"`
	Email               string `json:"email"`
	InstallAddress      string `json:"installAddress"`
	City                string `json:"city"`
	Zip                 string `json:"zip"`
	MeasureDate         string `json:"measureDate"`
	InstallDate         string `json:"installDate"`
	SpecialInstructions string `json:"specialInstructions"`
	Areas               []Area `json:"areas"`
}

// Form is a stored order as the portal reads it back.
type Form struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the customer last changed the order; absent if never.
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	// Editable is true while the order's Sales Order is still a draft: staff have
	// not moved it on, so the customer can still change it.
	Editable bool `json:"editable"`
	Input
}

// Page is one page of a customer's orders, newest first.
type Page struct {
	Orders     []Form
	NextCursor string
	HasMore    bool
}

// Result is what placing an order produced.
type Result struct {
	Form Form
	// SalesOrderUUID is the Draft Sales Order created from the form.
	SalesOrderUUID string
	// Synced is true when the Sales Order was saved along with the form. It is
	// false only for a staff change made after the order moved on from Draft,
	// which changes the stored form alone.
	Synced bool
	// CreatedKits / CreatedItems are the uuids of the catalogue rows this order
	// had to create; reused ones are not listed.
	CreatedKits  []string
	CreatedItems []string
}

// Submitter is the portal login placing the order, resolved server-side from
// the session — never from the request body.
type Submitter struct {
	PortalUserID string
	CustomerID   int
	CustomerUUID string
	FullName     string
	Email        string
}

// ValidationError is a problem with what the customer submitted. Message is
// written for the customer and safe to show.
type ValidationError struct{ Message string }

// Error implements error.
func (e *ValidationError) Error() string { return e.Message }

// ErrNotEditable means the order's Sales Order is no longer a draft (or is gone),
// so the customer can no longer change the order from the portal.
var ErrNotEditable = errors.New("order can no longer be changed")

// ErrCustomerNotActive means the customer's CRM status does not allow new
// orders (only Active customers can be used on new records).
var ErrCustomerNotActive = errors.New("customer is not active")
