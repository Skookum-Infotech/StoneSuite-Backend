package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// ErrAttachmentNotFound is returned when a requested attachment does not exist
// for the given record in this tenant's database.
var ErrAttachmentNotFound = errors.New("attachment not found")

// maxFileNameLength is the maximum byte length of a sanitized display filename.
const maxFileNameLength = 255

// Attachment is a single file attached to a workflow record.
type Attachment struct {
	ID               string    `json:"id"`
	RecordID         string    `json:"recordId"`
	FileName         string    `json:"fileName"`
	ContentType      string    `json:"contentType"`
	SizeBytes        int64     `json:"sizeBytes"`
	StorageKey       string    `json:"storageKey"`
	ChecksumSHA256   string    `json:"checksumSha256"`
	Status           string    `json:"status"` // pending|clean|infected|failed
	UploadedByUserID string    `json:"uploadedByUserId,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
}

// SanitizeFileName strips path separators, null bytes, and other control
// characters from a user-supplied filename, then caps it at maxFileNameLength.
// The original extension is preserved when present. Returns "file" if the
// result would otherwise be empty.
func SanitizeFileName(s string) string {
	// Extract just the base name — eliminate any directory traversal attempt.
	s = filepath.Base(s)

	// Strip null bytes and control characters.
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || r == 0 {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())

	// Cap length.
	if len(out) > maxFileNameLength {
		ext := filepath.Ext(out)
		stem := out[:maxFileNameLength-len(ext)]
		out = stem + ext
	}
	if out == "" || out == "." {
		return "file"
	}
	return out
}

// GenerateStorageKey builds the R2 object key for a new attachment.
// Scheme: {tenantSlug}/{workflowKey}/{recordID}/{attachmentID}.{ext}
// The UUID-based name is never derived from the user-supplied filename,
// eliminating path-traversal and enumeration risks.
func GenerateStorageKey(tenantSlug, workflowKey, recordID, attachmentID, fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	// Restrict ext to safe characters only (letters/digits/dot).
	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			return r
		}
		return -1
	}, ext)
	return fmt.Sprintf("%s/%s/%s/%s%s", tenantSlug, workflowKey, recordID, attachmentID, clean)
}

// RecordExistsInTenant verifies that a workflow_record with the given id
// exists in this tenant's database. Used to guard the list endpoint so one
// tenant cannot probe another's record ids.
func RecordExistsInTenant(ctx context.Context, q Querier, recordID string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM workflow_records WHERE id = $1)`, recordID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check record exists: %w", err)
	}
	return exists, nil
}

// crmTypeToWorkflowKey maps lkp_record_type.record_type_code values (used by the
// v2 relational CRM design) to canonical workflow key strings for storage paths.
var crmTypeToWorkflowKey = map[string]string{
	"LEAD": "lead",
	"PROS": "prospect",
	"CUST": "customer",
}

// RecordAccessInfo bundles what the attachment layer's RBAC + IDOR guard
// needs for a record, regardless of which of the three storage models backs
// it (v1 workflow_records, v2 relational customer/CRM, or the relational
// sales_order). OwnerUserID/TeamID are tenant users.id/teams.id (empty when
// unowned or not applicable — customer and sales_order have no team column).
type RecordAccessInfo struct {
	WorkflowKey string
	OwnerUserID string
	TeamID      string
}

