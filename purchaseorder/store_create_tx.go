package purchaseorder

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

// DraftRef identifies a draft inserted in a caller-owned transaction.
type DraftRef struct {
	InternalID int
	ID         string
}

// CreateDraftTx inserts a validated draft without committing or notifying.
// Callers must authorize creation and commit their transaction before publishing events.
func CreateDraftTx(ctx context.Context, tx pgx.Tx, in CreatePurchaseOrderInput, actorEmployeeID int) (DraftRef, error) {
	if strings.TrimSpace(in.VendorUUID) == "" {
		return DraftRef{}, ClientError{Msg: "A vendor is required."}
	}
	if err := validateCustom(ctx, tx, in.CustomFields); err != nil {
		return DraftRef{}, err
	}
	vendorInternalID, vendorName, err := vendorSnapshot(ctx, tx, in.VendorUUID)
	if err != nil {
		return DraftRef{}, err
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return DraftRef{}, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, pordRecordTypeCode)
	if err != nil {
		return DraftRef{}, fmt.Errorf("resolve PORD record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return DraftRef{}, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"purchase_order_status", draftStatusID, ""},
		{"purchase_order_vendor_id", vendorInternalID, ""},
		{"purchase_order_vendor_name", vendorName, ""},
		{"purchase_order_reference_number", in.ReferenceNumber, ""},
		{"purchase_order_date", orNow(in.OrderDate), "::date"},
		{"purchase_order_expected_date", nullableDate(in.ExpectedDate), "::date"},
		{"purchase_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"purchase_order_memo", in.Memo, ""},
		{"purchase_order_notes", in.Notes, ""},
		{"purchase_order_internal_notes", in.InternalNotes, ""},
		{"purchase_order_terms_conditions", in.TermsConditions, ""},
		{"purchase_order_owner_id", nullableInt(ownerEmployeeID), ""},
		{"purchase_order_payment_terms", in.PaymentTermsID, ""},
		{"purchase_order_currency", in.CurrencyID, ""},
		{"purchase_order_subtotal", header.Subtotal, ""},
		{"purchase_order_discount_total", header.DiscountTotal, ""},
		{"purchase_order_tax_total", header.TaxTotal, ""},
		{"purchase_order_shipping_charge", in.ShippingCharge, ""},
		{"purchase_order_adjustment", in.Adjustment, ""},
		{"purchase_order_grand_total", header.GrandTotal, ""},
		{"purchase_order_custom_fields", custom, ""},
		{"purchase_order_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals(in.ShipTo)...)

	insertSQL, insertArgs := buildInsert("purchase_order", cv, "purchase_order_id, purchase_order_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return DraftRef{}, ClientError{Msg: "One of the referenced ids (payment terms, currency, state, or country) does not exist."}
		}
		return DraftRef{}, fmt.Errorf("insert purchase order: %w", err)
	}

	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return DraftRef{}, err
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return DraftRef{}, err
	}

	if _, err := tx.Exec(ctx, `INSERT INTO purchase_order_history(purchase_order_id,to_status_id,action,actor_employee_id) VALUES($1,$2,'create',$3)`, internalID, draftStatusID, nullableInt(actorEmployeeID)); err != nil {
		return DraftRef{}, fmt.Errorf("record draft creation: %w", err)
	}

	return DraftRef{InternalID: internalID, ID: newUUID}, nil
}

// NotifyDraftCreated publishes the standard creation event after a caller commits.
func NotifyDraftCreated(ctx context.Context, pool *pgxpool.Pool, draft DraftRef, actorEmployeeID int) {
	notifyCreated(ctx, pool, draft.ID, draft.InternalID, actorEmployeeID)
}
