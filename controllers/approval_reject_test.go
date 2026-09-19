package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/approvalchain"
)

// TestRejectHandlers_RequireAuth: every Sales/Purchases module that lets an
// approver reject a record must sit behind the same auth as the rest of its
// handlers -- an unauthenticated call is a 401, never a 5xx or a panic.
func TestRejectHandlers_RequireAuth(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"estimate":       NewEstimateOps().Reject,
		"quote":          NewQuoteOps().Reject,
		"sales order":    NewSalesOrderOps(nil).Reject,
		"invoice":        NewInvoiceOps(nil).Reject,
		"purchase order": NewPurchaseOrderOps().Reject,
		"requisition":    NewRequisitionOps().Reject,
		"vendor bill":    NewVendorBillOps().Reject,
		"vendor payment": NewVendorPaymentOps().Reject,
		"payment":        NewPaymentOps(nil).Reject,
		"credit memo":    NewCreditMemoOps().Reject,
		"refund":         NewRefundOps(nil).Reject,
		"vendor credit":  NewVendorCreditOps().Reject,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/reject", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s Reject must require auth", name)
		})
	}
}

// TestRejectErrors_MapToHTTPStatusInEveryModule: the errors approvalchain adds
// for a rejection reach the client as the right 4xx from every module's *Fail
// helper, not a generic 500.
func TestRejectErrors_MapToHTTPStatusInEveryModule(t *testing.T) {
	fails := map[string]func(http.ResponseWriter, error, string){
		"estimate":       estimateFail,
		"quote":          quoteFail,
		"sales order":    soFail,
		"invoice":        invoiceFail,
		"purchase order": poFail,
		"requisition":    reqnFail,
		"vendor bill":    vbFail,
		"vendor payment": vendorPaymentFail,
		"payment":        paymentFail,
		"credit memo":    creditMemoFail,
		"refund":         refundFail,
		"vendor credit":  vendorCreditFail,
	}
	errs := []struct {
		name string
		err  error
		want int
	}{
		{"reason required", approvalchain.ErrReasonRequired, http.StatusBadRequest},
		{"reason too long", approvalchain.ErrReasonTooLong, http.StatusBadRequest},
		{"already rejected", approvalchain.ErrAlreadyRejected, http.StatusConflict},
		{"reject not supported", approvalchain.ErrRejectNotSupported, http.StatusConflict},
		{"wrapped already rejected", fmt.Errorf("approve: %w", approvalchain.ErrAlreadyRejected), http.StatusConflict},
	}
	for module, fail := range fails {
		for _, tc := range errs {
			t.Run(module+"/"+tc.name, func(t *testing.T) {
				rr := httptest.NewRecorder()
				fail(rr, tc.err, "server error")
				assert.Equal(t, tc.want, rr.Code)
			})
		}
	}
}
