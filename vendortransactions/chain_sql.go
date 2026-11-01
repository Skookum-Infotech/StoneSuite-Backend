package vendortransactions

import "time"

// argChainPO is the bind position of the purchase order's internal id in the
// count statement ($1 is the vendor).
const argChainPO = "$2"

// linkedBillIDs selects the live bills of the vendor tied to the purchase order
// $po: the bill's own FK or a conversion row. Static SQL; po is a placeholder.
func linkedBillIDs(po string) string {
	return "SELECT b.vendor_bill_id FROM vendor_bill b" +
		" WHERE b.vendor_bill_deleted_at IS NULL AND b.vendor_bill_vendor_id = " + argVendor +
		" AND (b.vendor_bill_purchase_order_id = " + po +
		" OR b.vendor_bill_id IN (SELECT c.vendor_bill_id FROM vendor_bill_conversion c" +
		" WHERE c.purchase_order_id = " + po + "))"
}

// linkedUUIDsSQL selects the uuids of every live, same-vendor document of the
// given type linked to the purchase order $po, whatever the caller may read. The
// payments are those with a live application against a linked bill.
func linkedUUIDsSQL(key, po string) string {
	switch key {
	case keyItemReceipt:
		return "SELECT r.item_receipt_uuid FROM item_receipt r" +
			" WHERE r.item_receipt_deleted_at IS NULL AND r.item_receipt_vendor_id = " + argVendor +
			" AND r.purchase_order_id = " + po
	case keyVendorBill:
		return "SELECT vb.vendor_bill_uuid FROM vendor_bill vb WHERE vb.vendor_bill_id IN (" + linkedBillIDs(po) + ")"
	default: // keyVendorPayment
		return "SELECT p.vendor_payment_uuid FROM vendor_payment p" +
			" WHERE p.vendor_payment_deleted_at IS NULL AND p.vendor_payment_vendor_id = " + argVendor +
			" AND p.vendor_payment_id IN (SELECT a.vendor_payment_id FROM vendor_payment_application a" +
			" WHERE a.application_deleted_at IS NULL AND a.vendor_bill_id IN (" + linkedBillIDs(po) + "))"
	}
}

// buildChainPOSQL selects the purchase order from the caller's readable branch
// (vendor, soft-delete and own-scope predicates included) by uuid.
func buildChainPOSQL(po granted, vendorID, employee int, poUUID string) (string, []any) {
	b := newBinder(vendorID, employee, time.Time{})
	branch := po.branchSQL(b)
	id := b.add(poUUID)
	sql := "SELECT x.id::text, x.number, x.status_name, x.status_code, p.purchase_order_id" +
		" FROM (" + branch + ") x" +
		" JOIN purchase_order p ON p.purchase_order_uuid = x.id" +
		" WHERE x.id = " + id + "::uuid"
	return sql, b.args
}

// buildChainItemsSQL selects the linked documents the caller may read, oldest
// first. The row set is the granted branch narrowed to the linked uuids, so the
// vendor, soft-delete and own-scope predicates always apply.
func buildChainItemsSQL(g granted, vendorID, employee, poID int) (string, []any) {
	b := newBinder(vendorID, employee, time.Time{})
	branch := g.branchSQL(b)
	po := b.add(poID)
	sql := "SELECT x.id::text, x.number, x.status_name, x.status_code, x.doc_date, x.amount, x.balance" +
		" FROM (" + branch + ") x" +
		" WHERE x.id IN (" + linkedUUIDsSQL(g.Key, po) + ")" +
		" ORDER BY x.doc_date, x.id"
	return sql, b.args
}

// buildChainCountSQL counts every live linked document of the type, readable or
// not; the caller subtracts the visible ones to get the hidden count.
func buildChainCountSQL(key string, vendorID, poID int) (string, []any) {
	return "SELECT COUNT(*) FROM (" + linkedUUIDsSQL(key, argChainPO) + ") l", []any{vendorID, poID}
}
