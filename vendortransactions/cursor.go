package vendortransactions

import (
	"encoding/base64"
	"strings"
	"time"
)

// cursorSep splits the encoded cursor into its created_at and uuid halves. It
// cannot occur in either: timestamps and canonical UUIDs never contain a pipe.
const cursorSep = "|"

// timestampLayout is how the cursor's created_at travels: the tables store
// `timestamp` (no zone), so it is bound as text and cast ::timestamp, never as
// a zone-carrying time.Time. Microsecond precision matches Postgres.
const timestampLayout = "2006-01-02 15:04:05.999999"

// cursor is the keyset position: the (created_at, uuid) of the last row of the
// previous page. (created_at DESC, id DESC) is a total order because a uuid is
// unique, so a page never overlaps or skips while the data holds still.
type cursor struct {
	createdAt time.Time
	id        string
}

// encode renders the cursor as an opaque URL-safe token.
func (c cursor) encode() string {
	raw := c.createdAt.UTC().Format(timestampLayout) + cursorSep + strings.ToLower(c.id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses a token produced by encode. Anything else, including a
// well-formed token with a bad timestamp or uuid, is rejected so a tampered
// cursor can never reach the SQL as anything but a validated value.
func decodeCursor(token string) (cursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor{}, false
	}
	ts, id, ok := strings.Cut(string(raw), cursorSep)
	if !ok || !uuidRe.MatchString(id) {
		return cursor{}, false
	}
	t, err := time.Parse(timestampLayout, ts)
	if err != nil {
		return cursor{}, false
	}
	return cursor{createdAt: t, id: strings.ToLower(id)}, true
}
