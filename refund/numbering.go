package refund

import "fmt"

const numberPrefix = "RFND"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: RFND-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
