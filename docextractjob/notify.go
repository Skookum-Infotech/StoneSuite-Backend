package docextractjob

import (
	"context"
	"fmt"
	"log/slog"

	"stonesuite-backend/docextract"
	"stonesuite-backend/services"
)

// Notification constants for a finished extraction.
const (
	eventDocExtractReady = "document_extraction_ready"
	channelInApp         = "in_app"
	// reviewLinkFormat is the New Sales Order route with the handoff query param.
	reviewLinkFormat = "/sales/sales_order/new?fromDocument=%s"
	notifyTitleFmt   = "Document ready: %s"
	notifyBody       = "Review the extracted values and create your sales order."
)

// resourceByDocType maps a doc type to the RBAC resource the bell filters on.
var resourceByDocType = map[string]string{
	string(docextract.DocTypeSalesOrder):    "sales_order",
	string(docextract.DocTypePurchaseOrder): "purchase_order",
	string(docextract.DocTypeVendorBill):    "vendor_bill",
}

// NotifyFunc sends one notification (services.SendNotification).
type NotifyFunc func(ctx context.Context, req services.NotificationRequest) error

// notifyReady sends the in-app "document ready" notification to the uploader.
// Best-effort: a failure is logged and never fails the job.
func notifyReady(ctx context.Context, send NotifyFunc, tenantID string, ex *Extraction) {
	if send == nil {
		return
	}
	req := services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []services.RecipientTarget{{UserID: ex.OwnerIdentityID}},
		ActorUserID: ex.OwnerIdentityID,
		EventType:   eventDocExtractReady,
		Resource:    resourceByDocType[ex.DocType],
		ResourceID:  ex.ID,
		Title:       fmt.Sprintf(notifyTitleFmt, ex.FileName),
		Body:        notifyBody,
		Link:        fmt.Sprintf(reviewLinkFormat, ex.ID),
		Channels:    []string{channelInApp},
	}
	if err := send(ctx, req); err != nil {
		slog.WarnContext(ctx, "docextract: ready notification failed", "extractionId", ex.ID, "error", err)
	}
}
