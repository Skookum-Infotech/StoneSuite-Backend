package orderform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/inventory"
)

// linePair is a line as the Sales Order shows it: the item and what it is for.
type linePair struct{ item, description string }

func pairs(lines []fieldLine) []linePair {
	out := make([]linePair, len(lines))
	for i, l := range lines {
		out[i] = linePair{l.item, l.description}
	}
	return out
}

func TestAreaLines_KeysEachLineByTheFieldItCameFrom(t *testing.T) {
	a := Area{
		Kind: "kitchen", Name: "Kitchen", Color: "Serene", Edge: "Flat", WaterfallQty: 2,
		FaucetSpread: []string{"C"},
	}

	var keys []string
	for _, l := range areaLines(a) {
		keys = append(keys, l.field)
	}

	assert.Equal(t, []string{fieldColor, fieldEdge, fieldWaterfallQty, fieldFaucetSpread}, keys)
}

func TestAreaLines(t *testing.T) {
	cases := []struct {
		name string
		area Area
		want []linePair
	}{
		{
			name: "only filled fields, in the form's order",
			area: Area{
				Kind: "kitchen", Name: "Kitchen", Color: "Serene", Edge: "Flat",
				BacksplashHeight: "4", SinkModel: "440185", CooktopModel: "CT-36",
				WaterfallQty: 2, FaucetSpread: []string{"L8", "C", "R8"},
			},
			want: []linePair{
				{"Serene", "Color"}, {"Flat", "Edge"}, {`4" Backsplash`, "Backsplash Height"},
				{"440185", "Sink Model"}, {"CT-36", "Cooktop Model"}, {"Waterfall", "Qty 2"},
				{`8" left, center, 8" right`, "Faucet Spread"},
			},
		},
		{
			name: "a count of zero and half wall no are not lines",
			area: Area{Kind: "master_bath", Name: "Primary / Master Bath", Color: "Serene"},
			want: []linePair{{"Serene", "Color"}},
		},
		{
			name: "location shows on a powder room",
			area: Area{Kind: "powder_1", Name: "Powder 1", Color: "Serene", Location: "Window sill"},
			want: []linePair{{"Serene", "Color"}, {"Window sill", "Location"}},
		},
		{
			name: "an island pop-up outlet is a quantity line",
			area: Area{Kind: "island", Name: "Island", Color: "Serene", PopUpOutlet: 1},
			want: []linePair{{"Serene", "Color"}, {"Pop-up Outlet", "Qty 1"}},
		},
		{
			name: "half wall yes is a line",
			area: Area{Kind: "master_bath", Name: "Master", Color: "Serene", HalfWall: true},
			want: []linePair{{"Serene", "Color"}, {"Half Wall", "Yes"}},
		},
		{
			name: "narrow row reads its own labels",
			area: Area{Kind: "bath", Name: "Bath", Color: "Serene", FaucetSpread: []string{"L2", "R4"}},
			want: []linePair{{"Serene", "Color"}, {`2" left, 4" right`, "Faucet Spread"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pairs(areaLines(tc.area)))
		})
	}
}

func TestCatalogRequest_NamesEachAreaAsAKitHoldingItsEntries(t *testing.T) {
	in := Input{Areas: []Area{
		{Kind: "kitchen", Name: "Kitchen", Color: "Serene", Edge: "Flat"},
		{Kind: "island", Name: "Island", Color: "Serene", Edge: "Ogee"},
	}}

	req := catalogRequest(in)

	require.Len(t, req.Kits, 2)
	assert.Equal(t, inventory.CatalogKit{Name: "Kitchen", Items: []string{"Serene", "Flat"}}, req.Kits[0])
	assert.Equal(t, inventory.CatalogKit{Name: "Island", Items: []string{"Serene", "Ogee"}}, req.Kits[1])
	assert.Equal(t, "PORTAL-", req.SKUPrefix)
}

// resolved fakes what EnsureCatalog returns for the given kit and item names.
func resolved(kits, items []string) *inventory.CatalogResult {
	res := &inventory.CatalogResult{Kits: map[string]inventory.CatalogRef{}, Items: map[string]inventory.CatalogRef{}}
	for _, k := range kits {
		res.Kits[duplicate.Key(k)] = inventory.CatalogRef{ID: "kit-" + k, Name: k}
	}
	for _, i := range items {
		res.Items[duplicate.Key(i)] = inventory.CatalogRef{ID: "item-" + i, Name: i}
	}
	return res
}

