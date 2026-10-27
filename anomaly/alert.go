package anomaly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"stonesuite-backend/services"
)

const (
	// AnomaliesLink is the app route the bell and email open for a finding.
	AnomaliesLink = "/anomalies"

	eventTypePrefix  = "anomaly."
	idempotencyScope = "anomaly:"
	channelEmail     = "email"
	// maxAlertsPerRun bounds how many findings one tick announces, so a first
	// scan of a big tenant can't make the (synchronous) tick request run long.
	// The rest stay pending and go out on the next tick.
	maxAlertsPerRun = 50
)

// Sender delivers one notification; automation.NotifySender satisfies it.
type Sender interface {
	Send(ctx context.Context, req services.NotificationRequest) (services.NotificationResult, error)
}

// AlertResult counts what one alert pass did for one tenant.
type AlertResult struct {
	Sent int
	// Unaddressed counts findings with nobody left to tell (every recipient
	// deactivated, or the only candidate was the person involved); they are
	// marked announced so they are not retried forever.
	Unaddressed int
	// PageOnly counts info-level findings, which appear on the Anomalies page but
	// are never sent to the bell or email; they are marked announced at once.
	PageOnly int
	Failed   int
}

type pendingAlert struct {
	id         int
	uuid       string
	ruleKey    string
	severity   string
	resource   string
	recordUUID string
	title      string
	detail     string
	detectedAt time.Time
}

// IdempotencyKey is the key a finding's notification carries, so a retry after
// a timeout can't notify twice. Always 64 hex bytes (notify caps keys at 128).
func IdempotencyKey(anomalyUUID string) string {
	sum := sha256.Sum256([]byte(idempotencyScope + anomalyUUID))
	return hex.EncodeToString(sum[:])
}

// buildNotification builds the notify request for one finding. Critical
// findings are emailed as well as shown in the bell; the rest are bell-only so
// a flood of warnings can't bury the ones that matter.
func buildNotification(tenantID string, a pendingAlert, to []services.RecipientTarget) services.NotificationRequest {
	resourceID := a.recordUUID
	if resourceID == "" {
		resourceID = a.uuid
	}
	req := services.NotificationRequest{
		TenantID:       tenantID,
		Recipients:     to,
		EventType:      eventTypePrefix + a.ruleKey,
		Resource:       a.resource,
		ResourceID:     resourceID,
		Title:          a.title,
		Body:           a.detail,
		Link:           AnomaliesLink,
		IdempotencyKey: IdempotencyKey(a.uuid),
	}
	if Severity(a.severity) == SeverityCritical {
		req.Channels = []string{channelEmail}
		req.Email = buildEmail(a)
	}
	return req
}

// Alert announces every finding not yet announced. A failed send leaves the
// finding pending, so the next tick retries it with the same idempotency key.
func Alert(ctx context.Context, db DB, send Sender, tenantID string) (AlertResult, error) {
	var (
		res  AlertResult
		errs []error
	)
	pending, err := loadPending(ctx, db)
	if err != nil {
		return res, err
	}
	for _, a := range pending {
		if Severity(a.severity) == SeverityInfo {
			if err := markAnnounced(ctx, db, a.id); err != nil {
				errs = append(errs, err)
				continue
			}
			res.PageOnly++
			continue
		}
		to, err := loadRecipients(ctx, db, a.id)
		if err != nil {
			res.Failed++
			errs = append(errs, err)
			continue
		}
		if len(to) == 0 {
			slog.WarnContext(ctx, "anomaly has no active recipient, marking announced", "anomaly_id", a.uuid, "rule", a.ruleKey)
			if err := markAnnounced(ctx, db, a.id); err != nil {
				errs = append(errs, err)
				continue
			}
			res.Unaddressed++
			continue
		}
		if _, err := send.Send(ctx, buildNotification(tenantID, a, to)); err != nil {
			res.Failed++
			errs = append(errs, fmt.Errorf("send alert for anomaly %s: %w", a.uuid, err))
			continue
		}
		if err := markAnnounced(ctx, db, a.id); err != nil {
			errs = append(errs, err)
			continue
		}
		res.Sent++
	}
	return res, errors.Join(errs...)
}

func loadPending(ctx context.Context, q Querier) ([]pendingAlert, error) {
	rows, err := q.Query(ctx, `
		SELECT anomaly_id, anomaly_uuid::text, anomaly_rule_key, anomaly_severity, anomaly_resource,
		       COALESCE(anomaly_record_uuid::text, ''), anomaly_title, anomaly_detail, anomaly_detected_at
		FROM anomaly
		WHERE anomaly_notified_at IS NULL
		ORDER BY anomaly_id
		LIMIT $1`, maxAlertsPerRun)
	if err != nil {
		return nil, fmt.Errorf("load pending anomaly alerts: %w", err)
	}
	defer rows.Close()
	var out []pendingAlert
	for rows.Next() {
		var a pendingAlert
		if err := rows.Scan(&a.id, &a.uuid, &a.ruleKey, &a.severity, &a.resource, &a.recordUUID, &a.title, &a.detail, &a.detectedAt); err != nil {
			return nil, fmt.Errorf("scan pending anomaly alert: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending anomaly alerts: %w", err)
	}
	return out, nil
}

// loadRecipients returns the finding's still-active recipients as notify targets.
func loadRecipients(ctx context.Context, q Querier, anomalyID int) ([]services.RecipientTarget, error) {
	rows, err := q.Query(ctx, `
		SELECT u.identity_id::text, u.email, u.full_name
		FROM anomaly_recipient r
		JOIN users u ON u.identity_id = r.recipient_identity_id AND u.status = 'active'
		WHERE r.anomaly_id = $1
		ORDER BY u.identity_id`, anomalyID)
	if err != nil {
		return nil, fmt.Errorf("load recipients for anomaly %d: %w", anomalyID, err)
	}
	defer rows.Close()
	var out []services.RecipientTarget
	for rows.Next() {
		var t services.RecipientTarget
		if err := rows.Scan(&t.UserID, &t.Email, &t.Name); err != nil {
			return nil, fmt.Errorf("scan recipient for anomaly %d: %w", anomalyID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipients for anomaly %d: %w", anomalyID, err)
	}
	return out, nil
}

func markAnnounced(ctx context.Context, q Querier, anomalyID int) error {
	if _, err := q.Exec(ctx, `UPDATE anomaly SET anomaly_notified_at = CURRENT_TIMESTAMP WHERE anomaly_id = $1`, anomalyID); err != nil {
		return fmt.Errorf("mark anomaly %d announced: %w", anomalyID, err)
	}
	return nil
}
