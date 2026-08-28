// Package feedback is the control-plane read/write model for in-app feedback
// tickets — bugs, feature requests, UX complaints and performance reports
// filed by tenant staff or customer-portal users, triaged by StoneSuite
// platform admins.
//
// This lives in the control-plane database (stonesuite_cp), NOT a tenant
// database, because the admin ticket list is inherently cross-tenant: scoping
// it per-tenant would force a fan-out read across every tenant database just
// to render one list. It mirrors auditstore's shape (opaque keyset
// pagination, a parameterized Filter struct) since both are "browse a
// control-plane table" read models.
package feedback

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reporter kinds — who filed the ticket.
const (
	KindStaff  = "staff"
	KindPortal = "portal"
)

// Comment/timeline author kinds. A platform admin's replies are attributed
// distinctly from a staff reporter's, even though both come from `identities`.
const (
	AuthorStaff         = "staff"
	AuthorPortal        = "portal"
	AuthorPlatformAdmin = "platform_admin"
)

// Ticket categories, matching the reporter widget's category picker.
const (
	CategoryBug            = "bug"
	CategoryFeatureRequest = "feature_request"
	CategoryUXImprovement  = "ux_improvement"
	CategoryPerformance    = "performance"
	CategoryGeneral        = "general"
)

// Ticket areas — which section of the app the reporter had open, so an
// admin has some idea where to reproduce a bug without guessing from
// page_url alone. Named "area", not "workspace": in StoneSuite "workspace"
// already means the tenant a customer-portal identity is signed into (see
// PortalWorkspace on the frontend) — reusing that word here would collide
// with an unrelated concept. The frontend auto-selects one of these from the
// reporter's current route, but the value is still just their choice at
// submit time — not re-derived or verified server-side.
const (
	AreaDashboard     = "dashboard"
	AreaCRM           = "crm"
	AreaSales         = "sales"
	AreaPurchases     = "purchases"
	AreaInventory     = "inventory"
	AreaFinance       = "finance"
	AreaConfiguration = "configuration"
	AreaAccount       = "account"
	AreaOther         = "other"
)

// Ticket statuses. Platform admins move a ticket through these; the reporter
// only ever reads the current value.
const (
	StatusNew        = "new"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
)

// Ticket priorities, admin-only.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// Comment/timeline event types.
const (
	EventComment      = "comment"
	EventStatusChange = "status_change"
)

// DefaultLimit / MaxLimit bound list page sizes.
const (
	DefaultLimit = 25
	MaxLimit     = 100
)

// MaxDescriptionLength / MaxCommentLength / MaxInternalNotesLength cap
// free-text fields against abuse; generous enough for real bug reports.
const (
	MaxDescriptionLength   = 5000
	MaxCommentLength       = 5000
	MaxInternalNotesLength = 5000
)

var (
	// ErrNotFound is returned when a ticket lookup misses, or matches a row the
	// caller is not scoped to see. Callers must map this to HTTP 404 (never
	// 403) so ticket ids cannot be enumerated across tenants or reporters.
	ErrNotFound = errors.New("feedback ticket not found")
	// ErrInvalidCursor is returned when a pagination cursor cannot be decoded.
	ErrInvalidCursor = errors.New("invalid pagination cursor")
)

var validCategories = map[string]bool{
	CategoryBug:            true,
	CategoryFeatureRequest: true,
	CategoryUXImprovement:  true,
	CategoryPerformance:    true,
	CategoryGeneral:        true,
}

// validAreas also accepts "" (unspecified) — see ValidArea.
var validAreas = map[string]bool{
	AreaDashboard:     true,
	AreaCRM:           true,
	AreaSales:         true,
	AreaPurchases:     true,
	AreaInventory:     true,
	AreaFinance:       true,
	AreaConfiguration: true,
	AreaAccount:       true,
	AreaOther:         true,
}

var validStatuses = map[string]bool{
	StatusNew:        true,
	StatusInProgress: true,
	StatusDone:       true,
	StatusCancelled:  true,
}

var validPriorities = map[string]bool{
	PriorityLow:    true,
	PriorityNormal: true,
	PriorityHigh:   true,
	PriorityUrgent: true,
}

var validReporterKinds = map[string]bool{
	KindStaff:  true,
	KindPortal: true,
}

