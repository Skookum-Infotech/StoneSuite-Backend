# Accounting

StoneSuite's Finance section covers your chart of accounts, moving money
between accounts, closing accounting periods, and tracking business
expenses. Find it under the Finance section of the sidebar, with Expenses
under Purchases.

## Chart of Accounts

The Chart of Accounts (Finance → Chart of Accounts) is the tenant's fixed
category tree of postable accounts — the categories themselves (Assets,
Liabilities, and so on) are fixed, but you add the actual accounts within
them. View it as a Tree (grouped by category) or a Table (a flat, filterable
list) using the toggle at the top of the page.

## Adding an account

From the Chart of Accounts tree view, use the add-account action on a
category (or the add-sub-account action on an existing account) to create a
new postable account or sub-account within that category. Newly created
accounts appear in the tree right away, under the category or parent
account you added them to, ready to be picked wherever the app asks for an
account.

## Viewing accounts

Use the Tree view when you want to see accounts grouped by category the way
your books are organized. Switch to the Table view for a flat, filterable
list of every account — handy when you know roughly what you're looking for
and just want to search or sort.

## Journal Entries

Journal Entries (Finance → Journal Entries) record a transfer of funds
between two of your accounts — a cash transfer. Create one from New Journal
Entry by choosing a From Account and a To Account (both Bank/Cash accounts).
Its statuses are Draft, Approved, Posted, Cancelled, or Reversed.

## Accounting Periods

Accounting Periods (Finance → Accounting Periods) is your fiscal calendar,
broken down into years, quarters, and months, each of which can be opened or
closed. The first time you visit, you'll set up the calendar; after that, use
Generate Fiscal Year to add each new year, and open or close individual
periods as your books are finalized. Every close is tracked in an audit
trail.

## Default Accounts

Default Accounts (Finance → Default Accounts) is where you tell StoneSuite
which account to use automatically for each purpose — for example, which
account a payment deposits into by default. Set the default account for each
listed slot from your Chart of Accounts, so day-to-day transactions post to
the right place without anyone having to choose an account by hand every
time.

## Expenses

An Expense records a business cost, created from Purchases → Expenses → New
Expense. It moves through Draft, Submitted, Approved, or Rejected, and
finally Reimbursed once paid back. A rejected expense shows the rejection
reason. If approvers are configured for expenses, a submitted expense waits
for their sign-off before it can be approved.

## Approvals in Finance

Journal Entries and Expenses can each have approvers configured on their
workflow (Configure → Workflows), the same way sales and purchasing
documents do. When approvers are set, the record waits for every configured
approver to sign off before it can move forward — a Journal Entry from Draft
to Approved, and an Expense from Submitted to Approved.
