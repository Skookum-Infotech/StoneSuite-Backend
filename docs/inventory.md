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

An item has two prices, and both are required: a Purchasing Unit Price, what you
pay for it, and a Selling Unit Price, what you charge. A purchase order, vendor
bill or requisition line fills in the purchasing price when you pick the item,
and a quote, estimate, sales order or invoice line fills in the selling price.
Either way the price on the line is yours to change; the item's own price is only
the starting point. A price of 0 is allowed, for a delivery or labour item.

An item's Track stock setting decides whether a sales order for it is checked
against stock and holds it. It is on for every item unless you switch it off,
which you should for anything with no stock to run out of — a delivery,
installation or labour charge. A slab-tracked item is always tracked, and its
switch cannot be turned off.

## Kits

A Kit/Package is a named group of items that are sold together — "Kitchen", for
example. Create one from Inventory → Items → Kit/Packages → New Kit/Package: give
it a name and add the items it holds, each with the quantity a line gets by
default. An item can be in several kits. An item's own form has a Kit/Packages
field too, so you can file an item into kits as you create it. Kit names are
unique, whatever their case or spacing; a deleted kit's name can be used again.
A kit that is switched Inactive no longer shows up in the item pickers.

On any document line, type a kit's name in the item box and pick it: the kit's
items are added as ordinary lines, each with its own price, tax and quantity
that you can still change. On a Quote, Estimate, Sales Order or Invoice the
lines sit under a kit heading — a full-width violet row naming the kit — which
is kept when the document is saved, converted, exported to PDF or sent to the
customer. The kit is only a heading: the lines are priced, taxed and checked
against stock exactly like any other, and later editing or deleting the kit
never changes a document it was used on. If the name you type is not a kit,
the picker offers Add as a new kit.

### Kits and items added by customer portal orders

When a customer places an order from the customer portal, every area they list
is matched to a Kit/Package of the same name — and created if you do not have
one yet — and everything they enter is matched to an item of the same name, or
added as a new item. Items added this way have a SKU that starts with PORTAL-,
a unit of Each, a price of zero, and no stock checking, and they are filed into
the area's kit. Find them in Inventory → Items by searching PORTAL-, then set
the real SKU, unit, prices and stock tracking, or merge them into the item you
already sell. Existing kits and items are reused as they are, and an item that
already exists keeps its own price.

## Units and Slabs

A Unit is an individual physical slab or piece tracked on its own. Every unit
is listed under Inventory → Inventory in the sidebar. You don't add units
there: a slab appears in inventory as **On Order** when the purchase order for
it is sent, with its serial number, and becomes available stock when it is
received against that order — see Item Receipts in the Purchasing guide. A slab
on order is not counted as on hand and cannot be moved, cut, bundled, counted or
transferred; it is written off if its purchase order is cancelled or closed
first. Its detail page records the item it belongs to,
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

Click a bin's name to open its page. It shows what is in the bin right now
(Contents), and a Movement history of every slab that has been moved in,
moved out, cut, or scrapped from it — including who did it, when, and which
Bin Transfer it belonged to.

## Moving Slabs Between Bins

A Bin Transfer is how slabs are moved from one bin to another within a
location. Create one from Inventory → Bin Transfer → New Bin Transfer: choose
the location, add the slabs, and pick the bin each one is going to. You can
also start one from a slab's own page with Transfer to Bin.

A transfer goes through these steps:

- Draft — you are still putting it together, and it can be edited or deleted.
- Pending Approval — it was submitted and is waiting for the approvers
  configured under Configuration → Workflows → Bin Transfer. Each of them must
  sign off. An approver can instead Reject it with a reason, which sends it
  back to Draft.
- Approved — everyone has signed off. Nothing has moved yet.
- Completed — someone pressed Complete and the slabs now sit in their new
  bins. Completing moves them all together, or none at all, and says which
  slab got in the way.

If no approvers are configured for Bin Transfer, there is nothing to wait for:
a draft can be completed straight away. A transfer can be cancelled at any step
before it is completed. A slab can only be on one open transfer at a time.

Each completed move is recorded on the slab's own History tab and on the
Movement history of both bins, with the transfer's number.

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