// ValidCategory reports whether c is one of the fixed category values.
func ValidCategory(c string) bool { return validCategories[c] }

// ValidArea reports whether a is one of the fixed area values, or empty
// (older tickets predating this field, or a reporter who skipped it).
func ValidArea(a string) bool { return a == "" || validAreas[a] }

// ValidStatus reports whether s is one of the fixed status values.
func ValidStatus(s string) bool { return validStatuses[s] }

// ValidPriority reports whether p is one of the fixed priority values.
func ValidPriority(p string) bool { return validPriorities[p] }

// ValidReporterKind reports whether k is a recognised reporter kind.
func ValidReporterKind(k string) bool { return validReporterKinds[k] }

// FormatTicketNumber renders a ticket's sequence number as its human-facing
// id, e.g. 123 -> "FB-000123". The sequence (platform_feedback_ticket_seq) is
// the single source of truth; this is a pure display formatter, never stored.
func FormatTicketNumber(seq int64) string {
	return fmt.Sprintf("FB-%06d", seq)
}

// Ticket is one platform_feedback row.
type Ticket struct {
	ID                      string    `json:"id"`
	TicketSeq               int64     `json:"ticketSeq"`
	TicketNumber            string    `json:"ticketNumber"` // derived: FormatTicketNumber(TicketSeq)
	TenantID                string    `json:"tenantId"`
	TenantName              string    `json:"tenantName,omitempty"` // joined, populated by admin list/get only
	ReporterIdentityID      string    `json:"reporterIdentityId,omitempty"`
	ReporterKind            string    `json:"reporterKind"`
	ReporterEmail           string    `json:"reporterEmail"`
	ReporterName            string    `json:"reporterName"`
	Category                string    `json:"category"`
	Area                    string    `json:"area,omitempty"`
	Rating                  *int      `json:"rating,omitempty"`
	Description             string    `json:"description"`
	PageURL                 string    `json:"pageUrl,omitempty"`
	UserAgent               string    `json:"userAgent,omitempty"`
	Status                  string    `json:"status"`
	Priority                string    `json:"priority"`
	AssignedAdminIdentityID string    `json:"assignedAdminIdentityId,omitempty"`
	AssignedAdminName       string    `json:"assignedAdminName,omitempty"` // joined, populated by admin list/get only
	InternalNotes           string    `json:"internalNotes,omitempty"`
	ReporterLastSeenAt      time.Time `json:"reporterLastSeenAt"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

// Comment is one entry in a ticket's timeline (a reply, or a status-change
// marker). IsInternal rows must never be returned on a reporter-facing route.
type Comment struct {
	ID               string    `json:"id"`
	FeedbackID       string    `json:"feedbackId"`
	AuthorIdentityID string    `json:"authorIdentityId,omitempty"`
	AuthorKind       string    `json:"authorKind"`
	AuthorName       string    `json:"authorName"`
	Body             string    `json:"body,omitempty"`
	IsInternal       bool      `json:"isInternal"`
	EventType        string    `json:"eventType"`
	OldStatus        string    `json:"oldStatus,omitempty"`
	NewStatus        string    `json:"newStatus,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Attachment is one file attached to a ticket, stored in the reporting
// tenant's own R2 bucket under `feedback/{feedbackId}/...`.
type Attachment struct {
	ID             string    `json:"id"`
	FeedbackID     string    `json:"feedbackId"`
	FileName       string    `json:"fileName"`
	ContentType    string    `json:"contentType"`
	SizeBytes      int64     `json:"sizeBytes"`
	StorageKey     string    `json:"storageKey"`
	ChecksumSHA256 string    `json:"checksumSha256,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// CreateInput carries the fields needed to file a new ticket.
type CreateInput struct {
	TenantID           string
	ReporterIdentityID string
	ReporterKind       string
	ReporterEmail      string
	ReporterName       string
	Category           string
	Area               string
	Rating             *int
	Description        string
	PageURL            string
	UserAgent          string
}

// ticketColumns deliberately OMITS internal_notes — every caller of
// scanTicket (Create, GetForReporter, ListForReporter) is reporter-facing,
// and internal_notes is an admin-only field that must never reach a
// reporter's response. The admin-facing path uses adminTicketColumns /
// scanAdminTicket instead, which does select it.
const ticketColumns = `
	id, ticket_seq, tenant_id, COALESCE(reporter_identity_id::text, ''), reporter_kind,
	reporter_email, reporter_name, category, area, rating, description, page_url, user_agent,
	status, priority, COALESCE(assigned_admin_identity_id::text, ''),
	reporter_last_seen_at, created_at, updated_at`

func scanTicket(row pgx.Row) (*Ticket, error) {
	var t Ticket
	if err := row.Scan(
		&t.ID, &t.TicketSeq, &t.TenantID, &t.ReporterIdentityID, &t.ReporterKind,
		&t.ReporterEmail, &t.ReporterName, &t.Category, &t.Area, &t.Rating, &t.Description, &t.PageURL, &t.UserAgent,
		&t.Status, &t.Priority, &t.AssignedAdminIdentityID,
		&t.ReporterLastSeenAt, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan feedback ticket: %w", err)
	}
	t.TicketNumber = FormatTicketNumber(t.TicketSeq)
	return &t, nil
}

// Create inserts a new ticket and returns it.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateInput) (*Ticket, error) {
	row := pool.QueryRow(ctx, `
		INSERT INTO platform_feedback (
			tenant_id, reporter_identity_id, reporter_kind, reporter_email, reporter_name,
			category, area, rating, description, page_url, user_agent
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+ticketColumns,
		in.TenantID, nullable(in.ReporterIdentityID), in.ReporterKind, in.ReporterEmail, in.ReporterName,
		in.Category, in.Area, in.Rating, in.Description, in.PageURL, in.UserAgent,
	)
	return scanTicket(row)
}

// GetForReporter fetches a ticket, scoped to the reporter who filed it (and
// their tenant). Returns ErrNotFound for a real miss AND for a ticket that
// exists but belongs to someone else — the caller must not be able to
// distinguish the two (IDOR: no existence leak).
func GetForReporter(ctx context.Context, pool *pgxpool.Pool, id, tenantID, reporterIdentityID string) (*Ticket, error) {
	row := pool.QueryRow(ctx, `
		SELECT `+ticketColumns+`
		FROM platform_feedback
		WHERE id = $1 AND tenant_id = $2 AND reporter_identity_id = $3`,
		id, tenantID, reporterIdentityID,
	)
	return scanTicket(row)
}

// ListForReporter returns a reporter's own tickets, newest first, keyset paginated.
func ListForReporter(ctx context.Context, pool *pgxpool.Pool, tenantID, reporterIdentityID, cursor string, limit int) ([]Ticket, string, error) {
	limit = clampLimit(limit)

	args := []any{tenantID, reporterIdentityID}
	where := "tenant_id = $1 AND reporter_identity_id = $2"
	if cursor != "" {
		ts, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, ts, id)
		where += fmt.Sprintf(" AND (created_at < $%d OR (created_at = $%d AND id < $%d))", len(args)-1, len(args)-1, len(args))
	}
	args = append(args, limit+1)

	sql := `SELECT ` + ticketColumns + ` FROM platform_feedback WHERE ` + where +
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list reporter tickets: %w", err)
	}
	defer rows.Close()
	return collectTicketPage(rows, limit)
}

