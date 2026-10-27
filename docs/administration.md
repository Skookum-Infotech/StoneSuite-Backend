# Administration

StoneSuite's administrative settings live under Configure in the sidebar:
users, roles and permissions, workflows and custom fields, record numbering,
company profile, single sign-on, approval chains, the audit log, and
dashboard widgets. Most of these require an administrator role to see or
change.

## Users and invites

Configure → Users lists your workspace's team members and any pending
invitations, on separate Members and Invites tabs. Use Invite member to send
a new teammate an invitation by email — you can also give them a full name
and an initial role right away. From the Invites tab you can Resend an
invitation that's pending or has expired. From a user's own detail view you
can manage which roles they hold, and use Deactivate account to remove their
access. A deactivated person also stops counting as an approver and stops
receiving approval emails (see Approval chains).

## Roles and access

Configure → Roles & Access is where you control what each role can see and
do. Use New Role to create one, then build its permission matrix: for each
resource (leads, quotes, invoices, and so on), choose which actions the role
can perform and the scope for reading records — All or Own. All means the
role can see every record of that type; Own means the role can only see
records it owns itself. Two system roles — Super Admin and Guest — are
locked and can't be edited or deleted, shown with a lock icon.

## Workflows and custom fields

Configure → Workflows lists every record type (lead, quote, invoice, and so
on) and whether it's enabled. Open one to configure it: turn its custom
fields section on or off and add up to 15 custom fields, each with its own
type and whether it's required. This is also where you configure approvers
(see below).

## Approval chains

On a workflow's configuration page, the Approval Chain section lists the
status a record must clear before moving forward (most workflows have one;
Fabrication Job has two — Templating and QC Pending). Turn the switch on for
a gate and pick up to a set number of approvers from your employees; every
approver you add must sign off before a record can leave that status. Turn
the switch off to remove the approval requirement entirely.

Only active people count as approvers. If an approver is deactivated, or their
employee record is deleted, they no longer count toward the sign-offs a record
needs and they stop receiving approval emails. The approvers who are still
active are enough to approve: the record needs sign-off from each of them, not
from the deactivated one. They still appear in the approver list so you can
remove them.

If every approver for a status has been deactivated, the record stays blocked
rather than skipping approval. A super admin can approve it in the meantime,
and all active super admins are emailed that it is waiting. To fix it for good,
add a new approver on the workflow's Approval Chain section.

## Record numbering

Configure → Record Numbering controls the auto-generated ID for each
workflow's records (for example, LEAD-0001). For each workflow you can turn
auto-numbering on or off and set its prefix, suffix, and number of digits;
the next number and a live preview are shown alongside so you can see
exactly what the next record's number will look like before you save your
changes.

## Company profile

Configure → Company Info holds your organization's own details, on Company
Profile and Locations tabs. The Company Profile tab covers your company
name, legal name, industry, website, country, currency, timezone, tax ID,
and main address. The Locations tab lets you add and manage additional
addresses beyond that default one. Use the edit action on either tab to make
changes, then save.

## SSO / SAML setup

Configure → SAML Setup is where an administrator connects a company identity
provider so staff can sign in with single sign-on. StoneSuite supports
Microsoft Entra ID, Amazon Cognito, and other SAML-based providers through a
custom setup flow. Each flow walks you through registering StoneSuite as an
application with your identity provider (using the Identifier / Entity ID and
Reply / ACS URL StoneSuite gives you), then pasting your provider's metadata
URL back into StoneSuite to finish the connection.

## Audit log

Configure → Audit Log lists security and record-change events across your
workspace, with filters so you can narrow down what you're looking for —
useful for reviewing who did what and when. This is the same audit trail
referenced elsewhere, such as the history behind an accounting period close;
it gives administrators one place to look up who made a change, what
changed, and when it happened, across every module.

## Anomalies

Anomalies in the sidebar lists things that look wrong, sent to the people who
can do something about them. StoneSuite checks your workspace regularly and,
when it finds one, adds it to this page and shows an alert in the bell at the
top of the screen. Serious ones are also emailed. It checks these areas:

- Sales and money in: unusually high discounts, items sold below cost, orders
  far larger than a customer's usual, possible duplicate invoices, customers
  over their credit limit, invoices well past due, payments never applied,
  refunds larger than the payment or issued right after it, several credit
  memos for one customer, documents voided after being issued, dates far from
  when a record was entered, an order whose total differs a lot from its
  quote, invoices with no sales tax for a customer who normally pays it, and
  filled orders that were never invoiced.
- Purchasing and money out: bills adding up to more than their purchase order,
  a bill paid more than it is worth, large payments to a brand-new vendor,
  purchase prices well above the usual cost, one vendor's orders split across
  a day, late or stalled purchase orders, repeated expenses, and unusually
  high expense lines. It also flags a vendor bill that looks like a copy of an
  earlier one (same vendor, vendor invoice number and total).
- Inventory: more stock reserved than is on hand, a stock balance that does not
  match its movements, large write-offs, many adjustments in a short time,
  large stock count differences, stock reserved for a cancelled order, slabs
  that have not moved for months, bin transfers waiting too long, a received
  slab count that differs from the order, and a spike in stock loss at one
  location.
- Fabrication: jobs that have not moved for a while, jobs past their promised
  install date, late scheduled activities, a template redone many times, and
  more than one job on an order.
- Ledger and finance: a journal entry whose debits and credits don't match,
  entries posted into a closed period, unusually large cash transfers, and
  changes to the chart of accounts. Separately, it flags a record approved by
  its own owner.

Each anomaly goes to the people who can act on it: usually the record's owner
and the module's approvers, and administrators for the more serious or
sensitive ones. You only see the anomalies sent to you, and Super Admins see
all of them. The person whose action triggered an anomaly is never alerted
about it. Low-importance ones, such as a chart of accounts change, appear on
this page only, with no bell alert or email.

Open an anomaly and choose what you decided: Mark reviewed, Confirm problem,
or Dismiss, optionally with a note. Dismissed anomalies stay dismissed, and you
can Reopen any of them later. Use the tabs at the top to switch between Open,
Confirmed, Reviewed, Dismissed and All. Anomaly checks run only while the
platform's automation scheduler is turned on. A workspace admin can also pause
it for just this workspace with the Anomaly scanning switch at the top of the page
(it needs the Automation permissions). While it is off nothing is checked and no
alerts are sent; switching it back on resumes at the next check and may raise
anything from the last month that was missed.

## Dashboard widgets

Configure → Dashboard Widgets controls which widgets each role can see on
their Dashboard. The allocation matrix lets you turn a widget on or off for
one role, or for every role at once. A user can only turn on a widget their
role has been allocated — they can't add one you haven't given them access
to.
