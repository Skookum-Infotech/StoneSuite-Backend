# CRM Concepts

StoneSuite's CRM tracks a sales relationship through three stages, each with its
own pipeline of statuses. A record moves forward through these stages by
converting — it is never re-classified backward.

## Leads

A lead is an inbound contact that hasn't been qualified as a real sales
opportunity yet. The lead pipeline is: New → In Progress → Qualified →
Converted, with Unqualified and Dead as exit points at any stage once it's
clear the lead won't go further. A qualified lead converts into a prospect.

## Prospects

A prospect is a qualified lead now being actively worked as a sales
opportunity. The prospect pipeline is: In Discussion → Identified Decision
Makers → Qualified → Proposal → In Negotiation → Purchasing, with Closed Lost
as the exit point if the deal falls through at any stage. A prospect that
completes purchasing converts into a customer.

## Customers

A customer is a closed-won deal. The customer pipeline is: Closed Won →
Renewal, with Closed Lost as the exit point (e.g. churn). Customers can cycle
back into Renewal repeatedly as their relationship continues.

## Custom fields

Each of the three pipelines (lead, prospect, customer) can have up to 15
admin-defined custom fields in addition to the standard ones — things like
deal size, industry, or a renewal date. An admin names and types each field;
that name is what shows up when the AI assistant describes a record.

## What the AI assistant can do

The assistant answers questions about your own CRM records — leads, prospects,
and customers — using retrieval-augmented search: it only answers from records
it can actually find and that you have permission to see, and always says so
plainly ("I don't have that information") rather than guessing when it can't
find a good match.

It's well-suited to questions about a specific record or a small set of
similar ones — "what's the status of the Acme deal," "who's the owner of the
Initech prospect," "summarize the notes on this lead." It is **not** currently
able to compute counts, totals, or date-range filters across your whole
dataset — questions like "how many customers do we have" or "who closed in the
last week" aren't answerable by record search and will get the same "I don't
have that information" response, even though the records exist.
