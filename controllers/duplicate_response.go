package controllers

import (
	"net/http"

	"stonesuite-backend/duplicate"
)

// writeDuplicate answers a create/rename that collides with an existing
// record's name: 409 with a readable message, plus the record it collided with
// so the client can offer to open it (or reactivate it) instead of only
// refusing. The message alone is enough for a client that ignores the rest.
func writeDuplicate(w http.ResponseWriter, d *duplicate.Error) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"success": false,
		"message": d.Error(),
		"duplicate": map[string]any{
			"entity": d.Entity,
			"id":     d.ExistingID,
			"name":   d.ExistingName,
			"status": d.ExistingStatus,
		},
	})
}
