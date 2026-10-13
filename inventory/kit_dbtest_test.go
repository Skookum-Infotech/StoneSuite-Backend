//go:build dbtest

package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/query"
)

// kitOf creates a kit holding the given items (quantity 1 each) under a unique name.
func kitOf(t *testing.T, name string, items ...*Item) *Kit {
	t.Helper()
	pool := testPool(t)
	members := make([]KitMemberInput, len(items))
	for i, it := range items {
		members[i] = KitMemberInput{ItemID: it.ID, Quantity: 1}
	}
	kit, err := CreateKit(context.Background(), pool, KitInput{Name: name, Members: members}, 1)
	if err != nil {
		t.Fatalf("CreateKit %q: %v", name, err)
	}
	return kit
}

func memberNames(k *Kit) []string {
	out := make([]string, len(k.Members))
	for i, m := range k.Members {
		out[i] = m.Item.Name
	}
	return out
}

func TestCreateKit_KeepsMembersInOrderWithTheirQuantities(t *testing.T) {
	pool := testPool(t)
	a, b, c := newItem(t, pool, 1), newItem(t, pool, 1), newItem(t, pool, 1)

	kit, err := CreateKit(context.Background(), pool, KitInput{
		Name: uniqueName("Kitchen"), Description: "Counters and splash",
		Members: []KitMemberInput{{ItemID: c.ID, Quantity: 2}, {ItemID: a.ID}, {ItemID: b.ID, Quantity: 0.5}},
	}, 1)
	if err != nil {
		t.Fatalf("CreateKit: %v", err)
	}

	if kit.ItemCount != 3 || len(kit.Members) != 3 {
		t.Fatalf("ItemCount = %d, Members = %d, want 3", kit.ItemCount, len(kit.Members))
	}
	want := []struct {
		id  string
		qty float64
	}{{c.ID, 2}, {a.ID, 1}, {b.ID, 0.5}}
	for i, w := range want {
		got := kit.Members[i]
		if got.Item.ID != w.id || got.Quantity != w.qty {
			t.Errorf("member %d = %s × %v, want %s × %v", i, got.Item.ID, got.Quantity, w.id, w.qty)
		}
	}
	if !kit.IsActive {
		t.Error("a kit is active unless it says otherwise")
	}
	if kit.Members[0].Item.Name != c.Name || kit.Members[0].Item.UnitPrice != 25 {
		t.Errorf("a member carries the full item record, got %+v", kit.Members[0].Item)
	}
}

func TestCreateKit_RefusesBadInput(t *testing.T) {
	pool := testPool(t)
	item := newItem(t, pool, 1)
	cases := []struct {
		name string
		in   KitInput
		want string
	}{
		{"blank name", KitInput{Name: "   "}, "name is required"},
		{"name too long", KitInput{Name: strings.Repeat("k", MaxKitNameLen+1)}, "at most"},
		{"same item twice", KitInput{Name: uniqueName("Twice"), Members: []KitMemberInput{{ItemID: item.ID}, {ItemID: strings.ToUpper(item.ID)}}}, "only once"},
		{"negative quantity", KitInput{Name: uniqueName("Neg"), Members: []KitMemberInput{{ItemID: item.ID, Quantity: -1}}}, "greater than zero"},
		{"unknown item", KitInput{Name: uniqueName("Ghost"), Members: []KitMemberInput{{ItemID: "00000000-0000-4000-8000-000000000000"}}}, "no longer exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateKit(context.Background(), pool, tc.in, 1)
			if !IsClientError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a client error containing %q", err, tc.want)
			}
		})
	}
}

func requireKitDuplicate(t *testing.T, err error, wantID string) {
	t.Helper()
	d, ok := duplicate.As(err)
	if !ok {
		t.Fatalf("err = %v, want a *duplicate.Error", err)
	}
	if d.Entity != duplicate.EntityKit || d.ExistingID != wantID {
		t.Errorf("duplicate = %+v, want a kit with id %s", d, wantID)
	}
}

