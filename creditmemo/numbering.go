package creditmemo

import "fmt"

// numberPrefix matches the CRDT record type code seeded in lkp_record_type.
const numberPrefix = "CRDT"

// FormatNumber renders a credit memo's human-facing document number from its
// serial primary key, e.g. CRDT-000001. Numbers are assigned post-insert from
// the returned serial (there is no sequence table) — see Create.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
