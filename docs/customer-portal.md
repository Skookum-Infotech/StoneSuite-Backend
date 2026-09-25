# Customer Portal

The customer portal is a separate, limited sign-in StoneSuite offers your
customers so they can log in themselves — outside of the staff app you use
day to day. A portal login is tied to one specific customer record.

## Granting a customer portal access

Open the customer's record (CRM → Customers) and find the Portal Access card.
Use the Grant Access action to give that customer a portal login — you'll
need the customer to be Active and to have a contact email on file before you
can grant access. The portal login is always the customer record's own
contact email, so there's nothing to type: confirm on the Grant Portal
Access dialog and StoneSuite emails that address an invitation to set up
their sign-in. If the invitation should go to a different address, update
the customer's contact email first.

## Managing a customer's portal logins

Each login under a customer's Portal Access card can be managed individually
with row actions: Resend (send the invitation again, if it's still pending or
has expired), Suspend, Resume, or Revoke (permanently remove that login).
Suspend and Resume are reversible — use them when you want to temporarily
block a login without losing it. Revoke is permanent: the customer would
need a brand-new invitation to get back in.

## Customer Portal Users

Configure → Customer Portal Users gives you a tenant-wide view of every
external login into your workspace, across all customers — search by email,
name, or customer, and filter by status (Active, Suspended, Revoked). You can
suspend or resume a login right from this list; granting a new login,
resending an invitation, or permanently revoking one is still done from the
customer's own record. You can also download the list as a CSV.

## What a customer sees in the portal

A customer signed into the portal only sees their own data, and only a
limited part of the app: their Sales Orders, Invoices, Payments, and
Refunds, plus their own Account Settings and the Support page to file or
follow up on a ticket. They don't see the rest of StoneSuite — the CRM,
purchasing, inventory, accounting, or configuration areas are all staff-only.

## Multiple workspaces

If the same person has portal access to more than one of your customers (or
to customers across different StoneSuite tenants), they can switch between
those workspaces from their profile menu after signing in, instead of
signing in separately for each one. Their profile menu lists every
workspace they can reach and marks which one is currently active, with a
single click to switch.
