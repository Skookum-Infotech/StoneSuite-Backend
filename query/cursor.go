package query

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// cursorData is the opaque keyset cursor payload. It pins the sort field +
// direction it was minted under so a cursor cannot be replayed against a
// different ordering, and carries the last row's sort value + id tiebreaker.
type cursorData struct {
	Sort  string    `json:"s"`
	Dir   Direction `json:"d"`
	Value any       `json:"v"`
	ID    string    `json:"id"`
}

// encodeCursor serializes a cursor to an opaque, URL-safe token.
func encodeCursor(c cursorData) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses a token. A malformed token is a client error (400), not
// a server fault. A well-formed but stale cursor is harmless: it only paginates
// within the caller's already scope-filtered set.
func decodeCursor(s string) (cursorData, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorData{}, invalid("cursor", "malformed cursor")
	}
	var c cursorData
	if err := json.Unmarshal(b, &c); err != nil {
		return cursorData{}, invalid("cursor", "malformed cursor")
	}
	return c, nil
}

// keysetSQL emits the keyset predicate for "rows after the cursor" under the
// effective sort. For ascending order rows after the cursor are greater; for
// descending, smaller. The id tiebreaker (always ASC) makes the order total.
func keysetSQL(sortExpr, idExpr string, dir Direction, dt DataType, cur cursorData, p *params) (string, error) {
	val, err := coerceScalar("cursor", dt, cur.Value)
	if err != nil {
		return "", err
	}
	cmp := ">"
	if dir == DirDesc {
		cmp = "<"
	}
	vp := p.add(val)
	ip := p.add(cur.ID)
	return "(" + sortExpr + " " + cmp + " " + vp +
		" OR (" + sortExpr + " = " + vp + " AND " + idExpr + " > " + ip + "))", nil
}

// NextCursor mints the cursor for the page-boundary row under the effective
// sort. The store passes the row's id and the value of the sort field; a
// time.Time value is normalized to RFC3339 so it round-trips through JSON back
// into a time.Time on the next request.
func NextCursor(id string, sort SortKey, value any) string {
	if t, ok := value.(time.Time); ok {
		value = t.Format(time.RFC3339Nano)
	}
	return encodeCursor(cursorData{
		Sort:  sort.Field,
		Dir:   sort.Dir,
		Value: value,
		ID:    id,
	})
}
