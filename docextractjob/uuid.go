package docextractjob

import "regexp"

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validUUID reports whether s is a canonical UUID string. Ids from a request
// are checked with it so a malformed one is a not-found, never a 500.
func validUUID(s string) bool { return uuidRe.MatchString(s) }
