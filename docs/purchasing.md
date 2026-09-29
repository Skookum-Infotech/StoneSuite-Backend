# Purchasing

StoneSuite's purchasing documents mirror the sales side: a Requisition can
become a Purchase Order, a received Purchase Order can become a Vendor Bill,
and Vendor Payments and Vendor Credits settle what you owe a vendor. Each
document type lives under the Purchases section of the sidebar.

## Vendors

A Vendor is a supplier you buy materials or services from, created from
Purchases → Vendors → New Vendor. A vendor's status is Active, On Hold, or
Inactive — only an Active vendor can be picked when creating a requisition,
purchase order, or other purchasing document, so put a vendor On Hold or
make them Inactive when you want to stop new purchasing activity with them
without deleting their record.

## Requisitions

A Requisition is an internal request to purchase something, created from
Purchases → Requisitions → New Requisition. Its statuses are Draft, Pending
Approval, Approved, or Cancelled. Once Approved, it can be converted into a
Purchase Order with the Convert to Purchase Order button — a requisition can
only be converted once; converting again is not offered, since it's already
linked to the purchase order it produced.

## Purchase Orders

A Purchase Order is your order to a vendor, created from Purchases →
Purchase Orders → New Purchase Order (or by converting a requisition). Its
statuses are Draft, Pending Approval, Approved, Sent, Partially Received,
Received, Closed, or Cancelled. Once items start arriving you can use the
Receive Items action to record them (creating an Item Receipt), and once the
purchase order is Received or Closed you can use the Convert to Bill action
to create a Vendor Bill from it.

Each line picks an item from Inventory, and its Unit column shows what the
quantity is measured in — Each for a counted item, Sq ft or Sq m for stone sold
by area. Stone is priced per square foot (or square metre), so a slab item's
quantity is the total area you are buying. Because stone is usually ordered by
the bundle, a slab line also offers a slab estimate: enter how many slabs you
expect and their average size, and the quantity fills in for you (you can still
type it directly). The expected slab count is kept on the line so that
receiving can show, for example, "11 of about 12 slabs" — it is only a count to
check the delivery against, and the exact area is measured when the slabs
arrive.

## Item Receipts

An Item Receipt records goods physically received against a purchase order,
created from the Receive Items action on the purchase order (or from
Purchases → Item Receipts → New Item Receipt). Its statuses are Pending,
Partial, Received, or Void. A purchase order can be received in more than
one shipment, which is why Partial exists alongside Received — a purchase
order's own status reflects whether it's been fully or only partly
received.

Slab items are received slab by slab. For a purchase order line whose item is
tracked as individual slabs, the Item Receipt asks for a receiving warehouse
and one row per physical slab — its length, width, and thickness in
millimetres, and optionally the bin it goes to. The received quantity for that
line is worked out from those slabs' areas, so you don't type it. Each slab is
given a serial number made from the purchase order number plus a running
number, shown on the form before you save. Saving the receipt adds those slabs
to Inventory; voiding the receipt takes them back out, as long as none of them
has since been used or moved.

To save typing a whole bundle by hand, use Paste from packing list above a
line's slabs: paste the length, width, and (optionally) thickness columns from
your supplier's packing list or spreadsheet, one slab per row, choose whether
the sizes are in millimetres, centimetres, or inches, and the slabs are added
for you. A trailing lot or bundle label on a row is picked up too. The preview
shows what was read, and any rows that could not be read are listed and
skipped. Set lot / block for all fills the same lot or block ID onto every slab
on the line in one step.

## Vendor Bills

A Vendor Bill is the vendor's invoice to you, created from Purchases →
Vendor Bills → New Vendor Bill (or by converting a received purchase order).
Its statuses are Draft, Pending Approval, Approved, Partially Paid, Overdue,
Paid, or Void — the same progression an Invoice you send to your own
customers follows, just on the vendor side. Vendor Payments and Vendor
Credits are applied against a bill to move it toward Paid.

## Vendor Payments

A Vendor Payment records money you've paid to a vendor, created from
Purchases → Vendor Payments → New Vendor Payment. When creating one, you can
optionally apply it to the vendor's open bills so those bills move toward
Paid. Its statuses are Draft, Pending Approval, Approved, Scheduled, Sent, or
Void — Scheduled covers a payment that's been approved but is set to go out
on a future date.

## Vendor Credits

A Vendor Credit records a credit a vendor has issued you — for a return or
an adjustment — created from Purchases → Vendor Credits → New Vendor Credit.
Its statuses are Draft, Approved, Applied, or Void; Applied means the credit
has been used against a vendor bill, the same way a Credit Memo is applied
against a customer invoice.

## Expenses

An Expense records a business cost — created from Purchases → Expenses → New
Expense — and tracked separately from your Vendor Bills. It's the right
place for a cost that isn't a bill from a vendor you set up in StoneSuite,
like a reimbursable purchase an employee made themselves. See
`accounting.md` for expense statuses and approval.

## Approvals

Requisitions, purchase orders, vendor bills, and vendor payments can each
have approvers configured on their workflow (Configure → Workflows). When
approvers are set, a record waits in Pending Approval until every configured
approver signs off before it can move forward — for example, a requisition
can't be converted into a purchase order until it clears its own approval,
since only an Approved requisition offers the Convert to Purchase Order
button. See `administration.md` for how approval chains are set up.

## Emailing and exporting purchasing documents

Vendor Bills, Vendor Credits, and Vendor Payments each have a Send action in
their Quick Actions panel to email the document as a PDF to the vendor.
A Purchase Order is emailed with the Send to Vendor button at the top of the
page: it marks the order Sent and emails the PDF to the vendor in one step
(using the vendor's contact email), and is refused if the vendor has no email
on file. There is no separate email action for it in Quick Actions.
Every purchasing document type — Vendors, Requisitions, Purchase Orders,
Item Receipts, Vendor Bills, Vendor Payments, Vendor Credits, and Expenses —
has an Export PDF action to download a copy.