// AdminFilter is the parameterized query surface for the platform-admin ticket list.
type AdminFilter struct {
	Status   string // exact match, optional
	Category string // exact match, optional
	Priority string // exact match, optional
	TenantID string // exact match, optional
	Search   string // ILIKE match against description, optional
	Limit    int
	Cursor   string
}

// ListForAdmin returns tickets across all tenants, newest first, keyset
// paginated, joined with the reporting tenant's display name and the
// assigned admin's name for the list view.
func ListForAdmin(ctx context.Context, pool *pgxpool.Pool, f AdminFilter) ([]Ticket, string, error) {
	limit := clampLimit(f.Limit)

	var conds []string
	var args []any
	param := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if f.Status != "" {
		conds = append(conds, "pf.status = "+param(f.Status))
	}
	if f.Category != "" {
		conds = append(conds, "pf.category = "+param(f.Category))
	}
	if f.Priority != "" {
		conds = append(conds, "pf.priority = "+param(f.Priority))
	}
	if f.TenantID != "" {
		conds = append(conds, "pf.tenant_id = "+param(f.TenantID))
	}
	if f.Search != "" {
		conds = append(conds, "pf.description ILIKE "+param("%"+f.Search+"%"))
	}
	if f.Cursor != "" {
		ts, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		tsP, idP := param(ts), param(id)
		conds = append(conds, "(pf.created_at < "+tsP+" OR (pf.created_at = "+tsP+" AND pf.id < "+idP+"))")
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	sql := `
		SELECT ` + adminTicketColumns + `
		FROM platform_feedback pf
		LEFT JOIN tenants t ON t.id = pf.tenant_id
		LEFT JOIN identities admin ON admin.id = pf.assigned_admin_identity_id
		` + where + `
		ORDER BY pf.created_at DESC, pf.id DESC
		LIMIT ` + param(limit+1)

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list admin tickets: %w", err)
	}
	defer rows.Close()
	return collectAdminTicketPage(rows, limit)
}

