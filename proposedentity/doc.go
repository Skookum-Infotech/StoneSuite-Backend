// Package proposedentity holds the proposedaction executors that create a
// customer, vendor or inventory item from a document-extraction proposal. The
// package owns the payload contract (strict decode, validation, sanitizing,
// previews) and calls each module's existing transaction-aware create helper,
// so a confirmed proposal behaves like the same create made by a human.
package proposedentity