// ResolveRecordAccess resolves a record's workflow key AND ownership in one
// pass, checking workflow_records (v1) first, then the relational customer
// table (v2 CRM), then the relational sales_order table — mirroring the same
// branch order the old key-only RecordKeyForAttachment used. Returns
// ErrRecordNotFound when none match.
func ResolveRecordAccess(ctx context.Context, q Querier, recordID string) (RecordAccessInfo, error) {
	// v1: any generic JSONB workflow record (lead/prospect/customer under a v1
	// tenant, or a non-CRM workflow like invoice/quote/purchase_order).
	rec, err := GetRecord(ctx, q, recordID)
	if err == nil {
		var wfKey string
		if err := q.QueryRow(ctx, `SELECT key FROM workflows WHERE id = $1`, rec.WorkflowID).Scan(&wfKey); err != nil {
			return RecordAccessInfo{}, fmt.Errorf("lookup workflow key: %w", err)
		}
		return RecordAccessInfo{WorkflowKey: wfKey, OwnerUserID: rec.OwnerUserID, TeamID: rec.TeamID}, nil
	}
	if !errors.Is(err, ErrRecordNotFound) {
		return RecordAccessInfo{}, fmt.Errorf("lookup v1 record: %w", err)
	}

	// v2: customer table (record_type + owner, resolved employee -> users.id
	// the same way crmstore's relationalResolver/recordSelect does).
	var typeCode, ownerUserID string
	err = q.QueryRow(ctx, `
		SELECT rt.record_type_code, COALESCE(u.id::text,'')
		FROM customer c
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		LEFT JOIN employee e ON e.employee_id = c.customer_crm_owner_user_id
		LEFT JOIN users u ON u.id = e.employee_user_id
		WHERE c.customer_uuid = $1::uuid AND c.customer_deleted_at IS NULL`,
		recordID).Scan(&typeCode, &ownerUserID)
	if err == nil {
		key := strings.ToLower(typeCode)
		if k, ok := crmTypeToWorkflowKey[typeCode]; ok {
			key = k
		}
		return RecordAccessInfo{WorkflowKey: key, OwnerUserID: ownerUserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordAccessInfo{}, fmt.Errorf("lookup crm record: %w", err)
	}

	// sales_order: dedicated relational table (spec AD-1), owner resolved the
	// same way (employee -> users.id); no team column.
	var soOwnerUserID string
	err = q.QueryRow(ctx, `
		SELECT COALESCE(u.id::text,'')
		FROM sales_order so
		LEFT JOIN employee e ON e.employee_id = so.sales_order_owner_id
		LEFT JOIN users u ON u.id = e.employee_user_id
		WHERE so.sales_order_uuid = $1::uuid AND so.sales_order_deleted_at IS NULL`,
		recordID).Scan(&soOwnerUserID)
	if err == nil {
		return RecordAccessInfo{WorkflowKey: "sales_order", OwnerUserID: soOwnerUserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordAccessInfo{}, fmt.Errorf("lookup sales order: %w", err)
	}

	return RecordAccessInfo{}, ErrRecordNotFound
}

// RecordExistsAnywhere checks workflow_records (v1), customer (v2), and
// sales_order to confirm a record UUID belongs to this tenant.
func RecordExistsAnywhere(ctx context.Context, q Querier, recordID string) (bool, error) {
	_, err := ResolveRecordAccess(ctx, q, recordID)
	if errors.Is(err, ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CurrentAttachmentBytes returns the sum of size_bytes for all non-infected
// attachments on a record — used to enforce the per-record cumulative cap.
func CurrentAttachmentBytes(ctx context.Context, q Querier, recordID string) (int64, error) {
	var total int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(size_bytes),0) FROM workflow_record_attachments
		 WHERE record_id = $1::uuid AND status != 'infected'`, recordID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum attachment bytes (record=%s): %w", recordID, err)
	}
	return total, nil
}

// ListAttachments returns all attachments for a record in insertion order.
func ListAttachments(ctx context.Context, q Querier, recordID string) ([]Attachment, error) {
	rows, err := q.Query(ctx, `
		SELECT id, record_id, file_name, content_type, size_bytes,
		       storage_key, checksum_sha256, status,
		       COALESCE(uploaded_by_user_id::text,''), created_at
		  FROM workflow_record_attachments
		 WHERE record_id = $1
		 ORDER BY created_at`, recordID)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if out == nil {
		out = []Attachment{}
	}
	return out, rows.Err()
}

// GetAttachment loads a single attachment, verifying it belongs to recordID.
func GetAttachment(ctx context.Context, q Querier, recordID, attachmentID string) (*Attachment, error) {
	row := q.QueryRow(ctx, `
		SELECT id, record_id, file_name, content_type, size_bytes,
		       storage_key, checksum_sha256, status,
		       COALESCE(uploaded_by_user_id::text,''), created_at
		  FROM workflow_record_attachments
		 WHERE id = $1 AND record_id = $2`, attachmentID, recordID)
	a, err := scanAttachment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttachmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// InsertAttachment inserts a single attachment metadata row.
// For v1, status is set to 'clean' immediately.
// TODO(malware-scanning): change status to 'pending' here and let an async
// scanner set it to 'clean'/'infected'/'failed' — status is the extension point.
func InsertAttachment(ctx context.Context, q Querier, a Attachment) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO workflow_record_attachments
			(record_id, file_name, content_type, size_bytes,
			 storage_key, checksum_sha256, status, uploaded_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,'clean',$7)
		RETURNING id`,
		a.RecordID,
		a.FileName,
		a.ContentType,
		a.SizeBytes,
		a.StorageKey,
		a.ChecksumSHA256,
		nullIfEmpty(a.UploadedByUserID),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert attachment: %w", err)
	}
	return id, nil
}

// DeleteAttachmentRow removes the metadata row. The caller is responsible for
// deleting the R2 object first (best-effort).
func DeleteAttachmentRow(ctx context.Context, q Querier, recordID, attachmentID string) error {
	tag, err := q.Exec(ctx,
		`DELETE FROM workflow_record_attachments WHERE id = $1 AND record_id = $2`,
		attachmentID, recordID)
	if err != nil {
		return fmt.Errorf("delete attachment row: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAttachmentNotFound
	}
	return nil
}

// LogAudit writes one row to the tenant audit_logs table. It is best-effort:
// a failure here is logged by the caller but never returned as an error to the
// HTTP client — audit logging must never break the primary operation.
func LogAudit(ctx context.Context, q Querier, actorUserID, action, resource, resourceID string, details map[string]any) error {
	raw, _ := json.Marshal(details)
	_, err := q.Exec(ctx, `
		INSERT INTO audit_logs (actor_user_id, action, resource, resource_id, details)
		VALUES ($1,$2,$3,$4,$5::jsonb)`,
		nullIfEmpty(actorUserID), action, resource, resourceID, raw)
	if err != nil {
		return fmt.Errorf("log audit: %w", err)
	}
	return nil
}

// scanAttachment scans a single row from workflow_record_attachments.
func scanAttachment(row pgx.Row) (Attachment, error) {
	var a Attachment
	err := row.Scan(
		&a.ID, &a.RecordID, &a.FileName, &a.ContentType, &a.SizeBytes,
		&a.StorageKey, &a.ChecksumSHA256, &a.Status,
		&a.UploadedByUserID, &a.CreatedAt,
	)
	if err != nil {
		return a, err
	}
	return a, nil
}
