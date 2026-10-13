// Package kitref normalizes the kit a document line was added from.
//
// A kit is only a heading over a run of ordinary lines. A line therefore stores
// the kit's uuid — what makes consecutive lines one group — and the kit's name
// as it was when the line was added, so renaming or deleting the kit later never
// rewrites a saved document. The label carries no weight in any total, stock
// check or conversion, which is why it is validated here once rather than in
// each of the document modules that store it.
package kitref

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxNameLen is the longest kit name a line stores; it matches the
// inventory_kit.inventory_kit_name column.
const MaxNameLen = 100

// uuidPattern is the canonical 8-4-4-4-12 hex form. Checked here because a
// malformed value would otherwise reach the driver as a type error and surface
// as a 500 instead of the 400 it deserves.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Ref is the kit label a line carries. The zero value means "not from a kit".
type Ref struct {
	// UUID is the kit's external id, lower-cased; nil for a line outside any kit.
	UUID *string
	// Name is the kit's name when the line was added; "" for a line outside any kit.
	Name string
}

// IsSet reports whether the line belongs to a kit.
func (r Ref) IsSet() bool { return r.UUID != nil }

// Normalize validates the kit fields a client sent for one line. A blank uuid
// means the line is not from a kit (a stray name is ignored, so a client that
// clears one field cannot leave a half-set label behind). A returned message is
// a client error, worded to follow "Line N: ".
func Normalize(uuid, name string) (Ref, string) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return Ref{}, ""
	}
	if !uuidPattern.MatchString(uuid) {
		return Ref{}, "the kit id is not valid."
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Ref{}, "a kit line needs the kit's name."
	}
	if utf8.RuneCountInString(name) > MaxNameLen {
		return Ref{}, "the kit name is too long (100 characters at most)."
	}
	id := strings.ToLower(uuid)
	return Ref{UUID: &id, Name: name}, ""
}

// Args returns the two values a line insert binds for its kit columns: the uuid
// (SQL NULL when the line has no kit) and the name (empty when it has none).
func (r Ref) Args() (uuid any, name string) {
	if r.UUID == nil {
		return nil, ""
	}
	return *r.UUID, r.Name
}
