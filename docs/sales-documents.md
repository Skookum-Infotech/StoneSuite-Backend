# Sales Documents

StoneSuite's sales documents form a chain: an Estimate can become a Quote, a
Quote can become a Sales Order, and a Sales Order can become an Invoice.
Payments, Credit Memos, and Refunds apply against a customer's invoices.
Each document type lives under the Sales section of the sidebar.

On an Estimate, Quote, Sales Order or Invoice, picking a kit in a line's item box
adds all of that kit's items as lines under a full-width kit heading (see Kits in
`inventory.md`). The heading carries through every conversion in the chain and
onto the exported and emailed PDF; the lines themselves are priced and taxed
like any other line, so totals are unaffected.

Lines measured in Sq ft or Sq m can also state the size of one slab — **Length**
and **Width** — and how many **Slabs** the line covers. Length and width are
always in the same unit, which comes from the inventory item (set on the item
as **Length / Width Unit**: mm, cm, in, ft or m; inches unless changed) and is kept
on the line, so changing an item's unit later never alters a saved document.
When length and width are both filled in, the line's quantity is no longer
typed: it is length × width × slabs converted to the item's area unit (rounded
to three decimals), so a 120 × 60 in slab is exactly 50 sq ft and 2 of them are
100 sq ft. Leave Slabs blank and one slab is assumed. Amount and Total are then
quantity × the per-area unit price, less discount, plus tax, exactly as for any
other line. Without a size, the line behaves as before. Size, unit and slab
count carry through every conversion and onto the exported and emailed PDF;
they are not available on lines measured in anything but an area unit.

## Estimates

An Estimate is a rough, early-stage price for a customer, created from
Sales → Estimates → New Estimate. It starts as Draft and moves through
Pending Approval, Approved, Sent, Rejected, Expired, or Cancelled. Only an
Active customer can be picked when creating one. An Approved or Sent estimate
can be converted into a Quote with the Convert to Quote button, which creates
a new quote from the estimate's details.

## Quotes

A Quote is a firmer, ready-to-send price, created from Sales → Quotes → New
Quote (or by converting an estimate). It uses the same statuses as an
estimate: Draft, Pending Approval, Approved, Sent, Rejected, Expired,
Cancelled. An Approved or Sent quote can be converted into a Sales Order with
the Convert to Sales Order button, which creates a new sales order from the
quote's details.

## Sales Orders

A Sales Order is the customer's confirmed order, created from Sales → Sales
Orders → New Sales Order (or by converting a quote). Its statuses are Draft,
Pending Approval, Approved, Open, Partially Filled, Filled, or Cancelled. A
sales order detail page also has Inventory and Scheduling tabs alongside its
Overview, Items, Audit, and Files tabs. Once Approved, Open, Partially Filled,
or Filled, it can be converted into an Invoice with the Convert to Invoice
button. A sales order is also the starting point for a fabrication job — see
`fabrication.md`.

Every sales order needs a Scheduled Installation Date (in Primary Information;
today or later). Saving it plans the order's production schedule, which the
Scheduling tab lists from Created through Installation: Template, Material
Allocation, Engineering, WIP Bin Transfer, Saw, Edges, Quality Control,
Installation Ready, Installation Picked and Installation. Each activity has a
status (Pending, In Progress or Completed), a scheduled date and time, a
completed-on time and notes. A Pending activity whose time has passed shows as
Overdue. The dates are spread evenly from the order date to the installation
date, with Installation on the installation date itself. Anyone who can edit the
order can set an activity's status, move its date or time, and add notes.
Changing the installation date re-plans every Pending activity; ones that are In
Progress or Completed stay where they are. An order made before this field
existed, or by converting a quote, has no schedule until a date is set on the
order.

Calendar → Activity Planner lays those schedules out across every order you can
see, one activity at a time. Pick Template, Engineering, Bin Transfer, Saw,
Edges, Quality Control, Installation or Invoicing, and each order appears as a
job card on the day and time it is planned for. Switch the columns between Day
(hours), Week and Month. A card links to its order's Scheduling tab; on the
Invoicing tab the cards are the invoices made from your orders, listed on
their due date, and link to the invoice. Cancelled orders are left out.

