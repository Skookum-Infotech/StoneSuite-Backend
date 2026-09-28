// Package duplicate is the one error every master-data store returns when a
// create or a rename would collide with a live record that already carries the
// same name — customer, vendor, inventory item.
//
// The check runs in application code rather than as a unique index on purpose:
// a tenant may already hold same-named rows, and a unique index would fail the
// schema apply on them. Stores therefore compare on create and on an actual
// rename only, so a pre-existing duplicate can still be edited for any other
// field. Two writers racing on the same brand-new name can still both win —
// closing that would need the index this package deliberately avoids.
package duplicate

import (
	"errors"
	"fmt"
	"strings"
)

// Entity names as they appear in messages and in the API's `duplicate.entity`.
const (
	EntityCustomer = "customer"
	EntityVendor   = "vendor"
	EntityItem     = "item"
)

// Error reports that Name is already taken by a live record. The Existing*
// fields describe that record so a client can point the user at it instead of
// just refusing — status is the record's human label (Active, Inactive,
// Draft, ...), because a same-named record that is not usable is exactly the
// case a picker cannot see.
type Error struct {
	Entity         string
	Name           string
	ExistingID     string
	ExistingName   string
	ExistingStatus string
}

func (e *Error) Error() string {
	name := e.ExistingName
	if name == "" {
		name = e.Name
	}
	msg := fmt.Sprintf("A %s named %q already exists", e.Entity, name)
	if e.ExistingStatus != "" {
		msg += fmt.Sprintf(" (%s)", e.ExistingStatus)
	}
	return msg + ". Use the existing record, or choose a different name."
}

// As returns the *Error inside err, if any.
func As(err error) (*Error, bool) {
	var d *Error
	ok := errors.As(err, &d)
	return d, ok
}

// Key normalizes a name for comparison: case-insensitive, trimmed, and with
// runs of whitespace collapsed. Stores use it to tell whether a rename actually
// changed the name; the collision query itself normalizes in SQL with
// NormalizeSQL so both sides of that comparison are computed the same way.
func Key(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// NormalizeSQL wraps a text expression in the SQL equivalent of Key, for
// comparing a column against a bound parameter.
func NormalizeSQL(expr string) string {
	return `lower(btrim(regexp_replace(` + expr + `, '\s+', ' ', 'g')))`
}
