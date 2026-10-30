package purchaseorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// loadRequisitionSnapshot loads a live requisition's header snapshot by
// external uuid inside tx. The row is read FOR SHARE so requisition.SoftDelete
// (FOR UPDATE, then a conversion count) serializes against this conversion:
// it either sees the conversion row or makes this read find nothing.
func loadRequisitionSnapshot(ctx context.Context, tx pgx.Tx, requisitionUUID string) (*requisitionSnapshot, error) {
	var s requisitionSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT reqn.requisition_id, rs.record_status_code,
		       reqn.requisition_memo, reqn.requisition_payment_terms,
		       reqn.requisition_sales_tax_percent, reqn.requisition_custom_fields
		FROM requisition reqn JOIN lkp_record_status rs ON rs.record_status_id = reqn.requisition_status
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL
		FOR SHARE OF reqn`, requisitionUUID).Scan(
		&s.internalID, &s.statusCode,
		&s.memo, &s.paymentTermsID,
		&s.salesTaxPercent, &customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRequisitionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load requisition snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}
