package itemreceipt

import "fmt"

// numberPrefix is the IRCT record-type code (lkp_record_type.record_type_code).
const numberPrefix = "IRCT"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: IRCT-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