A sales order checks your stock when you save it. If any line asks for more of
an item than is free — what is on hand, less what other orders are already
holding — the order is not saved: a Not enough stock window lists each short
item with how much is needed, how much is available and how much is missing, and
offers to start a requisition for the shortfall so you can restock. Otherwise the
order holds what it needs from that moment, so no other order can promise the
same stone. Editing the order re-checks it (its own hold is not counted against
it), and cancelling or deleting it gives the stock back. Filling an order takes
its quantity-counted lines out of stock; stone is taken out as a fabrication job
cuts it. Lines with no catalog item (free text) and items marked as not tracking
stock — a delivery or installation charge, say — are never checked. The
Inventory tab shows, for each item on the order, what is on hand, what every
order holds, what this order holds, and what is still free.

### Creating a sales order from a customer's PO

When your workspace has it turned on, Sales → Sales Orders has an Upload Sales
Order button: pick a customer's purchase order (a PDF or Word file, up to
10 MB and 20 pages), or drag it onto the page. StoneSuite reads the document,
matches the customer and items to your records, and opens a New Sales Order
form already filled in. Nothing is saved until you review it and press Save.
Scanned images and password-protected files can't be read; enter those
manually. The button is greyed out when AI features are off for your
workspace.

While the document is read, a progress window shows Upload, Read, Match and
Review. You can choose Continue in background and you'll be notified when it is
ready. Documents waiting for review appear under Pending documents on the
Sales Orders list for 24 hours, after which they expire (any values you already
entered on the form are kept).

On the review screen the document sits beside the form. Each filled value is
marked as coming from the document or learned from an earlier correction, and
the info icon shows where on which page it was found. Fields that need a
decision are flagged Needs review; use the Needs review button (or Alt+Down and
Alt+Up) to jump between them. When you correct a customer or item, StoneSuite
remembers it for the next document from that customer.

If the customer or an item isn't in your records yet, the review screen shows
a "Create customer" or "Create item" suggestion. Nothing is created until you
choose it and press Confirm after checking the details from the document. A
new customer needs a contact email, which you type in; a new item needs a SKU
if the document shows none. The new record is then selected on the form. You
need permission to create customers or items to confirm one; without it the
suggestion is greyed out with the reason. Account or card numbers in a
document are hidden and never copied into a new record.

You can also upload a builder's fillable countertop order form (the PDF a
builder fills in with job details and one box per area, such as Kitchen,
Island or Bath 2). StoneSuite reads what was typed into the form:

- The install date becomes the expected delivery date, and the install address
  and city/zip become the ship-to address. Pick the state, because the form
  doesn't give one.
- The customer is suggested from the builder's email address (for example
  highlandhomes.com suggests Highland Homes). Always check this suggestion.
- The special instructions, builder contact, community, plan and measure date
  are added to the order's memo.
- Each filled area becomes a line, with its color as the item and its finish,
  edge, backsplash, cutouts, faucet spread and notes in the line description.
  A sink model becomes its own line under that area.

Order forms have no prices or PO number, so set the price on each line and
enter the builder's PO number when you have it.

If a sales order with the same customer and PO number already exists, you are
warned and can open the existing order or choose Create anyway. You are asked
only once per document.

### Orders placed from the customer portal

When a customer submits a countertop order from the customer portal, a Draft
Sales Order is created for that customer, owned by the customer's owner (or
their sales rep). Open it from Sales Orders and finish it:

- Each area the customer listed is a Kit/Package heading — Kitchen, Island,
  Bath 2 and so on — and each thing they filled in under it is a line: the item
  is what they entered (the color, the edge, the sink model) and the line's
  description says which field it came from (Color, Edge, Sink Model).
  Waterfall, pop-up outlet and half wall appear as their own lines when used,
  and the faucet spread reads like "8" left, center".
- Every line has a quantity of 1 and the item's own price, which is zero for
  items the customer's entries created. Set the real item, size, quantity and
  price on each line, and check the stock.
- The install address and city/ZIP are the ship-to address, the install date is
  the scheduled installation date, and the builder, community, plan, contact
  details, measure date and special instructions are in the memo. Pick the
  state, because the form doesn't give one.

The order is a Draft, so the customer does not see it until you approve it.

While the Sales Order is still a Draft the customer can change their order from
the portal, and the same Sales Order is updated, so check it again after they
do. What you have already done is kept:

