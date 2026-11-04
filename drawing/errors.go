package drawing

import "errors"

// ErrVersionConflict is returned by Save/Restore when the caller's
// expectedVersion no longer matches the current saved version.
var ErrVersionConflict = errors.New("drawing: version conflict")

// ErrNotFound is returned by Get/GetVersion when no drawing (or no such
// version) has been saved yet for the sales order.
var ErrNotFound = errors.New("drawing: not found")
