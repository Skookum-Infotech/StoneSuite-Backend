//go:build dbtest

package docextractjob

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedVendor inserts a live Organization vendor with the lkp_record_status
// code (ACT_ active, INA_ inactive, ONHD on hold).
func seedVendor(t *testing.T, pool *pgxpool.Pool, legalName, statusCode string) string {
	t.Helper()
	var u string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name)
		SELECT rt.record_type_id, rs.record_status_id, 'Organization', $1
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = $2
		WHERE rt.record_type_code = 'VNDR'
		RETURNING vendor_uuid::text`, legalName, statusCode).Scan(&u)
	require.NoError(t, err, "seed vendor %q", legalName)
	return u
}

// seedPurchaseOrder inserts a live purchase order and returns its uuid and id.
func seedPurchaseOrder(t *testing.T, pool *pgxpool.Pool, vendorUUID, number, reference, statusCode string, totalCents int64) (uuid string, id int) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		INSERT INTO purchase_order (record_type, purchase_order_status, purchase_order_vendor_id, purchase_order_number,
		                            purchase_order_reference_number, purchase_order_subtotal, purchase_order_grand_total)
		SELECT rt.record_type_id, rs.record_status_id, v.vendor_id, $2, $3, $4::numeric / 100, $4::numeric / 100
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = $5
		JOIN vendor v ON v.vendor_uuid = $1::uuid
		WHERE rt.record_type_code = 'PORD'
		RETURNING purchase_order_uuid::text, purchase_order_id`, vendorUUID, number, reference, totalCents, statusCode).Scan(&uuid, &id)
	require.NoError(t, err, "seed purchase order %q", number)
	return uuid, id
}

// seedPOLine inserts a purchase order line (qty / received in whole units, price in cents).
func seedPOLine(t *testing.T, pool *pgxpool.Pool, poID, lineNo int, desc string, qty, received int, priceCents int64) string {
	t.Helper()
	var u string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO purchase_order_item (purchase_order_id, line_number, description, quantity, qty_received, unit_price)
		VALUES ($1, $2, $3, $4, $5, $6::numeric / 100)
		RETURNING purchase_order_item_uuid::text`, poID, lineNo, desc, qty, received, priceCents).Scan(&u)
	require.NoError(t, err, "seed purchase order line %d", lineNo)
	return u
}

// seedVendorBill inserts a live vendor bill and returns its uuid and id.
func seedVendorBill(t *testing.T, pool *pgxpool.Pool, vendorUUID, number, invoiceNumber, statusCode string, totalCents int64) (uuid string, id int) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_number,
		                         vendor_bill_vendor_invoice_number, vendor_bill_subtotal, vendor_bill_grand_total)
		SELECT rt.record_type_id, rs.record_status_id, v.vendor_id, $2, $3, $4::numeric / 100, $4::numeric / 100
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = $5
		JOIN vendor v ON v.vendor_uuid = $1::uuid
		WHERE rt.record_type_code = 'VBIL'
		RETURNING vendor_bill_uuid::text, vendor_bill_id`, vendorUUID, number, invoiceNumber, totalCents, statusCode).Scan(&uuid, &id)
	require.NoError(t, err, "seed vendor bill %q", number)
	return uuid, id
}

// seedBillConversion records that billID billed qty units of the PO line poItemUUID.
func seedBillConversion(t *testing.T, pool *pgxpool.Pool, poID, billID int, poItemUUID string, qty int) {
	t.Helper()
	var convID int
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO vendor_bill_conversion (purchase_order_id, vendor_bill_id) VALUES ($1, $2)
		RETURNING vendor_bill_conversion_id`, poID, billID).Scan(&convID))
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `
		WITH ins AS (
			INSERT INTO vendor_bill_conversion_line (vendor_bill_conversion_id, purchase_order_item_id, quantity)
			SELECT $1, purchase_order_item_id, $3 FROM purchase_order_item WHERE purchase_order_item_uuid = $2::uuid
			RETURNING 1)
		SELECT COUNT(*) FROM ins`, convID, poItemUUID, qty).Scan(&n))
	require.Equal(t, 1, n)
}
