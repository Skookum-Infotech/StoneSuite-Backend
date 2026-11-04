package controllers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"stonesuite-backend/installschedule"
)

// Error codes carried in the "code" field of install-schedule error bodies.
const (
	installCodeInvalidInput  = "invalid_input"
	installCodeInvalidTime   = "invalid_time"
	installCodeNotFound      = "not_found"
	installCodeStaleVersion  = "stale_version"
	installCodeConflict      = "conflict"
	installCodeJobClosed     = "job_closed"
	installCodeInvalidStatus = "invalid_status"
	installCodeNotStarted    = "not_started"
	installCodeForbidden     = "forbidden"
	installCodeInternal      = "internal"
)

// Install-schedule error messages.
const (
	msgInstallNotFound      = "Install appointment not found."
	msgInstallJobNotFound   = "Fabrication job not found."
	msgInstallInternal      = "Something went wrong. Please try again."
	msgInstallBadBody       = "Invalid request body."
	msgInstallBodyTooLarge  = "Request body too large."
	msgInstallStale         = "This visit was changed by someone else. Reload and try again."
	msgInstallInvalidStatus = "Only scheduled visits can be changed."
	msgInstallNotStarted    = "A visit cannot be completed before it starts."
	msgInstallJobClosed     = "The fabrication job is closed to scheduling."
	msgInstallInvalidTime   = "That local time does not exist (daylight saving gap)."
)

// installFail writes {success:false, message, code} plus any extra top-level fields.
func installFail(w http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	body := map[string]any{"success": false, "message": msg, "code": code}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

// writeInstallError maps installschedule sentinels and typed errors to the plan's error envelope (§5.4).
func writeInstallError(w http.ResponseWriter, err error) {
	var (
		conflict *installschedule.ConflictError
		stale    *installschedule.StaleVersionError
		invalid  *installschedule.ValidationError
	)
	switch {
	case errors.As(err, &conflict):
		extra := map[string]any{"requiresAcknowledgement": true, "conflicts": conflict.Conflicts}
		if conflict.More > 0 {
			extra["moreConflicts"] = conflict.More
		}
		total := len(conflict.Conflicts) + conflict.More
		installFail(w, http.StatusConflict, installCodeConflict,
			fmt.Sprintf("This visit overlaps %d other booking(s).", total), extra)
	case errors.As(err, &stale):
		installFail(w, http.StatusConflict, installCodeStaleVersion, msgInstallStale,
			map[string]any{"currentVersion": stale.CurrentVersion})
	case errors.Is(err, installschedule.ErrStaleVersion):
		installFail(w, http.StatusConflict, installCodeStaleVersion, msgInstallStale, nil)
	case errors.Is(err, installschedule.ErrNotFound):
		installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallNotFound, nil)
	case errors.Is(err, installschedule.ErrInvalidStatus):
		installFail(w, http.StatusConflict, installCodeInvalidStatus, msgInstallInvalidStatus, nil)
	case errors.Is(err, installschedule.ErrNotStarted):
		installFail(w, http.StatusConflict, installCodeNotStarted, msgInstallNotStarted, nil)
	case errors.Is(err, installschedule.ErrJobClosed):
		installFail(w, http.StatusConflict, installCodeJobClosed, msgInstallJobClosed, nil)
	case errors.Is(err, installschedule.ErrNonexistentLocalTime):
		installFail(w, http.StatusBadRequest, installCodeInvalidTime, msgInstallInvalidTime, nil)
	case errors.As(err, &invalid):
		installFail(w, http.StatusBadRequest, installCodeInvalidInput,
			fmt.Sprintf("Invalid %s: %s.", invalid.Field, invalid.Message), map[string]any{"field": invalid.Field})
	default:
		slog.Error("install schedule request failed", "error", err)
		installFail(w, http.StatusInternalServerError, installCodeInternal, msgInstallInternal, nil)
	}
}
