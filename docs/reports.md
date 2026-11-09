# Reports

StoneSuite's Reports area answers common questions about your business in one
place: who buys the most, what is overdue, what is running low, and how much you
made. Find it under the Reports section of the sidebar. Each report has a date
range with quick choices such as This month, This quarter and Year to date, a
row of headline numbers, a chart, and a table. Every report can be saved as a
PDF with Export PDF or as a spreadsheet file with Download CSV.

## Who can see what

Reports are switched on per area in Roles & Access: CRM Reports, Sales Reports,
Purchases Reports, Inventory Reports, Calendar Reports, Finance Reports and
Custom Reports. A person only sees the reports for the areas their role allows.
Reports only count records the person is allowed to see, so two people can open
the same report and get different numbers if one of them is limited to their own
records. Inventory and Finance reports cover the whole workspace.

## The standard reports

Each area opens a page of report cards. Pick a card to open the report.

- **CRM:** Conversion Funnel, Lead Source Performance, and Stale Leads &
  Prospects. These need the relational CRM setup.
- **Sales:** Sales by Customer, Sales by Item, and Sales Trend. Choose whether
  to count orders or invoices. Revenue leaves out tax and shipping.
- **Purchases:** Purchases by Vendor, Purchases by Item, Open POs & Receiving,
  and AP Aging.
- **Inventory:** Stock on Hand & Valuation, Slab Availability, Low Stock, and
  Stock Movement.
- **Calendar:** Upcoming Installs, Overdue Activities, On-time Completion, and
  Order-to-Install Lead Time.
- **Finance:** Profit & Loss, with a list of the journal lines behind each
  account.

Many rows can be clicked to see the orders, invoices or records behind a number.

## Custom Reports

Custom Reports is for questions none of the standard reports answers. Open
Reports → Custom Reports. You can build a report yourself, or describe it in
plain English and let StoneSuite build a first version for you to check.

### Building a report

Choose New report, then pick what the report is about. You can report on
quotes, estimates, sales orders and their lines, invoices and their lines,
payments, leads, prospects, customers, vendors, purchase orders and their lines,
vendor bills, inventory items, stock by location, and slabs. You only see the
subjects your role allows.

Then shape the report on the left while the result updates on the right:

- **Columns:** the fields to show. You can use fields from the record itself and
  from the records it links to. For an invoice line that includes the invoice,
  the customer (for example their lead source) and the item (for example its
  material).
- **Filters:** narrow the rows, for example a date range, a status, a customer
  name that contains some text, or an amount above a number. Date filters can use
  quick choices such as This quarter, which stay relative every time you open
  the report.
- **Group by:** add up rows by one field, such as customer, item, or a date by
  day, week, month or quarter.
- **Totals:** for each group, a Sum, Count, Average, Minimum or Maximum.
- **Sort and top:** order the result and keep only the top rows, such as the top
  10 customers.
- **Chart:** show a bar chart of the first total for a grouped report, or no
  chart.

A report with no group shows a plain list of rows. A report only counts what you
filter for: unlike the standard reports it does not quietly leave out cancelled
or draft records, so choose the statuses you want. The preview shows the first
500 rows. Download CSV and Export PDF include up to 10,000 rows and tell you if
the report was cut short. The totals row always covers every matching row.

### Describing a report in plain English

Type what you want in the box at the top of Custom Reports, for example "top 10
customers by invoice revenue this quarter", "open purchase orders by vendor",
"leads created per month by source this year" or "stock on hand by
location", and choose Build it. StoneSuite opens the builder with a first
version already filled in, together with a short line saying what it
understood and anything it had to leave out. Always check the result before you
rely on it: the assistant can misread a request, and you can correct anything in
the builder. Inside the builder, the Describe a change box adjusts the report you
have open, for example "only paid invoices", and you can undo the change.

The assistant only chooses from the subjects and fields you are allowed to use,
and it never sees your records. If the assistant is switched off or starting up,
the box says so and you can still build the report by hand. The first request
after a quiet period can take up to a minute.

### Saving and sharing

Choose Save to keep a report. A saved report is private to you. Turn on Share
with workspace to let everyone in the workspace open it. Anyone who opens a
shared report sees only the rows their own role allows, which can differ from
what you see. Only the person who made a report can change, rename, share or
delete it. Everyone else can choose Save a copy to get their own private copy to
adapt. If your access to a subject is removed later, a report on that subject
stays in your list but cannot be run until the access is restored.
