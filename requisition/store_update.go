// requisition/store_update.go
package requisition

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live requisition's header fields and lines (recomputing
// totals) inside one transaction. Allowed only at DRFT (AD-12) — once
// submitted the request is visible to an approver; recall it to draft
// (PAPV→DRFT / APPV→DRFT) to revise.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateRequisitionInput, actorEmployeeID int) (*Requisition, error) {
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}
	priority, err := orNormal(in.Priority)
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update requisition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT reqn.requisition_id, rs.record_status_code
		FROM requisition reqn JOIN lkp_record_status rs ON rs.record_status_id = reqn.requisition_status
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL
		FOR UPDATE OF reqn`, uuid,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load requisition for update: %w", err)
	}
	if statusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft requisition can be edited. Recall it to draft first."}
	}

	var vendorInternalID any
	var vendorName string
	if strings.TrimSpace(in.VendorUUID) != "" {
		id, name, err := vendorSnapshot(ctx, tx, in.VendorUUID)
		if err != nil {
			return nil, err
		}
		vendorInternalID, vendorName = id, name
	}

	lines, err := resolveLines(ctx, tx, in.Items)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.SalesTaxPercent)

	requestedByID := in.RequestedByEmployeeID
	if requestedByID <= 0 {
		requestedByID = actorEmployeeID
	}

	// requisition_custom_fields is NOT NULL DEFAULT '{}'; a nil map encodes
	// as SQL NULL and violates the constraint.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"requisition_requested_by_id", nullableInt(requestedByID), ""},
		{"requisition_department", in.Department, ""},
		{"requisition_needed_by_date", nullableDate(in.NeededByDate), "::date"},
		{"requisition_priority", priority, ""},
		{"requisition_memo", in.Memo, ""},
		{"requisition_vendor_id", vendorInternalID, ""},
		{"requisition_vendor_name", vendorName, ""},
		{"requisition_payment_terms", in.PaymentTermsID, ""},
		{"requisition_sales_tax_percent", in.SalesTaxPercent, ""},
		{"requisition_subtotal", header.Subtotal, ""},
		{"requisition_tax_total", header.TaxTotal, ""},
		{"requisition_estimated_total", header.EstimatedTotal, ""},
		{"requisition_custom_fields", custom, ""},
		{"requisition_updated_by", nullableInt(actorEmployeeID), ""},
	}

	updateSQL, updateArgs := buildUpdateSet("requisition", []any{uuid}, cv,
		[]string{"requisition_updated_at = NOW()", "requisition_record_version = requisition_record_version + 1"},
		"requisition_uuid = $1 AND requisition_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (requester, payment terms, or vendor) does not exist."}
		}
		return nil, fmt.Errorf("update requisition: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE requisition_item SET item_deleted_at = NOW() WHERE requisition_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous requisition items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update requisition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live requisition deleted. Only DRFT and CANC
// requisitions may be deleted (AD-11) — a request visible to an approver
// keeps its trail.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE requisition reqn
		SET requisition_deleted_at = NOW(), requisition_deleted_by = $2
		FROM lkp_record_status rs
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL
		  AND rs.record_status_id = reqn.requisition_status
		  AND rs.record_status_code IN ('DRFT','CANC')`,
		uuid, actorOrSystem(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete requisition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "not found" from "found but not deletable" for the 400/404 split.
		var exists bool
		if qerr := pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM requisition
				WHERE requisition_uuid = $1 AND requisition_deleted_at IS NULL)`, uuid).Scan(&exists); qerr == nil && exists {
			return ClientError{Msg: "Only a draft or cancelled requisition can be deleted."}
		}
		return ErrNotFound
	}
	return nil
}