func TestBuildOrder_HeaderAndLines(t *testing.T) {
	owner, rep, terms := 7, 9, 3
	cust := customerDefaults{
		name: "Highland Homes LLC", ownerEmployeeID: &owner, salesRepEmployeeID: &rep, paymentTermsID: &terms,
		salesTaxPercent: 8.25, billAttention: "Pat Lee", billPhone: "555-0100", billEmail: "ap@highland.test",
	}
	who := Submitter{CustomerUUID: "cust-uuid", FullName: "Brandon Reyes", Email: "brandon@highlandhomes.com"}
	in := validInput()
	in.SpecialInstructions = "Gate code 1234."
	in.Areas = []Area{
		{Kind: "kitchen", Name: "Kitchen", Color: "Serene", Edge: "Flat"},
		{Kind: "island", Name: "Island", Color: "Serene"},
	}

	so, err := buildOrder(in, who, cust, resolved([]string{"Kitchen", "Island"}, []string{"Serene", "Flat"}))

	require.NoError(t, err)
	assert.Equal(t, "cust-uuid", so.CustomerUUID)
	assert.Equal(t, "2026-10-26", so.InstallDate)
	assert.Equal(t, 8.25, so.SalesTaxPercent)
	assert.Equal(t, &owner, so.OwnerEmployeeID)
	assert.Equal(t, &rep, so.SalesRepEmployeeID)
	assert.Equal(t, &terms, so.PaymentTermsID)
	assert.Equal(t, "Pat Lee", so.Billing.Attention)
	assert.Equal(t, "741 Bridle Path", so.Shipping.AddrLine1)
	assert.Equal(t, "Aubrey", so.Shipping.City)
	assert.Equal(t, "Highland Homes LLC", so.Shipping.CustomerName)
	assert.Equal(t, "Brandon Reyes", so.Shipping.Attention)

	require.Len(t, so.Items, 3)
	first := so.Items[0]
	assert.Equal(t, 1, first.LineNumber)
	assert.Equal(t, "item-Serene", first.InventoryItemUUID)
	assert.Equal(t, "Color", first.Description)
	assert.Equal(t, float64(1), first.Quantity)
	assert.Equal(t, "kit-Kitchen", first.KitUUID)
	assert.Equal(t, "Kitchen", first.KitName)
	assert.Equal(t, 3, so.Items[2].LineNumber)
	assert.Equal(t, "kit-Island", so.Items[2].KitUUID)
}

func TestBuildOrder_OwnerFallsBackToTheSalesRep(t *testing.T) {
	rep := 9
	so, err := buildOrder(withAreas(kitchen()), Submitter{}, customerDefaults{salesRepEmployeeID: &rep},
		resolved([]string{"Kitchen"}, []string{"Serene"}))

	require.NoError(t, err)
	assert.Equal(t, &rep, so.OwnerEmployeeID)
}

func TestBuildOrder_FailsWhenTheCatalogMissedAName(t *testing.T) {
	_, err := buildOrder(withAreas(kitchen()), Submitter{}, customerDefaults{}, resolved([]string{"Kitchen"}, nil))

	assert.ErrorContains(t, err, `no item resolved for "Serene"`)
}

func TestOrderMemo(t *testing.T) {
	in := validInput()
	in.SpecialInstructions = "Gate code 1234."

	memo := orderMemo(in, Submitter{FullName: "Brandon Reyes", Email: "brandon@highlandhomes.com"})

	assert.Equal(t, "Placed from the customer portal by Brandon Reyes (brandon@highlandhomes.com).\n"+
		"Builder: Highland Homes\nCommunity: Sandbrock Ranch\nPlan: 214\nPhone: +14802095330\n"+
		"Email: brandon@highlandhomes.com\nInstall address: 741 Bridle Path, Aubrey 76227\n"+
		"Measure date: Oct 16, 2026\nInstall date: Oct 26, 2026\nSpecial instructions: Gate code 1234.", memo)
}

func TestOrderMemo_FallsBackToTheEmailWithoutAName(t *testing.T) {
	memo := orderMemo(validInput(), Submitter{Email: "brandon@highlandhomes.com"})

	assert.Contains(t, memo, "Placed from the customer portal by brandon@highlandhomes.com.")
}
