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
access.

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

## Dashboard widgets

Configure → Dashboard Widgets controls which widgets each role can see on
their Dashboard. The allocation matrix lets you turn a widget on or off for
one role, or for every role at once. A user can only turn on a widget their
role has been allocated — they can't add one you haven't given them access
to.
