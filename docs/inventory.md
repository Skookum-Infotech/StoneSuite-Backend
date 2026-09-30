# Inventory

StoneSuite's Inventory area tracks what you have on hand — individual items,
the physical slabs and bundles they come in, and where they're stored — plus
the adjustments, transfers, and counts that keep those numbers accurate. Find
it under the Inventory section of the sidebar.

## Inventory Items

An Inventory Item is a catalog entry for something you stock, created from
Inventory → Items → New Inventory Item. Its detail page shows Primary
Information and Stone Attributes (material, color, finish, and similar
properties specific to stone). Items are the building block every other
inventory record — slabs, bundles, adjustments, transfers, counts — refers
back to.

An item's Track stock setting decides whether a sales order for it is checked
against stock and holds it. It is on for every item unless you switch it off,
which you should for anything with no stock to run out of — a delivery,
installation or labour charge. A slab-tracked item is always tracked, and its
switch cannot be turned off.

## Units and Slabs

A Unit is an individual physical slab or piece tracked on its own. Every unit
is listed under Inventory → Inventory in the sidebar. You don't add units
there: a slab enters inventory when it is received against a purchase order —
see Item Receipts in the Purchasing guide — and it takes the serial number
assigned during that receipt. Its detail page records the item it belongs to,
the receipt it arrived on, its location, dimensions, attributes, and its
lineage (where it came from).

A slab is never partly used: it is either still whole, held for a fabrication
job, cut, or scrapped. When a slab is cut, all of it leaves stock, the offcuts
you keep come back as new units, and whatever did not come back — the finished
product and the saw kerf — counts as used. The Inventory list shows this for
every unit in its Consumption column: a small bar and a line such as
"Untouched", "Held for FJOB-000012", or "66% used · 15.20 sq ft back".

A slab that a fabrication job has claimed is also marked in the list's Allocated
To column with the sales order it is for and the fabrication job, each a link to
that record. It stays there once the slab is cut, so you can always tell whose
stone it was. The slab's own page shows the same two links in a strip under its
title on every tab.

Opening a unit takes you to its Usage tab, which is about exactly that. It leads
with how much of the piece is gone ("Untouched", "66% used", "Fully used"), a bar
and the numbers behind it — the size before cutting, what was used, and what came
back as offcuts. Below that is the unit's lifecycle: where it was received (or
which slab it was cut from), the fabrication job that held it, when it was cut or
scrapped, and where it is now. A cut slab also lists its offcuts, each linked to
its own page. The unit's fixed facts — item, dimensions, grade, location and
lineage — are on the Details tab, and its movement trail on the History tab.

## Bundles

A Bundle groups related units together — for example, several slabs from the
same lot — so you can track and move them as one group instead of handling
each slab separately. Find and open bundles from Inventory → Bundles; each
bundle's detail page shows its bundle information and the inventory item
it's associated with.

## Bin Management

Bins are the specific storage spots within a location — the shelf,
rack, or slot something actually sits in, one level more specific than the
location itself. Manage them from Inventory → Bin Management, so your
units and items can be tracked down to exactly where they're stored.

## Locations

A Location is a place where you hold inventory — a yard, shop, or facility.
Locations are the same ones you set up under Configuration → Company Info →
Locations, which is the only place they are added, edited, marked as the
default, or deleted. Inventory → Locations lists them read-only, with a star
badge on the default. A location that still holds stock or bins cannot be
deleted until it is emptied.

## Adjusting Inventory

An Adjustment corrects the recorded quantity of an item — for damage, loss,
or a count correction — created from Inventory → Adjust Inventory → New
Adjustment. Give it a location and header information, then add the item
lines being adjusted, each with the corrected quantity and a reason. Once
saved, the adjustment's own record shows the location it applied to in its
title.

## Transferring Inventory

A Transfer moves inventory from one location to another, created from
Inventory → Transfer Inventory → New Transfer. Set the header (including the
from and to locations) and the item lines being moved; the transfer's detail
page shows both locations in its title, so you can see the move direction
at a glance without opening the line detail.

## Inventory Counts

An Inventory Count is a physical count of what's in a location, created from
Inventory → Inventory Count → New Count by choosing its scope. A count starts
without lines — use Freeze & Start Counting on the count to lock in the
system's current on-hand quantities and generate the lines to count against.
As counting proceeds, the count's Progress section shows how many variances
have turned up and the net variance so far.

## Inventory Setup

Administrators with access to Configure → Inventory Setup manage the shared
vocabulary used across inventory records: Materials, Colors, Finishes,
Reasons (used on adjustments), Units, and Tax Rates. These are the option
lists that show up in dropdowns elsewhere in Inventory — for example, the
Reasons list here is what populates the reason field when someone creates
an adjustment.

## Default location

One location can be marked as the default with the Set as Default action on
Configuration → Company Info → Locations. Records that need a location —
like an item receipt, an adjustment, a transfer's From location, a count or
a new bundle — start on the default location so you don't have to pick one
every time, though you can always choose another location on a particular
record if it belongs somewhere else. If the company has only one location,
that is the one offered.
