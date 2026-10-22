package automation

import (
	"context"
	"fmt"

	"stonesuite-backend/services"
)

// Sender delivers one outbound notification.
type Sender interface {
	Send(ctx context.Context, req services.NotificationRequest) (services.NotificationResult, error)
}

// NotifySender is the production Sender, backed by stonesuite-notify.
type NotifySender struct{}

// Send forwards the request through services.SendNotificationWithResult.
func (NotifySender) Send(ctx context.Context, req services.NotificationRequest) (services.NotificationResult, error) {
	res, err := services.SendNotificationWithResult(ctx, req)
	if err != nil {
		return res, fmt.Errorf("send notification: %w", err)
	}
	return res, nil
}
