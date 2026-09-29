package controllers

import (
	"net/http"

	"stonesuite-backend/inventory"
)

// stockShortageCode is the machine-readable code the frontend keys its "not
// enough stock" dialog on. A message alone would be all it could show.
const stockShortageCode = "insufficient_stock"

// writeStockShortage answers a save that asked for more stock than is free: a
// 409 carrying every short item (what was needed, what is free, how much is
// missing) so the screen can list them and offer to restock. Nothing was saved.
func writeStockShortage(w http.ResponseWriter, e *inventory.StockShortageError) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"success":   false,
		"code":      stockShortageCode,
		"message":   e.Error(),
		"shortages": e.Shortages,
	})
}
