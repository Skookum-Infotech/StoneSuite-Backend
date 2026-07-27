// inventory/bundle_store_test.go
//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedBin creates a live bin in warehouse 1 and returns its uuid.
func seedBin(t *testing.T, pool *pgxpool.Pool, code string) string {
	t.Helper()
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path)
		VALUES (1, $1, $1, 'rack', $1)
		RETURNING inventory_bin_uuid`, code).Scan(&uuid); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	return uuid
}

// seedBundleUnit receives one slab into warehouse 1.
func seedBundleUnit(t *testing.T, pool *pgxpool.Pool, item string) *Unit {
	t.Helper()
	u, err := CreateUnit(context.Background(), pool, CreateUnitInput{
		Serial: uniq("DBTEST-BU"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	return u
}

func TestBundle_SealFreezesMembershipAndBreakReleasesIt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-BUNDLE"))
	a := seedBundleUnit(t, pool, item)
	b := seedBundleUnit(t, pool, item)
	c := seedBundleUnit(t, pool, item)

	bundle, err := CreateBundle(ctx, pool, BundleInput{
		Code: uniq("DBTEST-B"), WarehouseID: 1, BlockID: "BLK-9",
		MemberIDs: []string{a.ID, b.ID},
	}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if bundle.MemberCount != 2 {
		t.Fatalf("MemberCount = %d, want 2", bundle.MemberCount)
	}
	// The bundle had no item; its first member fixes it, so TotalArea is never
	// summing two units of measure.
	if bundle.InventoryItemID == nil || *bundle.InventoryItemID != item {
		t.Errorf("bundle item = %v, want it adopted from the first member (%s)", bundle.InventoryItemID, item)
	}
	if bundle.TotalArea != a.Area+b.Area {
		t.Errorf("TotalArea = %v, want %v", bundle.TotalArea, a.Area+b.Area)
	}

	sealed, err := SealBundle(ctx, pool, bundle.ID, 1)
	if err != nil {
		t.Fatalf("SealBundle: %v", err)
	}
	if sealed.Status != BundleSealed {
		t.Fatalf("status = %q, want %q", sealed.Status, BundleSealed)
	}

	// Sealing bands the pallet: membership freezes and a single member cannot
	// walk off on its own.
	if _, err := AddBundleMembers(ctx, pool, bundle.ID, BundleMemberInput{MemberIDs: []string{c.ID}}, 1); err == nil {
		t.Error("expected adding to a sealed bundle to be refused")
	}
	if _, err := RemoveBundleMembers(ctx, pool, bundle.ID, BundleMemberInput{MemberIDs: []string{a.ID}}, 1); err == nil {
		t.Error("expected removing from a sealed bundle to be refused")
	}
	bin := seedBin(t, pool, uniq("DBTEST-BIN"))
	if err := MoveUnitToBin(ctx, pool, a.ID, MoveUnitInput{BinUUID: &bin}, 1); err == nil {
		t.Error("expected a single-member move out of a sealed bundle to be refused")
	}
	// Nor can it be sawn up while it is still banded to the pallet.
	if _, err := CutUnit(ctx, pool, a.ID, CutInput{
		Remnants: []CutPiece{{Serial: uniq("DBTEST-RX"), LengthMM: 900, WidthMM: 600}},
	}, 1); err == nil {
		t.Error("expected cutting a sealed-bundle member to be refused")
	}

	// Breaking cuts the band: members are detached and independent again.
	broken, err := BreakBundle(ctx, pool, bundle.ID, "shipped one off", 1)
	if err != nil {
		t.Fatalf("BreakBundle: %v", err)
	}
	if broken.Status != BundleBroken {
		t.Errorf("status = %q, want %q", broken.Status, BundleBroken)
	}
	if broken.MemberCount != 0 {
		t.Errorf("MemberCount after break = %d, want 0", broken.MemberCount)
	}
	reloaded, err := GetUnit(ctx, pool, a.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if reloaded.BundleUUID != nil {
		t.Errorf("member is still attached to bundle %v after break", reloaded.BundleUUID)
	}
	// Provenance survives the band being cut — that is what a vendor claim needs.
	if reloaded.BundleID != bundle.Code {
		t.Errorf("bundle label = %q, want the code %q kept as provenance", reloaded.BundleID, bundle.Code)
	}
	// And now it moves freely.
	if err := MoveUnitToBin(ctx, pool, a.ID, MoveUnitInput{BinUUID: &bin}, 1); err != nil {
		t.Errorf("moving a released member: %v", err)
	}
}

func TestBundle_HoldsEveryMemberToOneItem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	itemA := seedAreaItem(t, pool, uniq("DBTEST-BUNDLE-A"))
	itemB := seedAreaItem(t, pool, uniq("DBTEST-BUNDLE-B"))
	granite := seedBundleUnit(t, pool, itemA)
	marble := seedBundleUnit(t, pool, itemB)

	bundle, err := CreateBundle(ctx, pool, BundleInput{
		Code: uniq("DBTEST-B"), WarehouseID: 1, MemberIDs: []string{granite.ID},
	}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	// A pallet is sawn from one block. Mixing items would also make TotalArea sum
	// across two units of measure.
	if _, err := AddBundleMembers(ctx, pool, bundle.ID,
		BundleMemberInput{MemberIDs: []string{marble.ID}}, 1); err == nil {
		t.Error("expected a second item in one bundle to be refused")
	}

	// A unit already on another pallet cannot be double-banded.
	other, err := CreateBundle(ctx, pool, BundleInput{Code: uniq("DBTEST-B2"), WarehouseID: 1}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if _, err := AddBundleMembers(ctx, pool, other.ID,
		BundleMemberInput{MemberIDs: []string{granite.ID}}, 1); err == nil {
		t.Error("expected bundling a unit that is already bundled to be refused")
	}

	// Attaching a unit twice in one call is a no-op, not a duplicate.
	dup := seedBundleUnit(t, pool, itemA)
	got, err := AddBundleMembers(ctx, pool, bundle.ID,
		BundleMemberInput{MemberIDs: []string{dup.ID, dup.ID}}, 1)
	if err != nil {
		t.Fatalf("AddBundleMembers: %v", err)
	}
	if got.MemberCount != 2 {
		t.Errorf("MemberCount = %d, want 2 after a duplicated id", got.MemberCount)
	}
}

func TestMoveBundle_MovesEveryMemberAndLeavesStockAlone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-BUNDLE"))
	a := seedBundleUnit(t, pool, item)
	b := seedBundleUnit(t, pool, item)

	bundle, err := CreateBundle(ctx, pool, BundleInput{
		Code: uniq("DBTEST-B"), WarehouseID: 1, MemberIDs: []string{a.ID, b.ID},
	}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if _, err := SealBundle(ctx, pool, bundle.ID, 1); err != nil {
		t.Fatalf("SealBundle: %v", err)
	}
	before := onHand(t, pool, item)

	bin := seedBin(t, pool, uniq("DBTEST-BIN"))
	if err := MoveBundle(ctx, pool, bundle.ID, MoveBundleInput{BinUUID: &bin, Note: "to rack 4"}, 1); err != nil {
		t.Fatalf("MoveBundle: %v", err)
	}

	// A bundle move is stock-neutral by construction: bins locate units and
	// inventory_stock is keyed on (item, warehouse). A ledger row here would be a
	// phantom movement.
	if got := onHand(t, pool, item); got != before {
		t.Errorf("a bundle move changed stock: %v -> %v", before, got)
	}
	if got := ledgerSum(t, pool, item); got != before {
		t.Errorf("ledger sum %v != on-hand %v after a bundle move", got, before)
	}

	// Every member came with it — leaving one behind is the failure that makes
	// the paperwork lie about where the stone is.
	for _, id := range []string{a.ID, b.ID} {
		u, err := GetUnit(ctx, pool, id)
		if err != nil {
			t.Fatalf("GetUnit: %v", err)
		}
		if u.BinID == nil || *u.BinID != bin {
			t.Errorf("member %s is in bin %v, want %s", u.Serial, u.BinID, bin)
		}
	}
	reloaded, err := GetBundle(ctx, pool, bundle.ID)
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if reloaded.BinID == nil || *reloaded.BinID != bin {
		t.Errorf("bundle bin = %v, want %s", reloaded.BinID, bin)
	}
}

func TestDeleteBundle_RefusesWhileUnitsAreAttached(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-BUNDLE"))
	a := seedBundleUnit(t, pool, item)

	bundle, err := CreateBundle(ctx, pool, BundleInput{
		Code: uniq("DBTEST-B"), WarehouseID: 1, MemberIDs: []string{a.ID},
	}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if err := DeleteBundle(ctx, pool, bundle.ID, 1); err == nil {
		t.Error("expected deleting an occupied bundle to be refused")
	}
	if _, err := BreakBundle(ctx, pool, bundle.ID, "", 1); err != nil {
		t.Fatalf("BreakBundle: %v", err)
	}
	if err := DeleteBundle(ctx, pool, bundle.ID, 1); err != nil {
		t.Errorf("deleting an emptied bundle: %v", err)
	}
	if _, err := GetBundle(ctx, pool, bundle.ID); err != ErrNotFound {
		t.Errorf("GetBundle after delete = %v, want ErrNotFound", err)
	}
}

func TestUpdateBundle_RefusesARenameOnceUnitsCarryTheCode(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-BUNDLE"))
	a := seedBundleUnit(t, pool, item)

	bundle, err := CreateBundle(ctx, pool, BundleInput{Code: uniq("DBTEST-B"), WarehouseID: 1}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	// Empty: still free to rename.
	renamed := uniq("DBTEST-B-NEW")
	if err := UpdateBundle(ctx, pool, bundle.ID, BundleInput{
		Code: renamed, WarehouseID: 1, Notes: "retagged",
	}, 1); err != nil {
		t.Fatalf("UpdateBundle on an empty bundle: %v", err)
	}

	if _, err := AddBundleMembers(ctx, pool, bundle.ID, BundleMemberInput{MemberIDs: []string{a.ID}}, 1); err != nil {
		t.Fatalf("AddBundleMembers: %v", err)
	}
	// Now the code is stamped on a slab as provenance, so it is frozen.
	if err := UpdateBundle(ctx, pool, bundle.ID, BundleInput{
		Code: uniq("DBTEST-B-AGAIN"), WarehouseID: 1,
	}, 1); err == nil {
		t.Error("expected renaming an occupied bundle to be refused")
	}
	// Editing everything else still works.
	if err := UpdateBundle(ctx, pool, bundle.ID, BundleInput{
		Code: renamed, WarehouseID: 1, Lot: "LOT-77", Notes: "chipped corner",
	}, 1); err != nil {
		t.Errorf("UpdateBundle keeping the code: %v", err)
	}
	member, err := GetUnit(ctx, pool, a.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if member.BundleID != renamed {
		t.Errorf("member carries bundle label %q, want %q", member.BundleID, renamed)
	}
}

func TestSealBundle_RefusesAnEmptyPallet(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bundle, err := CreateBundle(ctx, pool, BundleInput{Code: uniq("DBTEST-B"), WarehouseID: 1}, 1)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	// Sealing nothing would freeze a bundle that accepts no units and holds none.
	if _, err := SealBundle(ctx, pool, bundle.ID, 1); err == nil {
		t.Error("expected sealing an empty bundle to be refused")
	}
}
