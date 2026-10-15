package orderform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/inventory"
	"stonesuite-backend/salesorder"
)

// area builds a custom area; the catalogue ids below are derived from names, so
// a test reads as "the Serene item" without a lookup table.
func area(name string, color, edge string) Area {
	return Area{Kind: "custom", Name: name, Color: color, Edge: edge, FaucetSpread: []string{}}
}

func catalogFor(areas ...Area) *inventory.CatalogResult {
	cat := &inventory.CatalogResult{Kits: map[string]inventory.CatalogRef{}, Items: map[string]inventory.CatalogRef{}}
	for _, a := range areas {
		cat.Kits[duplicate.Key(a.Name)] = inventory.CatalogRef{ID: "kit-" + duplicate.Key(a.Name), Name: a.Name}
		for _, l := range areaLines(a) {
			cat.Items[duplicate.Key(l.item)] = inventory.CatalogRef{ID: "item-" + duplicate.Key(l.item), Name: l.item}
		}
	}
	return cat
}

// soLine is a line as the portal first made it for (area, entry).
func portalLine(areaName, entry, desc string) salesorder.LineInput2 {
	return salesorder.LineInput2{
		InventoryItemUUID: "item-" + duplicate.Key(entry), ItemName: entry, Description: desc,
		Quantity: 1, KitUUID: "kit-" + duplicate.Key(areaName), KitName: areaName,
	}
}

func staffLine(desc string) salesorder.LineInput2 {
	return salesorder.LineInput2{InventoryItemUUID: "item-slab", ItemName: "Calacatta Slab", Description: desc, Quantity: 3, UnitPrice: 90}
}

// describe reads each line as "kit/item/description". A line the planner added
// carries only the item's id (the store fills the name from the item master), so
// the id stands in for the name.
func describe(lines []salesorder.LineInput2) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		item := l.ItemName
		if item == "" {
			item = strings.TrimPrefix(l.InventoryItemUUID, "item-")
		}
		out[i] = l.KitName + "/" + item + "/" + l.Description
	}
	return out
}

func TestPlanLines_UnchangedFormLeavesStaffsLinesExactlyAsTheyAre(t *testing.T) {
	a := area("Pantry", "Serene", "Flat")
	color := portalLine("Pantry", "Serene", "Color")
	color.UnitPrice, color.Quantity = 125.5, 42 // staff priced and sized it
	edge := portalLine("Pantry", "Flat", "Edge")
	existing := []salesorder.LineInput2{color, edge, staffLine("Cut-out")}

	got, err := planLines([]Area{a}, []Area{a}, existing, catalogFor(a))

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, 125.5, got[0].UnitPrice)
	assert.Equal(t, 42.0, got[0].Quantity)
	assert.Equal(t, []int{1, 2, 3}, []int{got[0].LineNumber, got[1].LineNumber, got[2].LineNumber})
	assert.Equal(t, "Cut-out", got[2].Description)
}

func TestPlanLines_ChangedEntrySwapsTheItemInPlaceKeepingStaffsQuantity(t *testing.T) {
	was, is := area("Pantry", "Serene", "Flat"), area("Pantry", "Ogee Mist", "Flat")
	color := portalLine("Pantry", "Serene", "Color")
	color.UnitPrice, color.Quantity, color.DiscountPercent = 125.5, 42, 5
	existing := []salesorder.LineInput2{color, portalLine("Pantry", "Flat", "Edge")}

	got, err := planLines([]Area{was}, []Area{is}, existing, catalogFor(is))

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "item-ogee mist", got[0].InventoryItemUUID)
	assert.Equal(t, 0.0, got[0].UnitPrice, "the price is the new item's, which the store fills in")
	assert.Equal(t, 42.0, got[0].Quantity)
	assert.Equal(t, 5.0, got[0].DiscountPercent)
	assert.Equal(t, "Color", got[0].Description)
	assert.Equal(t, "Flat", got[1].ItemName, "the other field is untouched")
}

func TestPlanLines_RemovedFieldAndRemovedAreaDropTheirLines(t *testing.T) {
	pantry, bath := area("Pantry", "Serene", "Flat"), area("Bath 2", "Calm", "Ogee")
	existing := []salesorder.LineInput2{
		portalLine("Pantry", "Serene", "Color"), portalLine("Pantry", "Flat", "Edge"),
		portalLine("Bath 2", "Calm", "Color"), portalLine("Bath 2", "Ogee", "Edge"),
	}
	pantryNoEdge := area("Pantry", "Serene", "")

	got, err := planLines([]Area{pantry, bath}, []Area{pantryNoEdge}, existing, catalogFor(pantryNoEdge))

	require.NoError(t, err)
	assert.Equal(t, []string{"Pantry/Serene/Color"}, describe(got))
}

