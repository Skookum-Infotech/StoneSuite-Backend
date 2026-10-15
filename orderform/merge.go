package orderform

import (
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/inventory"
	"stonesuite-backend/salesorder"
)

// Bringing a changed order's Sales Order along without undoing what staff have
// done to it. Staff finish the draft the portal made — specs, sizes, prices,
// extra lines — and a customer who then corrects the order must not wipe that.
//
// A Sales Order line is the form's when it sits under the area's kit (matched by
// the kit name the line carries) and carries the item the form produced for the
// field (matched by item name). Anything else, including a line staff re-pointed
// to another item, is staff's and is never touched. Matching is by name, not by
// a stored link, because every staff save re-inserts the lines under new ids.

// formLine is one line the portal produces for a field of an area.
type formLine struct {
	areaKey  string // duplicate.Key of the area's name: which kit
	field    string // which input of the area
	itemKey  string // duplicate.Key of the customer's entry: which item
	itemName string
	desc     string
}

// fieldID names one field of one area across the old and the new form.
type fieldID struct{ areaKey, field string }

func formLines(areas []Area) []formLine {
	var out []formLine
	for _, a := range areas {
		for _, l := range areaLines(a) {
			out = append(out, formLine{
				areaKey: duplicate.Key(a.Name), field: l.field,
				itemKey: duplicate.Key(l.item), itemName: l.item, desc: l.description,
			})
		}
	}
	return out
}

// claim pairs each line the old form produced with the Sales Order line that is
// still it, returned as existing index -> old line index. A line staff removed
// or re-pointed has no pair.
func claim(olds []formLine, existing []salesorder.LineInput2) map[int]int {
	claimedBy := map[int]int{}
	for oi, o := range olds {
		best := -1
		for j, e := range existing {
			if _, taken := claimedBy[j]; taken || e.InventoryItemUUID == "" {
				continue
			}
			if duplicate.Key(e.KitName) != o.areaKey || duplicate.Key(e.ItemName) != o.itemKey {
				continue
			}
			if e.Description == o.desc { // the same field, not just the same entry
				best = j
				break
			}
			if best < 0 {
				best = j
			}
		}
		if best >= 0 {
			claimedBy[best] = oi
		}
	}
	return claimedBy
}

// planLines returns the Sales Order's lines after the customer's change:
//
//   - a field whose entry is unchanged keeps its line exactly as staff have it;
//   - a field whose entry changed swaps the line's item in place, keeping its
//     position, quantity, size and discount (the price comes from the new item);
//   - a field or area the customer removed drops its line;
//   - a field or area the customer added gets a new quantity-1 line, in the
//     form's own order within its area's kit (or at the end for a new area);
//   - lines that are not the form's stay where they are.
//
// cat resolves the new form's kits and items (see catalogRequest).
func planLines(oldAreas, newAreas []Area, existing []salesorder.LineInput2, cat *inventory.CatalogResult) ([]salesorder.LineInput2, error) {
	olds, news := formLines(oldAreas), formLines(newAreas)
	claimedBy := claim(olds, existing)

	oldAt := make(map[fieldID]int, len(olds))
	for i, o := range olds {
		oldAt[fieldID{o.areaKey, o.field}] = i
	}
	newAt := make(map[fieldID]int, len(news))
	for i, n := range news {
		newAt[fieldID{n.areaKey, n.field}] = i
	}

	// tags runs parallel to out: the index into news of a line that is the form's,
	// -1 for a line that is staff's. It is how a new field finds its place.
	out := make([]salesorder.LineInput2, 0, len(existing)+len(news))
	tags := make([]int, 0, cap(out))
	handled := make(map[int]bool, len(news))
	for j, e := range existing {
		oi, mine := claimedBy[j]
		if !mine {
			out, tags = append(out, e), append(tags, -1)
			continue
		}
		o := olds[oi]
		ni, still := newAt[fieldID{o.areaKey, o.field}]
		if !still {
			continue // the customer removed the field, or the whole area
		}
		n := news[ni]
		handled[ni] = true
		if n.itemKey != o.itemKey {
			item, ok := cat.Items[n.itemKey]
			if !ok {
				return nil, fmt.Errorf("order form: no item resolved for %q", n.itemName)
			}
			// The item master supplies name, SKU and unit again; the price is the
			// new item's, not the old item's.
			e.InventoryItemUUID = item.ID
			e.ItemName, e.SKU, e.UnitCode, e.UnitPrice = "", "", "", 0
		}
		if e.Description == o.desc { // staff's own wording is left alone
			e.Description = n.desc
		}
		out, tags = append(out, e), append(tags, ni)
	}

	for ni, n := range news {
		if handled[ni] {
			continue
		}
		if oi, had := oldAt[fieldID{n.areaKey, n.field}]; had && olds[oi].itemKey == n.itemKey {
			// The customer's entry is the same as before, so staff removed or
			// re-pointed the line on purpose. Do not put it back.
			continue
		}
		item, ok := cat.Items[n.itemKey]
		if !ok {
			return nil, fmt.Errorf("order form: no item resolved for %q", n.itemName)
		}
		kit, ok := cat.Kits[n.areaKey]
		if !ok {
			return nil, fmt.Errorf("order form: no kit resolved for area key %q", n.areaKey)
		}
		line := salesorder.LineInput2{
			InventoryItemUUID: item.ID, Description: n.desc, Quantity: 1, KitUUID: kit.ID, KitName: kit.Name,
		}
		for k := len(out) - 1; k >= 0; k-- {
			if duplicate.Key(out[k].KitName) == n.areaKey {
				line.KitUUID, line.KitName = out[k].KitUUID, out[k].KitName // one heading, not two
				break
			}
		}
		at := insertAt(out, tags, news, ni)
		out = append(out, salesorder.LineInput2{})
		copy(out[at+1:], out[at:])
		out[at] = line
		tags = append(tags, 0)
		copy(tags[at+1:], tags[at:])
		tags[at] = ni
	}
	for i := range out {
		out[i].LineNumber = i + 1
	}
	return out, nil
}

