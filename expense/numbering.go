package expense

import "fmt"

// numberPrefix is the EXPN record-type code (lkp_record_type.record_type_code).
const numberPrefix = "EXPN"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: EXPN-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