const adminTicketColumns = `
	pf.id, pf.ticket_seq, pf.tenant_id, COALESCE(t.display_name, ''),
	COALESCE(pf.reporter_identity_id::text, ''), pf.reporter_kind,
	pf.reporter_email, pf.reporter_name, pf.category, pf.area, pf.rating, pf.description,
	pf.page_url, pf.user_agent, pf.status, pf.priority,
	COALESCE(pf.assigned_admin_identity_id::text, ''), COALESCE(admin.full_name, ''),
	pf.internal_notes, pf.reporter_last_seen_at, pf.created_at, pf.updated_at`

func scanAdminTicket(row pgx.Row) (*Ticket, error) {
	var t Ticket
	if err := row.Scan(
		&t.ID, &t.TicketSeq, &t.TenantID, &t.TenantName,
		&t.ReporterIdentityID, &t.ReporterKind,
		&t.ReporterEmail, &t.ReporterName, &t.Category, &t.Area, &t.Rating, &t.Description,
		&t.PageURL, &t.UserAgent, &t.Status, &t.Priority,
		&t.AssignedAdminIdentityID, &t.AssignedAdminName,
		&t.InternalNotes, &t.ReporterLastSeenAt, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan admin feedback ticket: %w", err)
	}
	t.TicketNumber = FormatTicketNumber(t.TicketSeq)
	return &t, nil
}

// GetForAdmin fetches one ticket (any tenant) with the joined tenant/admin
// display fields, for the platform-admin detail page.
func GetForAdmin(ctx context.Context, pool *pgxpool.Pool, id string) (*Ticket, error) {
	row := pool.QueryRow(ctx, `
		SELECT `+adminTicketColumns+`
		FROM platform_feedback pf
		LEFT JOIN tenants t ON t.id = pf.tenant_id
		LEFT JOIN identities admin ON admin.id = pf.assigned_admin_identity_id
		WHERE pf.id = $1`, id)
	return scanAdminTicket(row)
}

func collectTicketPage(rows pgx.Rows, limit int) ([]Ticket, string, error) {
	tickets := make([]Ticket, 0, limit)
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, "", err
		}
		tickets = append(tickets, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list feedback tickets: %w", err)
	}
	return paginate(tickets, limit)
}

