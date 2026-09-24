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

## Units and Slabs

A Unit (shown as Units / Slabs in the sidebar) is an individual physical
slab or piece tracked on its own, received with the Receive Slab action from
Inventory → Units / Slabs. Its detail page records the item it belongs to,
its location, dimensions, attributes, and its lineage (where it came from).

## Bundles

A Bundle groups related units together — for example, several slabs from the
same lot — so you can track and move them as one group instead of handling
each slab separately. Find and open bundles from Inventory → Bundles; each
bundle's detail page shows its bundle information and the inventory item
it's associated with.

## Bin Management

Bins are the specific storage locations within a warehouse — the shelf,
rack, or slot something actually sits in, one level more specific than the
warehouse itself. Manage them from Inventory → Bin Management, so your
units and items can be tracked down to exactly where they're stored.

## Warehouses

A Warehouse is a stocking location — a yard, shop, or facility where you
hold inventory. Manage them from Inventory → Warehouses, where you can add
new warehouses and mark one as the default with the Set as Default action,
shown with a star next to that warehouse's name.

## Adjusting Inventory

An Adjustment corrects the recorded quantity of an item — for damage, loss,
or a count correction — created from Inventory → Adjust Inventory → New
Adjustment. Give it a warehouse and header information, then add the item
lines being adjusted, each with the corrected quantity and a reason. Once
saved, the adjustment's own record shows the warehouse it applied to in its
title.

## Transferring Inventory

A Transfer moves inventory from one warehouse to another, created from
Inventory → Transfer Inventory → New Transfer. Set the header (including the
from and to warehouses) and the item lines being moved; the transfer's detail
page shows both warehouses in its title, so you can see the move direction
at a glance without opening the line detail.

## Inventory Counts

An Inventory Count is a physical count of what's in a warehouse, created from
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

## Default warehouse

Warehouses can have one marked as the default with the Set as Default
action on Inventory → Warehouses, shown with a star. Records that need a
warehouse — like an item receipt — default to this warehouse so you don't
have to pick one every time, though you can always change it on a
particular record if that one belongs somewhere else.
