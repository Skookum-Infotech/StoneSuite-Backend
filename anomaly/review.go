package anomaly

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultPageSize is the list page size when the caller names none.
const DefaultPageSize = 25

// MaxPageSize caps the list page size.
const MaxPageSize = 100

// MaxNoteLen caps a reviewer's note.
const MaxNoteLen = 1000

const cursorSep = "|"

// pgTimestampFormat is how the cursor carries anomaly_detected_at: Postgres
// text form with microseconds, so it round-trips with no time-zone guess.
const pgTimestampFormat = `YYYY-MM-DD HH24:MI:SS.US`

// cursorTimeLayout is pgTimestampFormat in Go's layout language, used only to
// check that a decoded cursor is well formed.
const cursorTimeLayout = "2006-01-02 15:04:05.999999"

// ErrNotFound is returned when a finding does not exist or is not visible to
// the viewer. Both look the same on purpose (IDOR guard).
var ErrNotFound = errors.New("anomaly not found")

// ErrBadCursor is returned for a cursor the server did not issue.
var ErrBadCursor = errors.New("invalid cursor")

// Viewer says whose findings a list or update may touch. A super admin sees
// every finding; anyone else only those addressed to them.
type Viewer struct {
	IdentityID string
	SeesAll    bool
}

// Item is one finding as the Anomalies page shows it.
type Item struct {
	ID              string     `json:"id"`
	RuleKey         string     `json:"ruleKey"`
	Severity        Severity   `json:"severity"`
	Resource        string     `json:"resource"`
	RecordID        string     `json:"recordId,omitempty"`
	Title           string     `json:"title"`
	Detail          string     `json:"detail"`
	Status          Status     `json:"status"`
	Note            string     `json:"note"`
	DetectedAt      time.Time  `json:"detectedAt"`
	StatusChangedAt *time.Time `json:"statusChangedAt,omitempty"`
}

// Cursor is a keyset position in the list, newest first.
type Cursor struct {
	DetectedAt string
	ID         int
}

// ListFilter narrows and pages the list. A zero Status means every status.
type ListFilter struct {
	Status Status
	Limit  int
	Cursor *Cursor
}

// Page is one page of findings plus the cursor for the next, if any.
type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// EncodeCursor packs a keyset position into an opaque string.
func EncodeCursor(c Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.DetectedAt + cursorSep + strconv.Itoa(c.ID)))
}

// DecodeCursor unpacks a string EncodeCursor issued.
func DecodeCursor(s string) (*Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadCursor
	}
	ts, idText, ok := strings.Cut(string(raw), cursorSep)
	if !ok {
		return nil, ErrBadCursor
	}
	if _, err := time.Parse(cursorTimeLayout, ts); err != nil {
		return nil, ErrBadCursor
	}
	id, err := strconv.Atoi(idText)
	if err != nil || id < 1 {
		return nil, ErrBadCursor
	}
	return &Cursor{DetectedAt: ts, ID: id}, nil
}

// visibleSQL restricts a query on alias a to the viewer's findings. $1 is
// SeesAll and $2 the viewer's identity id.
const visibleSQL = `($1::bool OR EXISTS (
	SELECT 1 FROM anomaly_recipient r
	WHERE r.anomaly_id = a.anomaly_id AND r.recipient_identity_id = $2::uuid))`

const itemColumns = `a.anomaly_id, a.anomaly_uuid::text, a.anomaly_rule_key, a.anomaly_severity, a.anomaly_resource,
	COALESCE(a.anomaly_record_uuid::text, ''), a.anomaly_title, a.anomaly_detail, a.anomaly_status, a.anomaly_note,
	a.anomaly_detected_at, a.anomaly_status_changed_at,
	to_char(a.anomaly_detected_at, '` + pgTimestampFormat + `')`

func scanItem(row pgx.Row) (Item, int, string, error) {
	var (
		it         Item
		id         int
		detectedTS string
	)
	err := row.Scan(&id, &it.ID, &it.RuleKey, &it.Severity, &it.Resource, &it.RecordID, &it.Title, &it.Detail,
		&it.Status, &it.Note, &it.DetectedAt, &it.StatusChangedAt, &detectedTS)
	return it, id, detectedTS, err
}

// List returns one page of the viewer's findings, newest first.
func List(ctx context.Context, q Querier, v Viewer, f ListFilter) (Page, error) {
	limit := f.Limit
	if limit < 1 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)

	var (
		cursorTS *string
		cursorID *int
	)
	if f.Cursor != nil {
		cursorTS, cursorID = &f.Cursor.DetectedAt, &f.Cursor.ID
	}
	rows, err := q.Query(ctx, `
		SELECT `+itemColumns+`
		FROM anomaly a
		WHERE `+visibleSQL+`
		  AND ($3 = '' OR a.anomaly_status = $3)
		  AND ($4::text IS NULL OR (a.anomaly_detected_at, a.anomaly_id) < ($4::timestamp, $5::int))
		ORDER BY a.anomaly_detected_at DESC, a.anomaly_id DESC
		LIMIT $6`,
		v.SeesAll, v.IdentityID, string(f.Status), cursorTS, cursorID, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("list anomalies: %w", err)
	}
	defer rows.Close()

	page := Page{Items: []Item{}}
	var lastID int
	var lastTS string
	for rows.Next() {
		it, id, ts, err := scanItem(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan anomaly: %w", err)
		}
		if len(page.Items) == limit {
			page.NextCursor = EncodeCursor(Cursor{DetectedAt: lastTS, ID: lastID})
			break
		}
		page.Items = append(page.Items, it)
		lastID, lastTS = id, ts
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate anomalies: %w", err)
	}
	return page, nil
}

// UpdateStatus sets a finding's review status and note, recording who did it.
// It returns ErrNotFound when the finding is missing or not the viewer's.
func UpdateStatus(ctx context.Context, q Querier, v Viewer, anomalyUUID string, status Status, note string) (Item, error) {
	row := q.QueryRow(ctx, `
		UPDATE anomaly a
		SET anomaly_status = $3, anomaly_note = $4,
		    anomaly_status_changed_at = CURRENT_TIMESTAMP, anomaly_status_changed_by = $2::uuid
		WHERE a.anomaly_uuid = $5::uuid AND `+visibleSQL+`
		RETURNING `+itemColumns,
		v.SeesAll, v.IdentityID, string(status), note, anomalyUUID)
	it, _, _, err := scanItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("update anomaly %s: %w", anomalyUUID, err)
	}
	return it, nil
}