func collectAdminTicketPage(rows pgx.Rows, limit int) ([]Ticket, string, error) {
	tickets := make([]Ticket, 0, limit)
	for rows.Next() {
		t, err := scanAdminTicket(rows)
		if err != nil {
			return nil, "", err
		}
		tickets = append(tickets, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list admin feedback tickets: %w", err)
	}
	return paginate(tickets, limit)
}

func paginate(tickets []Ticket, limit int) ([]Ticket, string, error) {
	next := ""
	if len(tickets) > limit {
		last := tickets[limit-1]
		tickets = tickets[:limit]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return tickets, next, nil
}

// UpdateStatus transitions a ticket's status and appends a status_change
// timeline row, atomically. actorName is snapshotted onto the comment the
// same way reporter identity is snapshotted onto the ticket.
func UpdateStatus(ctx context.Context, pool *pgxpool.Pool, id, newStatus, actorIdentityID, actorKind, actorName string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin status update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var oldStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM platform_feedback WHERE id = $1 FOR UPDATE`, id).Scan(&oldStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock ticket for status update: %w", err)
	}
	if oldStatus == newStatus {
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `UPDATE platform_feedback SET status = $1, updated_at = NOW() WHERE id = $2`, newStatus, id); err != nil {
		return fmt.Errorf("update ticket status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform_feedback_comments (feedback_id, author_identity_id, author_kind, author_name, event_type, old_status, new_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, nullable(actorIdentityID), actorKind, actorName, EventStatusChange, oldStatus, newStatus,
	); err != nil {
		return fmt.Errorf("insert status_change comment: %w", err)
	}
	return tx.Commit(ctx)
}

// AdminPatch is the set of admin-only fields PATCH /api/platform/feedback/{id}
// may update. A nil pointer means "leave unchanged".
type AdminPatch struct {
	Priority                *string
	AssignedAdminIdentityID *string // empty string clears the assignment
	InternalNotes           *string
}

// UpdateAdminFields applies a partial update of admin-only ticket fields.
func UpdateAdminFields(ctx context.Context, pool *pgxpool.Pool, id string, patch AdminPatch) error {
	var sets []string
	var args []any
	param := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if patch.Priority != nil {
		sets = append(sets, "priority = "+param(*patch.Priority))
	}
	if patch.AssignedAdminIdentityID != nil {
		sets = append(sets, "assigned_admin_identity_id = "+param(nullable(*patch.AssignedAdminIdentityID)))
	}
	if patch.InternalNotes != nil {
		sets = append(sets, "internal_notes = "+param(*patch.InternalNotes))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = NOW()")
	args = append(args, id)
	sql := "UPDATE platform_feedback SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(len(args))
	tag, err := pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("update ticket admin fields: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAllSeenForReporter records that the reporter has viewed their ticket
// list, clearing the unread badge for every one of their tickets at once —
// the panel calls this when the "My Tickets" tab opens, rather than
// tracking per-ticket reads.
func MarkAllSeenForReporter(ctx context.Context, pool *pgxpool.Pool, tenantID, reporterIdentityID string) error {
	if _, err := pool.Exec(ctx, `
		UPDATE platform_feedback SET reporter_last_seen_at = NOW()
		WHERE tenant_id = $1 AND reporter_identity_id = $2`,
		tenantID, reporterIdentityID); err != nil {
		return fmt.Errorf("mark tickets seen: %w", err)
	}
	return nil
}

// UnreadCountForReporter counts the reporter's own tickets that have a
// non-internal comment (a reply, or a status change) newer than the
// reporter's last-seen timestamp for that ticket.
func UnreadCountForReporter(ctx context.Context, pool *pgxpool.Pool, tenantID, reporterIdentityID string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT pf.id)
		FROM platform_feedback pf
		JOIN platform_feedback_comments c ON c.feedback_id = pf.id
		WHERE pf.tenant_id = $1 AND pf.reporter_identity_id = $2
		  AND c.is_internal = FALSE
		  AND c.created_at > pf.reporter_last_seen_at`,
		tenantID, reporterIdentityID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unread tickets: %w", err)
	}
	return count, nil
}

// AddCommentInput carries the fields needed to append a timeline comment.
type AddCommentInput struct {
	FeedbackID       string
	AuthorIdentityID string
	AuthorKind       string
	AuthorName       string
	Body             string
	IsInternal       bool
}

const commentColumns = `id, feedback_id, COALESCE(author_identity_id::text, ''), author_kind, author_name,
	body, is_internal, event_type, COALESCE(old_status, ''), COALESCE(new_status, ''), created_at`

func scanComment(row pgx.Row) (*Comment, error) {
	var c Comment
	if err := row.Scan(
		&c.ID, &c.FeedbackID, &c.AuthorIdentityID, &c.AuthorKind, &c.AuthorName,
		&c.Body, &c.IsInternal, &c.EventType, &c.OldStatus, &c.NewStatus, &c.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("scan feedback comment: %w", err)
	}
	return &c, nil
}

// AddComment appends a reply to a ticket's timeline and bumps updated_at.
func AddComment(ctx context.Context, pool *pgxpool.Pool, in AddCommentInput) (*Comment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin add comment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO platform_feedback_comments (feedback_id, author_identity_id, author_kind, author_name, body, is_internal, event_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+commentColumns,
		in.FeedbackID, nullable(in.AuthorIdentityID), in.AuthorKind, in.AuthorName, in.Body, in.IsInternal, EventComment,
	)
	c, err := scanComment(row)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_feedback SET updated_at = NOW() WHERE id = $1`, in.FeedbackID); err != nil {
		return nil, fmt.Errorf("touch ticket after comment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit add comment: %w", err)
	}
	return c, nil
}

// ListComments returns a ticket's timeline, oldest first. includeInternal
// must be false for every reporter-facing route — is_internal rows are
// admin-only notes.
func ListComments(ctx context.Context, pool *pgxpool.Pool, feedbackID string, includeInternal bool) ([]Comment, error) {
	sql := `SELECT ` + commentColumns + ` FROM platform_feedback_comments WHERE feedback_id = $1`
	if !includeInternal {
		sql += ` AND is_internal = FALSE`
	}
	sql += ` ORDER BY created_at ASC`

	rows, err := pool.Query(ctx, sql, feedbackID)
	if err != nil {
		return nil, fmt.Errorf("list feedback comments: %w", err)
	}
	defer rows.Close()
	comments := make([]Comment, 0)
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list feedback comments: %w", err)
	}
	return comments, nil
}

const attachmentColumns = `id, feedback_id, file_name, content_type, size_bytes, storage_key, checksum_sha256, created_at`

func scanAttachment(row pgx.Row) (*Attachment, error) {
	var a Attachment
	if err := row.Scan(&a.ID, &a.FeedbackID, &a.FileName, &a.ContentType, &a.SizeBytes, &a.StorageKey, &a.ChecksumSHA256, &a.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan feedback attachment: %w", err)
	}
	return &a, nil
}

// InsertAttachment records a confirmed upload's metadata.
func InsertAttachment(ctx context.Context, pool *pgxpool.Pool, a Attachment) (*Attachment, error) {
	row := pool.QueryRow(ctx, `
		INSERT INTO platform_feedback_attachments (feedback_id, file_name, content_type, size_bytes, storage_key, checksum_sha256)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+attachmentColumns,
		a.FeedbackID, a.FileName, a.ContentType, a.SizeBytes, a.StorageKey, a.ChecksumSHA256,
	)
	return scanAttachment(row)
}