func TestPlanLines_AddedFieldGoesAfterTheAreasLastLineUnderTheSameHeading(t *testing.T) {
	was, is := area("Pantry", "Serene", ""), area("Pantry", "Serene", "Flat")
	existing := []salesorder.LineInput2{
		portalLine("Pantry", "Serene", "Color"), staffLine("Cut-out"),
		portalLine("Bath 2", "Calm", "Color"),
	}
	bath := area("Bath 2", "Calm", "")

	got, err := planLines([]Area{was, bath}, []Area{is, bath}, existing, catalogFor(is, bath))

	require.NoError(t, err)
	assert.Equal(t, []string{
		"Pantry/Serene/Color", "Pantry/flat/Edge", "/Calacatta Slab/Cut-out", "Bath 2/Calm/Color",
	}, describe(got))
	assert.Equal(t, "kit-pantry", got[1].KitUUID)
	assert.Equal(t, 1.0, got[1].Quantity)
}

func TestPlanLines_AnAddedFieldTakesItsPlaceInTheFormsOwnOrder(t *testing.T) {
	was, is := area("Pantry", "Serene", ""), area("Pantry", "Serene", "Flat")
	was.SinkModel, is.SinkModel = "440185", "440185"
	existing := []salesorder.LineInput2{
		portalLine("Pantry", "Serene", "Color"), portalLine("Pantry", "440185", "Sink Model"),
	}

	got, err := planLines([]Area{was}, []Area{is}, existing, catalogFor(is))

	require.NoError(t, err)
	assert.Equal(t, []string{"Pantry/Serene/Color", "Pantry/flat/Edge", "Pantry/440185/Sink Model"}, describe(got))
}

func TestPlanLines_AddedAreaIsAppendedAtTheEnd(t *testing.T) {
	pantry, bath := area("Pantry", "Serene", ""), area("Bath 2", "Calm", "Ogee")
	existing := []salesorder.LineInput2{portalLine("Pantry", "Serene", "Color"), staffLine("Cut-out")}

	got, err := planLines([]Area{pantry}, []Area{pantry, bath}, existing, catalogFor(pantry, bath))

	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, "Bath 2", got[2].KitName)
	assert.Equal(t, "kit-bath 2", got[2].KitUUID)
	assert.Equal(t, "Bath 2", got[3].KitName)
}

func TestPlanLines_StaffsOwnLinesAreNeverTouched(t *testing.T) {
	a := area("Pantry", "Serene", "")
	mine := staffLine("Cut-out")
	mine.KitName, mine.KitUUID = "Pantry", "kit-pantry" // staff put it under the area's heading

	got, err := planLines([]Area{a}, []Area{}, []salesorder.LineInput2{portalLine("Pantry", "Serene", "Color"), mine}, &inventory.CatalogResult{})

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Cut-out", got[0].Description)
	assert.Equal(t, 1, got[0].LineNumber)
}

func TestPlanLines_ALineStaffRePointedIsNotPutBackWhenTheEntryIsUnchanged(t *testing.T) {
	a := area("Pantry", "Serene", "")
	repointed := portalLine("Pantry", "Calacatta Slab", "Color") // staff swapped the placeholder for the real slab

	got, err := planLines([]Area{a}, []Area{a}, []salesorder.LineInput2{repointed}, catalogFor(a))

	require.NoError(t, err)
	assert.Equal(t, []string{"Pantry/Calacatta Slab/Color"}, describe(got))
}

func TestPlanLines_ALineStaffRePointedGetsAFreshOneWhenTheEntryChanges(t *testing.T) {
	was, is := area("Pantry", "Serene", ""), area("Pantry", "Ogee Mist", "")
	repointed := portalLine("Pantry", "Calacatta Slab", "Color")

	got, err := planLines([]Area{was}, []Area{is}, []salesorder.LineInput2{repointed}, catalogFor(is))

	require.NoError(t, err)
	assert.Equal(t, []string{"Pantry/Calacatta Slab/Color", "Pantry/ogee mist/Color"}, describe(got))
}

func TestPlanLines_CountChangeUpdatesTheDescriptionUnlessStaffReworded(t *testing.T) {
	mk := func(qty int) Area {
		a := area("Bath 2", "Calm", "")
		a.WaterfallQty = qty
		return a
	}
	cases := []struct {
		name     string
		existing string
		want     string
	}{
		{"untouched wording follows the count", "Qty 2", "Qty 3"},
		{"staff's wording is kept", "Two end panels", "Two end panels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := []salesorder.LineInput2{
				portalLine("Bath 2", "Calm", "Color"), portalLine("Bath 2", "Waterfall", tc.existing),
			}

			got, err := planLines([]Area{mk(2)}, []Area{mk(3)}, existing, catalogFor(mk(3)))

			require.NoError(t, err)
			require.Len(t, got, 2)
			assert.Equal(t, tc.want, got[1].Description)
			assert.Equal(t, "Waterfall", got[1].ItemName)
		})
	}
}

