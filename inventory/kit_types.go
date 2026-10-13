package inventory

import "time"

const (
	// MaxKitNameLen matches the inventory_kit.inventory_kit_name column.
	MaxKitNameLen = 100
	// MaxKitMembers bounds one kit, so adding it to a document stays a request of
	// sensible size. A kit is a short list of items sold together, not a catalogue.
	MaxKitMembers = 200
)

// KitRef is the light reference to a kit that an item carries.
type KitRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// KitMember is one item a kit holds and the quantity a document line gets by
// default. Item is the full catalogue record — the same shape an item search
// returns — so a client adds a kit to a line table through the code it already
// has for adding an item.
type KitMember struct {
	Quantity float64 `json:"quantity"`
	Item     Item    `json:"item"`
}

// Kit is a named group of catalogue items that are sold together, e.g. "Kitchen".
type Kit struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsActive    bool   `json:"isActive"`
	// ItemCount is the number of live items in the kit; set on every read.
	ItemCount int `json:"itemCount"`
	// Members is filled by GetKit only — a list or search row carries the count.
	Members   []KitMember `json:"members,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

// KitMemberInput names one item to put in a kit.
type KitMemberInput struct {
	// ItemID is the item's external uuid.
	ItemID string `json:"itemId"`
	// Quantity is what a line gets by default; omitted or 0 reads as 1.
	Quantity float64 `json:"quantity"`
}

// KitInput is the write shape for a kit. Update reuses it and overwrites every
// field, like items: the form always sends the whole object.
//
// Two fields use nil to mean "leave as it is", so a client that omits them can
// never wipe them by accident: IsActive (nil on create means active) and
// Members (an explicit empty list, by contrast, empties the kit).
type KitInput struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	IsActive    *bool            `json:"isActive,omitempty"`
	Members     []KitMemberInput `json:"members"`
}

// KitPage is one page of a keyset-paginated kit search.
type KitPage struct {
	Records    []Kit  `json:"records"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}