// insertAt picks where the new form line news[ni] goes: right after the nearest
// earlier field of its area that is on the order, else right before the nearest
// later one, else after the area's last line, else at the end (a new area).
func insertAt(out []salesorder.LineInput2, tags []int, news []formLine, ni int) int {
	areaKey := news[ni].areaKey
	prev, next := -1, -1 // positions in out
	for pos, tag := range tags {
		if tag < 0 || news[tag].areaKey != areaKey {
			continue
		}
		if tag < ni && (prev < 0 || tag > tags[prev]) {
			prev = pos
		}
		if tag > ni && (next < 0 || tag < tags[next]) {
			next = pos
		}
	}
	switch {
	case prev >= 0:
		return prev + 1
	case next >= 0:
		return next
	}
	for k := len(out) - 1; k >= 0; k-- {
		if duplicate.Key(out[k].KitName) == areaKey {
			return k + 1
		}
	}
	return len(out)
}

// editInfo says who changed the order and how, for the heading of the block
// appended to a staff-edited memo: "<verb> by <by> on <date>."
type editInfo struct {
	verb string
	by   string
}

// portalEdit is a change made by the customer from the portal.
func portalEdit(who Submitter) editInfo {
	return editInfo{verb: "Updated from the customer portal", by: submitterLabel(who)}
}

// staffEdit is a change made by a super admin from the Sales Order.
func staffEdit(by string) editInfo {
	return editInfo{verb: "Customer order form updated", by: by}
}

// mergeHeader applies the new details to the order's header wherever staff have
// not changed what the portal first wrote there: the install date, the ship-to
// job site and the memo. cur is the order as it stands; orig is who placed it,
// edit who is changing it and how.
func mergeHeader(cur salesorder.UpdateOrderInput, oldIn, newIn Input, orig Submitter, edit editInfo, now time.Time) salesorder.UpdateOrderInput {
	out := cur
	if cur.InstallDate == oldIn.InstallDate {
		out.InstallDate = newIn.InstallDate
	}
	swap := func(field *string, was, becomes string) {
		if *field == was {
			*field = becomes
		}
	}
	swap(&out.Shipping.AddrLine1, oldIn.InstallAddress, newIn.InstallAddress)
	swap(&out.Shipping.City, oldIn.City, newIn.City)
	swap(&out.Shipping.Zip, oldIn.Zip, newIn.Zip)
	swap(&out.Shipping.Phone, oldIn.Phone, newIn.Phone)
	swap(&out.Shipping.Email, oldIn.Email, newIn.Email)
	out.Memo = mergeMemo(cur.Memo, oldIn, newIn, orig, edit, now)
	return out
}

// mergeMemo regenerates the memo when it is still exactly what the portal wrote.
// When staff have added to it, theirs is kept and the new job details are
// appended as a dated block, so the change is visible and nothing they wrote is
// lost.
func mergeMemo(current string, oldIn, newIn Input, orig Submitter, edit editInfo, now time.Time) string {
	if strings.TrimSpace(current) == orderMemo(oldIn, orig) {
		return orderMemo(newIn, orig)
	}
	details := memoDetails(newIn)
	if strings.Join(details, "\n") == strings.Join(memoDetails(oldIn), "\n") {
		return current // nothing the memo describes changed
	}
	heading := fmt.Sprintf("%s by %s on %s.", edit.verb, edit.by, now.Format(memoDateLayout))
	block := strings.Join(append([]string{heading}, details...), "\n")
	if strings.TrimSpace(current) == "" {
		return block
	}
	return strings.TrimRight(current, " \t\r\n") + "\n\n" + block
}
