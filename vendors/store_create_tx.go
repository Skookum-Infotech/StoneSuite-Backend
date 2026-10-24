package vendors

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CreateTx inserts a new vendor inside the caller's transaction (no Begin,
// Commit or read-back) and returns its external uuid. The caller owns commit
// and rollback; read the row back with GetQ on the same tx.
func CreateTx(ctx context.Context, tx pgx.Tx, in CreateVendorInput, actorEmployeeID int) (string, error) {
	if err := validateVendorType(in.VendorType, in.vendorFields); err != nil {
		return "", err
	}

	if err := checkVendorNameFree(ctx, tx, vendorDuplicateName(in.VendorType, in.vendorFields), ""); err != nil {
		return "", err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vendorRecordTypeCode)
	if err != nil {
		return "", fmt.Errorf("resolve VNDR record type: %w", err)
	}
	statusID, err := statusIDByCode(ctx, tx, recordTypeID, defaultStatusCode)
	if err != nil {
		return "", fmt.Errorf("resolve ACT_ status: %w", err)
	}

	cv := append([]colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_status", statusID, ""},
		{"vendor_type", in.VendorType, ""},
		{"vendor_owner_id", nullableInt(actorEmployeeID), ""},
	}, vendorColVals(in.vendorFields)...)
	cv = append(cv, colVal{"vendor_created_by", nullableInt(actorEmployeeID), ""})

	insertSQL, insertArgs := buildInsert("vendor", cv, "vendor_id, vendor_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		if isForeignKeyViolation(err) {
			return "", ClientError{Msg: "The referenced nationality/country does not exist."}
		}
		return "", fmt.Errorf("insert vendor: %w", err)
	}

	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return "", err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &statusID, actorEmployeeID)
	return newUUID, nil
}
