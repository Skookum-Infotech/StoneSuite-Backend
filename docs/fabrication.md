# Fabrication Jobs

StoneSuite calls the work of templating, cutting, and installing stone an
Installation / Fabrication job. Find it under Sales → Installation /
Fabrication in the sidebar.

## What a fabrication job is

A fabrication job tracks one job from order through installation: which
sales order it's for, the job site, the schedule, who's assigned, and its
progress through fabrication and installation. Every job starts from an
existing sales order — you can't create one from scratch. It's the record
that carries the order forward through the physical work — templating,
cutting, edging, and installing — after the paperwork side is settled.

## Creating a fabrication job

From Sales → Installation / Fabrication → New Fabrication Job, choose the
source sales order and the line items the job covers. The job is created
with the customer, job details, and line items carried over from that sales
order, so you don't have to re-enter what's already on the order — you're
mainly filling in the site, schedule, and staffing details that the sales
order doesn't have.

## Fabrication job statuses

A job moves through: Draft, Order Received, Material Allocated, Templating,
Template Approved, Fabrication Ready, Cutting, Edging, QC Pending, QC
Passed, Ready for Shipping, In Transit, Installing, and Completed. A job can
also be put On Hold or Cancelled. If approvers are configured, a job waits
for sign-off before it can leave the Templating status or the QC Pending
status.

## Job site, schedule, and assignment

A job's detail page has a Job Site section (site contact, address, and
phone — defaulted from the sales order's shipping address but editable) and
a Schedule section (Template Date, Fabrication Start, and Promised Install
Date). You can also assign a Job Owner, Templater, Fabricator, and Install
Crew Lead, each picked from your employees — the Job Owner defaults to
whoever created the job, and the others are left for you to assign as the
work is staffed.

## Pieces, slabs, and checklist

A job's detail page has tabs for Overview, Pieces (the individual pieces
being fabricated), Slabs (the specific slabs allocated to the job, if you
have access), Checklist, and Files. Use these tabs to track the physical
work as it happens, and add any supporting files or notes on the job. The
Slabs tab connects a fabrication job back to the actual inventory units it
consumes, which is why it's only shown to users with access to that
inventory data.

## Editing and exporting a fabrication job

Use the edit action on a job to update its site, schedule, or assignment
details, or to move it forward as work progresses. The job's Quick Actions
also include Export PDF, for a downloadable copy of the job you can print or
hand to a crew on-site instead of pulling up the job on a device.

## Approvals

If approvers are configured for the Templating gate or the QC Pending gate,
a job waits there until every configured approver signs off before it can
continue to the next status. These two gates match the two points in the
process where sign-off matters most — after the template is taken, and
after quality control — see `administration.md` for how approval chains are
set up.

## Finding a fabrication job

Sales → Installation / Fabrication lists every job — "Production jobs
tracking a stone order to install" — with columns for Job #, Customer,
Status, Promised Install date, and when it was created, so you can find and
check on a job without opening its source sales order first.
