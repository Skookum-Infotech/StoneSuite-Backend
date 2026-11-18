package fabrication

// slabFreeForJob reports whether a job on salesOrderID may allocate a slab with
// this status and sales-order hold: a free slab, or one the job's own order
// already holds from its Material drawing. A slab held for a customer, another
// order or another job is not free.
//
// The caller clears reserved_sales_order_id when it allocates the slab, which
// hands the hold over to the job's own allocation row.
func slabFreeForJob(status string, heldForOrder *int, salesOrderID int) bool {
	if status == "available" {
		return true
	}
	return status == "reserved" && heldForOrder != nil && *heldForOrder == salesOrderID
}
