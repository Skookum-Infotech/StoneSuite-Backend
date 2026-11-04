# Install Schedule

Install Schedule is where you book your crew's site visits for fabrication
jobs. Find it under Calendar → Install Schedule in the sidebar. Each visit is
one block of time on a job, with the crew members booked for it, so you can
see who is where across the week and catch double-bookings before they
happen.

## Install Schedule and the Activity Planner

Calendar → Activity Planner and Install Schedule show different things, so it
helps to know which one to use:

- The Activity Planner's Installation cards show the install date that is
  planned for a sales order. They do not hold crew, so they cannot tell you
  whether a crew member is already booked.
- Install Schedule holds the crew bookings: which people are going to a job
  site, when, and for what kind of visit.

Booking a visit on Install Schedule does not move the dates on the fabrication
job or the sales order, and it does not change the job's Install Crew Lead.
The two calendars are separate records.

## Kinds of visit

Every visit has one of three types:

- **Template** — the templater measuring the job site.
- **Install** — the crew installing the stone.
- **Service** — a return visit to fix or adjust finished work.

A fabrication job can have as many visits as it needs, of any type. A
template visit and an install visit on the same job are two separate
bookings.

## Booking a visit

To book a visit, choose the fabrication job, choose the visit type, and set
when it happens:

- Enter a start and an end time. Times move in 15-minute steps, and a visit
  can run for up to 14 days.
- Or mark the visit as all day. An all-day visit covers whole days, including
  the end day you choose.
- Pick the crew from your employees. You can name one crew lead, and the rest
  are members. A visit can have up to 10 crew members, and it can be saved
  with no crew at all.
- Add notes if the crew needs to know something, such as gate codes or
  access times. Notes can be up to 2,000 characters.

You need permission to book visits, and you also need permission to see the
fabrication job. If the job is outside what your role can see, booking it
reports that the job could not be found.

A visit cannot start more than one day before today, so you can't book or
move a visit back further than that. Completing a visit has no such limit and
can be done once its start time has passed. You also can't book a visit on,
or move the time of a visit on, a job that is already completed or cancelled.

If a booking is submitted twice, for example after a slow connection, it is
saved only once.

## Conflicts and overrides

Install Schedule checks every booking for two kinds of overlap:

- **Crew conflict:** a crew member on the visit is already booked at an
  overlapping time, on this job or another one.
- **Job conflict:** another visit on the same job overlaps the time, for
  example a template visit booked at the same time as an install visit.

Visits that only touch do not overlap. A visit that ends at 12:00 and another
that starts at 12:00 can both be booked for the same crew member.

Cancelled visits never count as conflicts. A completed visit counts only until
its end time has passed.

A conflict is a warning, not a block. When a booking overlaps another one,
StoneSuite lists the overlapping bookings and asks you to confirm. If you
confirm, the visit is saved, and the history of the visit records which
conflicts you overrode. The list shows up to 20 overlapping bookings; if there
are more, it says how many more there are.

Some overlapping bookings may belong to a job you are not allowed to see. For
those, the warning still shows the crew member's name and the times, but it
does not show the job, so you can see that someone is busy without seeing
the job itself.

The check runs again at the moment you save. If someone else books the same
crew member while you are looking at the warning, you will be asked to
confirm again.

## Changing a visit

You can change a visit after it is booked:

- **Reschedule** by changing its start time, end time, or all-day setting.
  The visit keeps its identity, and the change is recorded in its history.
  You can add a reason.
- **Reassign** by changing the crew. Added and removed crew members are
  notified separately (see below).
- **Edit** its notes or its visit type. These changes are recorded, but nobody
  is notified about them.

Only a scheduled visit can be changed. Once a visit is completed or cancelled,
it is final.

If you try to change a visit while someone else has changed it since you
opened it, StoneSuite does not save your change and asks you to reload the
visit. The other person's change is kept.

Changing the time of a visit on a job that has since been completed or
cancelled is refused. You can still change its crew and notes.

## Cancelling a visit

To cancel a visit, give a reason of up to 500 characters. The reason is
required. A cancelled visit is kept in the history and is hidden from the calendar by
default. A cancelled visit cannot be reopened. To put the work back on the calendar, book a new visit.

Cancelling a visit notifies the crew on it.

StoneSuite does not cancel a job's visits for you. If a job is cancelled or
completed, its scheduled visits stay on the calendar until you cancel them
yourself. Those visits are flagged "Job cancelled" when the job is cancelled,
so you can spot them and cancel each one by hand.

## Completing a visit

Mark a visit complete once it has started. StoneSuite will not complete a
visit before its start time. Completing a visit does not notify anyone.

## Who gets notified

Crew members are notified by email when their bookings change:

- **Booked:** every crew member on a new visit is told about it.
- **Rescheduled:** every crew member, both the people who were on the visit
  before the change and the people on it after, is told the new time. Anyone
  added or removed in the same change gets the message below instead.
- **Crew added:** a newly added crew member is told they've been booked.
- **Crew removed:** a removed crew member is told they've been taken off the
  visit.
- **Cancelled:** every crew member on the visit is told it was cancelled.
- **Completed, or notes or visit type edited:** nobody is notified.

You are never notified about a change you made yourself.

Notifications are sent only to crew members who have a StoneSuite login. A
crew member without a login is skipped, and the booking still saves. After
saving, StoneSuite tells you whether any notification was sent.

Each notification shows the day, the time, and the time zone abbreviation,
for example "Tue 10 Nov, 8:00–12:00 CST".

## Time zones

Install Schedule works in your company's time zone, which is set on the
Company Profile tab under Configure → Company Info. The times you enter and
see on Install Schedule are in that zone.

Each visit remembers the time zone it was booked in. If you later change your
company's time zone, existing visits keep the times and the zone they were
booked with. Changing the company time zone does not move them.

Daylight saving time can cause two special cases:

- When clocks jump forward, a time in the skipped hour does not exist. If you
  enter one, StoneSuite refuses to save it.
- When clocks fall back, an hour happens twice. A time in that hour is booked
  at its first occurrence.

All-day visits always cover whole local days, including on daylight saving
changeover days.

## Permissions

Install Schedule is off for every role until an administrator grants it. In
Configure → Roles & Access, choose the role, then give it the install
appointment permissions: read to see visits, create to book them, update to reschedule,
reassign or edit them, and transition to cancel and complete them. Booking a
visit also needs permission to read fabrication jobs.

Permissions can be limited to your own visits. A user with own-only access
sees the visits on fabrication jobs they own, and the visits they are on as
crew. Everything else is hidden from them, and if they try to open a visit
that is hidden, StoneSuite reports that it could not be found.

## What the history shows

Every booking and change is recorded on the visit's history. Each entry shows
who made the change, when, what the visit's times were before and after, the
reason if one was given, and how many conflicts were overridden.

Visits are never deleted. Use cancel instead.

## What is not included yet

- Customers cannot see their visits through the customer portal.
- Install Schedule does not check your crew's working hours or time off.
  Conflicts are only about other visits.