func TestCreateKit_SameNameIsRejected_AnyCaseOrSpacing(t *testing.T) {
	pool := testPool(t)
	name := uniqueName("Bathroom Set")
	first := kitOf(t, name)

	for _, variant := range []string{strings.ToUpper(name), "  " + strings.ToLower(name) + "  ", strings.Replace(name, " ", "   ", 1)} {
		_, err := CreateKit(context.Background(), pool, KitInput{Name: variant}, 1)
		requireKitDuplicate(t, err, first.ID)
	}
}

func TestKitName_InactiveStillBlocksAndDeletedFrees(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	name := uniqueName("Laundry")
	first := kitOf(t, name)

	off := false
	if err := UpdateKit(ctx, pool, first.ID, KitInput{Name: name, IsActive: &off}, 1); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	_, err := CreateKit(ctx, pool, KitInput{Name: name}, 1)
	requireKitDuplicate(t, err, first.ID)
	if d, _ := duplicate.As(err); d.ExistingStatus != "Inactive" {
		t.Errorf("ExistingStatus = %q, want Inactive", d.ExistingStatus)
	}

	if err := SoftDeleteKit(ctx, pool, first.ID, 1); err != nil {
		t.Fatalf("SoftDeleteKit: %v", err)
	}
	if _, err := CreateKit(ctx, pool, KitInput{Name: name}, 1); err != nil {
		t.Fatalf("a deleted kit's name is free again: %v", err)
	}
	if _, err := GetKit(ctx, pool, first.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetKit of a deleted kit = %v, want ErrNotFound", err)
	}
}

func TestUpdateKit_MembersAreReplacedOnlyWhenSent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a, b := newItem(t, pool, 1), newItem(t, pool, 1)
	kit := kitOf(t, uniqueName("Patio"), a)

	// nil members: rename only, items stay.
	renamed := uniqueName("Patio Deluxe")
	if err := UpdateKit(ctx, pool, kit.ID, KitInput{Name: renamed, Description: "Outdoor"}, 1); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, _ := GetKit(ctx, pool, kit.ID)
	if got.Name != renamed || got.Description != "Outdoor" || len(got.Members) != 1 {
		t.Fatalf("after rename: %+v", got)
	}
	if !got.IsActive {
		t.Error("an omitted isActive must leave the kit as it was")
	}

	// a list replaces them.
	if err := UpdateKit(ctx, pool, kit.ID, KitInput{Name: renamed, Members: []KitMemberInput{{ItemID: b.ID, Quantity: 3}}}, 1); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = GetKit(ctx, pool, kit.ID)
	if len(got.Members) != 1 || got.Members[0].Item.ID != b.ID || got.Members[0].Quantity != 3 {
		t.Fatalf("after replace: %+v", got.Members)
	}

	// an explicit empty list empties it.
	if err := UpdateKit(ctx, pool, kit.ID, KitInput{Name: renamed, Members: []KitMemberInput{}}, 1); err != nil {
		t.Fatalf("empty: %v", err)
	}
	got, _ = GetKit(ctx, pool, kit.ID)
	if len(got.Members) != 0 || got.ItemCount != 0 {
		t.Fatalf("after emptying: %+v", got)
	}
}

func TestUpdateKit_RenameOntoAnotherKitIsRejected(t *testing.T) {
	pool := testPool(t)
	taken := kitOf(t, uniqueName("Taken Kit"))
	other := kitOf(t, uniqueName("Other Kit"))

	err := UpdateKit(context.Background(), pool, other.ID, KitInput{Name: taken.Name}, 1)
	requireKitDuplicate(t, err, taken.ID)
}

func TestKit_UnknownOrMalformedIDIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for _, id := range []string{"00000000-0000-4000-8000-000000000000", "not-a-uuid"} {
		if _, err := GetKit(ctx, pool, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetKit(%q) = %v, want ErrNotFound", id, err)
		}
		if err := UpdateKit(ctx, pool, id, KitInput{Name: "x"}, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateKit(%q) = %v, want ErrNotFound", id, err)
		}
		if err := SoftDeleteKit(ctx, pool, id, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("SoftDeleteKit(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

// A kit lists the items that are still live: deleting an item takes it out of
// its kits' contents and counts.
func TestGetKit_LeavesOutDeletedItems(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keep, drop := newItem(t, pool, 1), newItem(t, pool, 1)
	kit := kitOf(t, uniqueName("Shrinking"), keep, drop)

	if err := SoftDelete(ctx, pool, drop.ID, 1); err != nil {
		t.Fatalf("delete item: %v", err)
	}
	got, err := GetKit(ctx, pool, kit.ID)
	if err != nil {
		t.Fatalf("GetKit: %v", err)
	}
	if got.ItemCount != 1 || len(got.Members) != 1 || got.Members[0].Item.ID != keep.ID {
		t.Fatalf("members after deleting an item = %v (count %d)", memberNames(got), got.ItemCount)
	}
}

func TestSearchKits_FindsByNameAndCountsItems(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniqueName("Searchable")
	a, b := newItem(t, pool, 1), newItem(t, pool, 1)
	hit := kitOf(t, tag+" Kitchen", a, b)
	kitOf(t, uniqueName("Unrelated"))

	page, err := SearchKits(ctx, pool, query.Request{Search: tag, Limit: 10})
	if err != nil {
		t.Fatalf("SearchKits: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != hit.ID {
		t.Fatalf("records = %+v, want only %s", page.Records, hit.ID)
	}
	if page.Records[0].ItemCount != 2 {
		t.Errorf("ItemCount = %d, want 2", page.Records[0].ItemCount)
	}
	if len(page.Records[0].Members) != 0 {
		t.Error("a search row carries the count, not the members")
	}
}

func TestSearchKits_PagesByName(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniqueName("Paged")
	for _, suffix := range []string{"C", "A", "B"} {
		kitOf(t, tag+" "+suffix)
	}

	req := query.Request{Search: tag, Limit: 2, Sort: []query.SortKey{{Field: "name", Dir: query.DirAsc}}}
	first, err := SearchKits(ctx, pool, req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first.Records) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("page 1 = %+v", first)
	}
	req.Cursor = first.NextCursor
	second, err := SearchKits(ctx, pool, req)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	got := []string{first.Records[0].Name, first.Records[1].Name, second.Records[0].Name}
	want := []string{tag + " A", tag + " B", tag + " C"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if second.HasMore {
		t.Error("the last page has no more")
	}
}

// ── the item's side ──────────────────────────────────────────────────────────

func kitNames(refs []KitRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Name
	}
	return out
}

func TestItem_CreateIntoKitsAndSetKitsFromTheItemForm(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	kitchen := kitOf(t, uniqueName("Kitchen"))
	bath := kitOf(t, uniqueName("Bathroom"))

	item, err := Create(ctx, pool, CreateItemInput{
		SKU: uniqueName("KSKU"), Name: uniqueName("Faucet"), UnitID: 1, UnitPrice: 10,
		KitIDs: []string{kitchen.ID, bath.ID, kitchen.ID}, // a repeat is harmless
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(item.Kits) != 2 {
		t.Fatalf("item.Kits = %v, want both kits", kitNames(item.Kits))
	}
	got, _ := GetKit(ctx, pool, kitchen.ID)
	if len(got.Members) != 1 || got.Members[0].Item.ID != item.ID || got.Members[0].Quantity != 1 {
		t.Fatalf("kitchen members = %+v, want the new item × 1", got.Members)
	}

	// Raise the quantity from the kit's side; the item form must not reset it.
	if err := UpdateKit(ctx, pool, kitchen.ID, KitInput{Name: kitchen.Name,
		Members: []KitMemberInput{{ItemID: item.ID, Quantity: 4}}}, 1); err != nil {
		t.Fatalf("set quantity: %v", err)
	}

	// Omitted kitIds: the item stays in both kits.
	if err := Update(ctx, pool, item.ID, CreateItemInput{SKU: item.SKU, Name: item.Name, UnitID: 1}, 1); err != nil {
		t.Fatalf("Update without kitIds: %v", err)
	}
	if again, _ := Get(ctx, pool, item.ID); len(again.Kits) != 2 {
		t.Fatalf("an update that omits kitIds must leave the kits alone, got %v", kitNames(again.Kits))
	}

	// Keep kitchen, leave bathroom.
	if err := Update(ctx, pool, item.ID, CreateItemInput{SKU: item.SKU, Name: item.Name, UnitID: 1,
		KitIDs: []string{kitchen.ID}}, 1); err != nil {
		t.Fatalf("Update with kitIds: %v", err)
	}
	after, _ := Get(ctx, pool, item.ID)
	if len(after.Kits) != 1 || after.Kits[0].ID != kitchen.ID {
		t.Fatalf("after leaving bathroom: %v", kitNames(after.Kits))
	}
	kept, _ := GetKit(ctx, pool, kitchen.ID)
	if kept.Members[0].Quantity != 4 {
		t.Errorf("a membership kept through an item save keeps its quantity, got %v", kept.Members[0].Quantity)
	}

	// An explicit empty list takes the item out of every kit.
	if err := Update(ctx, pool, item.ID, CreateItemInput{SKU: item.SKU, Name: item.Name, UnitID: 1,
		KitIDs: []string{}}, 1); err != nil {
		t.Fatalf("Update with empty kitIds: %v", err)
	}
	if none, _ := Get(ctx, pool, item.ID); len(none.Kits) != 0 {
		t.Fatalf("kits after emptying = %v", kitNames(none.Kits))
	}

	// The changes are in the item's history.
	hist, err := ItemHistory(ctx, pool, item.ID)
	if err != nil {
		t.Fatalf("ItemHistory: %v", err)
	}
	var kitEvents int
	for _, h := range hist {
		if h.Field == "kits" {
			kitEvents++
		}
	}
	if kitEvents != 3 {
		t.Errorf("kit history entries = %d, want 3 (joined, left one, left all)", kitEvents)
	}
}

func TestItem_UnknownKitIsRefusedAndNothingIsSaved(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := newItem(t, pool, 1)

	err := Update(ctx, pool, item.ID, CreateItemInput{
		SKU: item.SKU, Name: "Renamed " + item.Name, UnitID: 1,
		KitIDs: []string{"00000000-0000-4000-8000-000000000000"},
	}, 1)
	if !IsClientError(err) {
		t.Fatalf("err = %v, want a client error", err)
	}
	after, _ := Get(ctx, pool, item.ID)
	if after.Name != item.Name {
		t.Errorf("the item's other edits must roll back with the failed kit change, name = %q", after.Name)
	}
}

func TestItem_KitsListIsEmptyForListRowsAndFullForGet(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	kit := kitOf(t, uniqueName("Garage"))
	item, err := Create(ctx, pool, CreateItemInput{
		SKU: uniqueName("GSKU"), Name: uniqueName("Shelf"), UnitID: 1, KitIDs: []string{kit.ID},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, query.Request{Search: item.Name, Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page.Records) != 1 || len(page.Records[0].Kits) != 0 {
		t.Errorf("a list row carries no kits (no query per row), got %+v", page.Records)
	}
	got, _ := Get(ctx, pool, item.ID)
	if len(got.Kits) != 1 || got.Kits[0].Name != kit.Name {
		t.Errorf("Get carries the kits, got %v", kitNames(got.Kits))
	}
}
