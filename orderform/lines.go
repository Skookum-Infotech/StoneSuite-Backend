package orderform

import (
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/inventory"
	"stonesuite-backend/salesorder"
)

// What a form becomes on the Sales Order. Each area is a Kit/Package, and each
// field the customer filled in is a line under it: the item is the customer's
// entry ("Serene"), the line's description names the field it came from
// ("Color"). The items and kits are real catalogue rows (inventory.EnsureCatalog)
// that staff finish — specification, size, price — on the draft.

const (
	// skuPrefix starts the SKU of every item an order creates, so staff can find
	// the ones that still need completing.
	skuPrefix       = "PORTAL-"
	kitDescription  = "Created from a customer portal order."
	itemDescription = "Added from a customer portal order. Complete the specification and prices."

	memoDateLayout = "Jan 2, 2006"
)

// customerDefaults are the header values the staff Sales Order form pre-fills
// from the customer: terms, pricing, tax, owner and contact details.
type customerDefaults struct {
	name               string
	ownerEmployeeID    *int
	salesRepEmployeeID *int
	paymentTermsID     *int
	priceLevelID       *int
	currencyID         *int
	salesTaxPercent    float64
	billAttention      string
	billPhone          string
	billFax            string
	billEmail          string
}

// fieldFaucetSpread keys the Faucet Spread row, which areas.go has no input
// constant for (it is a list of tick boxes, not a field the kinds enumerate).
const fieldFaucetSpread = "faucetSpread"

// fieldLine is one filled field of an area.
type fieldLine struct {
	field       string // the input it came from: fieldColor, fieldEdge, …
	item        string // the catalogue item: the customer's entry
	description string // the line's description: which field it came from
}

// areaLines lists an area's filled fields in the form's own order. Blank text,
// a count of 0 and Half Wall "No" are not lines. Counts are not quantities: a
// line is always one of the item, and the count rides in its description.
func areaLines(a Area) []fieldLine {
	kind := areaKinds[a.Kind]
	var out []fieldLine
	add := func(field, item, description string) {
		if item != "" {
			out = append(out, fieldLine{field: field, item: item, description: description})
		}
	}
	add(fieldColor, a.Color, "Color")
	add(fieldFinish, a.Finish, "Finish")
	add(fieldEdge, a.Edge, "Edge")
	if a.BacksplashHeight != "" {
		add(fieldBacksplashHeight, a.BacksplashHeight+`" Backsplash`, "Backsplash Height")
	}
	add(fieldSinkModel, a.SinkModel, "Sink Model")
	add(fieldCooktopModel, a.CooktopModel, "Cooktop Model")
	if a.WaterfallQty > 0 {
		add(fieldWaterfallQty, "Waterfall", fmt.Sprintf("Qty %d", a.WaterfallQty))
	}
	if a.PopUpOutlet > 0 {
		add(fieldPopUpOutlet, "Pop-up Outlet", fmt.Sprintf("Qty %d", a.PopUpOutlet))
	}
	if a.HalfWall {
		add(fieldHalfWall, "Half Wall", "Yes")
	}
	add(fieldLocation, a.Location, "Location")
	if len(a.FaucetSpread) > 0 {
		add(fieldFaucetSpread, spreadText(kind.scale, a.FaucetSpread), "Faucet Spread")
	}
	return out
}

// catalogRequest names the kits and items the order needs the catalogue to hold.
func catalogRequest(in Input) inventory.CatalogRequest {
	kits := make([]inventory.CatalogKit, 0, len(in.Areas))
	for _, a := range in.Areas {
		lines := areaLines(a)
		items := make([]string, len(lines))
		for i, l := range lines {
			items[i] = l.item
		}
		kits = append(kits, inventory.CatalogKit{Name: a.Name, Items: items})
	}
	return inventory.CatalogRequest{
		Kits: kits, SKUPrefix: skuPrefix, KitDescription: kitDescription, ItemDescription: itemDescription,
	}
}

// buildOrder turns a normalized form into the Sales Order create input.
func buildOrder(in Input, who Submitter, cust customerDefaults, cat *inventory.CatalogResult) (salesorder.CreateOrderInput, error) {
	var so salesorder.CreateOrderInput
	so.CustomerUUID = who.CustomerUUID
	so.InstallDate = in.InstallDate
	so.Memo = orderMemo(in, who)
	so.SalesTaxPercent = cust.salesTaxPercent
	so.SalesRepEmployeeID = cust.salesRepEmployeeID
	so.PaymentTermsID = cust.paymentTermsID
	so.PriceLevelID = cust.priceLevelID
	so.CurrencyID = cust.currencyID
	// The customer's CRM owner looks after the account; failing that, its sales
	// rep. With neither, the order is unowned and any all-scope role sees it.
	so.OwnerEmployeeID = cust.ownerEmployeeID
	if so.OwnerEmployeeID == nil {
		so.OwnerEmployeeID = cust.salesRepEmployeeID
	}
	so.Billing = salesorder.AddressInput{
		Attention: cust.billAttention, Phone: cust.billPhone, Fax: cust.billFax, Email: cust.billEmail,
	}
	// The job site, not the customer's own address.
	so.Shipping = salesorder.AddressInput{
		CustomerName: cust.name, Attention: who.FullName,
		AddrLine1: in.InstallAddress, City: in.City, Zip: in.Zip, Phone: in.Phone, Email: in.Email,
	}

	lineNo := 0
	for _, a := range in.Areas {
		kit, ok := cat.Kits[duplicate.Key(a.Name)]
		if !ok {
			return so, fmt.Errorf("order form: no kit resolved for area %q", a.Name)
		}
		for _, l := range areaLines(a) {
			item, ok := cat.Items[duplicate.Key(l.item)]
			if !ok {
				return so, fmt.Errorf("order form: no item resolved for %q", l.item)
			}
			lineNo++
			so.Items = append(so.Items, salesorder.LineInput2{
				LineNumber:        lineNo,
				InventoryItemUUID: item.ID,
				Description:       l.description,
				Quantity:          1,
				KitUUID:           kit.ID,
				KitName:           kit.Name,
			})
		}
	}
	return so, nil
}

// orderMemo is the job's details, for the Sales Order's Memo — the one free-text
// field the staff edit form round-trips.
func orderMemo(in Input, who Submitter) string {
	lines := append([]string{"Placed from the customer portal by " + submitterLabel(who) + "."}, memoDetails(in)...)
	return strings.Join(lines, "\n")
}

// submitterLabel is who placed (or changed) an order, for a person to read.
func submitterLabel(who Submitter) string {
	by := who.FullName
	switch {
	case by == "":
		by = who.Email
	case who.Email != "":
		by += " (" + who.Email + ")"
	}
	return by
}

// memoDetails are the job's details, one per line, without the heading.
func memoDetails(in Input) []string {
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	add("Builder", in.Builder)
	add("Community", in.Community)
	add("Plan", in.Plan)
	add("Phone", in.Phone)
	add("Email", in.Email)
	add("Install address", fmt.Sprintf("%s, %s %s", in.InstallAddress, in.City, in.Zip))
	add("Measure date", memoDate(in.MeasureDate))
	add("Install date", memoDate(in.InstallDate))
	add("Special instructions", in.SpecialInstructions)
	return lines
}

func memoDate(iso string) string {
	d, err := time.Parse(dateLayout, iso)
	if err != nil {
		return iso
	}
	return d.Format(memoDateLayout)
}
