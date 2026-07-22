package fabrication

import "fmt"

// numberPrefix is the FJOB record-type code (lkp_record_type.record_type_code).
const numberPrefix = "FJOB"

// FormatNumber renders the human-readable job number from the row's serial PK,
// zero-padded to 6 digits: FJOB-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