func TestPlanLines_TheSameEntryUnderTwoFieldsIsPairedByField(t *testing.T) {
	was := area("Pantry", "Serene", "Serene") // colour and edge happen to read the same
	is := area("Pantry", "Serene", "Flat")
	existing := []salesorder.LineInput2{portalLine("Pantry", "Serene", "Color"), portalLine("Pantry", "Serene", "Edge")}

	got, err := planLines([]Area{was}, []Area{is}, existing, catalogFor(is))

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "item-serene", got[0].InventoryItemUUID, "the colour keeps its item")
	assert.Equal(t, "item-flat", got[1].InventoryItemUUID, "only the edge changed")
	assert.Equal(t, "Edge", got[1].Description)
}

func TestMergeHeader_FollowsTheFormWhereStaffLeftWhatThePortalWrote(t *testing.T) {
	orig := Submitter{FullName: "Brandon Reyes", Email: "brandon@highlandhomes.com"}
	oldIn := validInput()
	newIn := validInput()
	newIn.InstallAddress, newIn.City, newIn.InstallDate = "9 Quarry Rd", "Prosper", "2026-11-02"
	var cur salesorder.UpdateOrderInput
	cur.InstallDate = oldIn.InstallDate
	cur.Memo = orderMemo(oldIn, orig)
	cur.Shipping = salesorder.AddressInput{
		AddrLine1: oldIn.InstallAddress, City: oldIn.City, Zip: oldIn.Zip, Phone: oldIn.Phone, Email: oldIn.Email,
		Attention: "Brandon Reyes",
	}

	got := mergeHeader(cur, oldIn, newIn, orig, portalEdit(orig), now)

	assert.Equal(t, "2026-11-02", got.InstallDate)
	assert.Equal(t, "9 Quarry Rd", got.Shipping.AddrLine1)
	assert.Equal(t, "Prosper", got.Shipping.City)
	assert.Equal(t, oldIn.Zip, got.Shipping.Zip)
	assert.Equal(t, "Brandon Reyes", got.Shipping.Attention)
	assert.Equal(t, orderMemo(newIn, orig), got.Memo)
}

func TestMergeHeader_LeavesWhatStaffChanged(t *testing.T) {
	orig := Submitter{FullName: "Brandon Reyes", Email: "brandon@highlandhomes.com"}
	oldIn := validInput()
	newIn := validInput()
	newIn.InstallAddress, newIn.InstallDate = "9 Quarry Rd", "2026-11-02"
	var cur salesorder.UpdateOrderInput
	cur.InstallDate = "2026-10-30" // the planner moved it
	cur.Memo = orderMemo(oldIn, orig) + "\nCall the super first."
	cur.Shipping.AddrLine1 = "Lot 12, " + oldIn.InstallAddress

	got := mergeHeader(cur, oldIn, newIn, orig, portalEdit(orig), now)

	assert.Equal(t, "2026-10-30", got.InstallDate)
	assert.Equal(t, "Lot 12, 741 Bridle Path", got.Shipping.AddrLine1)
	assert.True(t, strings.HasPrefix(got.Memo, cur.Memo), "staff's memo is kept as it was")
	assert.Contains(t, got.Memo, "Updated from the customer portal by Brandon Reyes")
	assert.Contains(t, got.Memo, "Install address: 9 Quarry Rd, Aubrey 76227")
}

func TestMergeMemo_NothingDescribedChangedLeavesAStaffMemoAlone(t *testing.T) {
	in := validInput()
	edited := "Staff note.\n" + orderMemo(in, Submitter{})

	got := mergeMemo(edited, in, in, Submitter{}, portalEdit(Submitter{}), now)

	assert.Equal(t, edited, got)
}

func TestMergeMemo_ALoginThatNoLongerExistsAppendsRatherThanReplaces(t *testing.T) {
	oldIn := validInput()
	newIn := validInput()
	newIn.Plan = "300"
	written := orderMemo(oldIn, Submitter{FullName: "Brandon Reyes", Email: "brandon@highlandhomes.com"})

	got := mergeMemo(written, oldIn, newIn, Submitter{}, portalEdit(Submitter{FullName: "Dana"}), now)

	assert.True(t, strings.HasPrefix(got, written))
	assert.Contains(t, got, "Plan: 300")
}

func TestMergeMemo_AStaffEditSaysSoInItsHeading(t *testing.T) {
	oldIn := validInput()
	newIn := validInput()
	newIn.Plan = "300"

	got := mergeMemo("Staff note.", oldIn, newIn, Submitter{}, staffEdit("Dana Admin (dana@acme.test)"), now)

	assert.Contains(t, got, "Customer order form updated by Dana Admin (dana@acme.test) on Oct 14, 2026.")
	assert.True(t, strings.HasPrefix(got, "Staff note."))
}
