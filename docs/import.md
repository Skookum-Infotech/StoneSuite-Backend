# Importing Data

StoneSuite lets you bring records in from a file instead of entering them one
at a time. Find it at Configure → Import Data.

## Supported file types

You can upload a CSV, XLSX, DOCX, or PDF file. The file is staged as
candidate records for you to review before anything is actually created —
nothing is added to StoneSuite until you commit the import, so uploading a
file to see how it stages is always safe to try.

## Choosing what to import into

Use the "Import into" dropdown to pick which record type you're importing —
leads, customers, quotes, or any other workflow — but you'll only see the
ones you have permission to create records in. Pick your file, then upload
it to start staging.

## Staging

After you upload, StoneSuite parses your file and stages each row as a
candidate record without creating anything yet. This runs in the
background; you'll see a progress indicator counting up as rows are staged,
so you can tell it's working on a larger file. If staging fails, you'll see
the error and can try again with a different file using the Try Again
action.

## Reviewing and mapping columns

Once staging finishes, you land on the review step. Each column from your
file is listed with a dropdown to choose what it maps to: Ignore, a core
field, or one of the workflow's custom fields. Choosing "Core field…" lets
you type the field's key (suggestions are offered as you type). Apply your
mapping to update how every staged row will be interpreted.

## Fixing rows

Each staged row shows its status — Pending, Committed, Skipped, or Failed —
and any errors found on it. If a row has an error you don't want to fix, use
its Skip action to leave it out of the import; the rest of the rows are
unaffected. To fix an error, adjust the column mapping and reapply it, or
correct the source file and re-upload.

## Committing the import

When you're ready, use the Commit action to create records from every row
that's still pending (not skipped). When it finishes, you'll see a summary
of how many rows were committed, skipped, and failed, so you know at a
glance whether anything needs another look. From there you can start
another import with Import Another File.

## Who can import what

You can only import into a record type you have permission to create
records in — the "Import into" dropdown only lists the workflows you're
allowed to use. If you don't have create access to anything importable, the
page tells you to ask an administrator for access instead of showing an
empty import form.

## Custom field mapping

If the record type you're importing into has custom fields configured, the
column mapping step offers them as mapping targets alongside the core
fields, so a column from your file can be mapped straight to a custom field
without any extra setup — you don't need to create the fields separately
first, since they're already defined on the workflow.

## Ignoring a column

Not every column in your file needs to become a field on the record. Set a
column's mapping to Ignore and it's left out of the import entirely, while
the rest of your mapped columns are still applied normally — useful for
dropping an internal reference column, a blank column, or anything else
your file has that StoneSuite doesn't need.

## After the import

Once an import is committed, the new records exist just like any other
record you created by hand — they show up in their workflow's list, and you
can open, edit, or search for them the same way. There's no separate "import
batch" to manage afterward; the records are simply part of your data from
then on.
