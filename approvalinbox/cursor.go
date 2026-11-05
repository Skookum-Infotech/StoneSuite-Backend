package approvalinbox

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
)

const cursorSep = "|"

// ErrBadCursor is returned for any cursor that is not one this package issued.
var ErrBadCursor = errors.New("invalid cursor")

var cursorUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Cursor is the keyset position of the last row of a page, in the inbox sort
// order (entered_at, workflow_key, uuid).
type Cursor struct {
	EnteredAt   time.Time
	WorkflowKey string
	UUID        string
}

// Encode renders the cursor as base64url(enteredAt|workflowKey|uuid).
func (c Cursor) Encode() string {
	raw := c.EnteredAt.UTC().Format(time.RFC3339Nano) + cursorSep + c.WorkflowKey + cursorSep + c.UUID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor strictly validates a client-supplied cursor: three parts, an RFC 3339
// timestamp, a registry workflow key and a lowercase UUID. Anything else is ErrBadCursor.
func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrBadCursor
	}
	parts := strings.Split(string(raw), cursorSep)
	if len(parts) != 3 {
		return Cursor{}, ErrBadCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}, ErrBadCursor
	}
	if _, ok := metas[parts[1]]; !ok {
		return Cursor{}, ErrBadCursor
	}
	if !cursorUUIDRE.MatchString(parts[2]) {
		return Cursor{}, ErrBadCursor
	}
	return Cursor{EnteredAt: ts.UTC(), WorkflowKey: parts[1], UUID: parts[2]}, nil
}