- A line whose entry the customer did not change stays exactly as you left it,
  with your price, size and wording.
- If the customer changes an entry (a different color, say) the line's item is
  swapped for the new one in the same place, keeping your quantity, size and
  discount; its price becomes the new item's.
- A field or area they remove loses its lines, and one they add gets a new line
  of 1 under the area's kit heading.
- Lines you added yourself, or lines whose item you replaced with another, are
  not touched.
- The ship-to address, install date and memo follow the customer's changes only
  where you have not changed them. If you have added to the memo, your text is
  kept and the new job details are added below it under "Updated from the
  customer portal".

To see the form the customer filled in, open the Sales Order and choose
Customer Order Form in Quick Actions. It opens a large read-only window showing
the project details and every area exactly as the customer sees them in their
portal, including the area notes, which are not put on the Sales Order. The
action only appears on orders that were placed from the portal.

A super admin also gets an Edit button in that window, with the same fields the
customer uses. Nobody else can change the form from the Sales Order, whatever
their other permissions. While the Sales Order is a Draft the change is carried
onto its lines the same way as a customer's change (see above) and a heading
naming who made it is added to the memo if you have written in it. Once the
order is out of Draft only the customer's form record changes and the Sales
Order's lines are left as they are, so edit them directly. The customer sees
the corrected form in their portal either way.

Once you move the order out of Draft the customer can no longer change it from
the portal. If they rename a Bath or Custom area, that counts as removing the
old area and adding a new one, so redo any work on the old area's lines.
## Invoices

An Invoice is created from Sales → Invoices → New Invoice, or by converting a
sales order. Its statuses are Draft, Pending Approval, Approved, Sent,
Partially Paid, Overdue, Paid, or Void. Payments and credit memos are applied
against an invoice to move it toward Paid; an invoice that's past its due
date without being fully paid shows as Overdue. An invoice with payments or
credit memos applied to it can't be voided — unapply them first. Once an
invoice is Void it stays Void.

## Payments

A Payment records money received from a customer, created from Sales →
Payments → New Payment. When creating one, you can optionally apply it to one
or more of the customer's open invoices. Its statuses are Pending, Approved,
Deposited, or Void — Deposited means the money has been recorded as
received into your bank.

## Credit Memos

A Credit Memo records a credit issued to a customer — for a return, an
overcharge, or a goodwill adjustment — created from Sales → Credit Memos →
New Credit Memo. Its statuses are Draft, Approved, Applied, or Void; Applied
means the credit has been used against an invoice, the same way a Payment
is applied to reduce what a customer still owes.

## Refunds

A Refund records money paid back to a customer, created from Sales → Refunds
→ New Refund. You can optionally link it to related documents when creating
it, so the refund shows what it's connected to. Its statuses are Pending,
Approved, Sent, or Void.

## Approvals

Any of these document types can have approvers configured for its workflow
(Configure → Workflows). When approvers are set, a new or edited record waits
in a pending-approval status until every configured approver signs off before
it can move forward — for example, only an Approved (or Sent) quote offers
the Convert to Sales Order button, so approval is a checkpoint before a
document can move to the next stage. Deactivated people don't count as
approvers; if none of a status's approvers are still active, the record waits
for a super admin to approve it. See `administration.md` for how approval
chains are set up.

## Sending a document by email

On a Quote, Estimate, Sales Order, or Invoice, use the Send to Customer
action in the Quick Actions panel to email the document as a PDF to the
customer's email on file. A confirmation dialog shows who it will be sent to
before it goes out, so you can double-check the recipient before the email
leaves StoneSuite.

## Exporting a document as a PDF

Every sales document — Estimate, Quote, Sales Order, Invoice, Payment,
Credit Memo, and Refund — has an Export PDF action in its Quick Actions
panel, which downloads a PDF copy of the document for you to save, print, or
send yourself outside of StoneSuite's built-in Send to Customer action.

### Terms and notes on the PDF

An Estimate, Quote, Sales Order, or Invoice PDF prints the Terms & Conditions
and Notes saved on that document. If you left them blank, the PDF uses the
default wording your company set for that document type, and leaves the section
out when no default is set. StoneSuite does not add wording of its own.