// ListAttachments returns every attachment on a ticket.
func ListAttachments(ctx context.Context, pool *pgxpool.Pool, feedbackID string) ([]Attachment, error) {
	rows, err := pool.Query(ctx, `SELECT `+attachmentColumns+` FROM platform_feedback_attachments WHERE feedback_id = $1 ORDER BY created_at ASC`, feedbackID)
	if err != nil {
		return nil, fmt.Errorf("list feedback attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]Attachment, 0)
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list feedback attachments: %w", err)
	}
	return attachments, nil
}

// GetAttachment fetches one attachment, scoped to its ticket.
func GetAttachment(ctx context.Context, pool *pgxpool.Pool, feedbackID, attachmentID string) (*Attachment, error) {
	row := pool.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM platform_feedback_attachments WHERE id = $1 AND feedback_id = $2`, attachmentID, feedbackID)
	return scanAttachment(row)
}

// Stats summarizes ticket counts by status, for the platform-admin dashboard tiles.
type Stats struct {
	New        int `json:"new"`
	InProgress int `json:"inProgress"`
	Done       int `json:"done"`
	Cancelled  int `json:"cancelled"`
	Total      int `json:"total"`
}

// GetStats computes the current status breakdown across every tenant.
func GetStats(ctx context.Context, pool *pgxpool.Pool) (Stats, error) {
	rows, err := pool.Query(ctx, `SELECT status, COUNT(*) FROM platform_feedback GROUP BY status`)
	if err != nil {
		return Stats{}, fmt.Errorf("compute feedback stats: %w", err)
	}
	defer rows.Close()
	var s Stats
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return Stats{}, fmt.Errorf("scan feedback stats: %w", err)
		}
		switch status {
		case StatusNew:
			s.New = count
		case StatusInProgress:
			s.InProgress = count
		case StatusDone:
			s.Done = count
		case StatusCancelled:
			s.Cancelled = count
		}
		s.Total += count
	}
	if err := rows.Err(); err != nil {
		return Stats{}, fmt.Errorf("compute feedback stats: %w", err)
	}
	return s, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

// nullable converts an empty string to SQL NULL — used for optional UUID
// foreign keys (reporter_identity_id, assigned_admin_identity_id) where ""
// would otherwise fail UUID column validation.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// encodeCursor builds an opaque base64 cursor from the last row's sort key.
func encodeCursor(ts time.Time, id string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor.
func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return ts, parts[1], nil
}
