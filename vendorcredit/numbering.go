package vendorcredit

import "fmt"

// numberPrefix prefixes every generated vendor credit number. Deliberately
// shorter than the VCRD record-type code (lkp_record_type.record_type_code)
// per the request's requested document-number format.
const numberPrefix = "VCR"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: VCR-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
