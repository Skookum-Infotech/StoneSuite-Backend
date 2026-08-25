package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestDocumentOps_RequiresAuth(t *testing.T) {
	h := NewDocumentOps(nil, map[string]DocumentLoader{})
	// Task 6 implements only GetPDF; Send/Sends are added and covered in Task 7.
	for name, fn := range map[string]http.HandlerFunc{
		"GetPDF": h.GetPDF,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/records/x/document/pdf", nil)
			req.SetPathValue("id", "x")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestSellerFromTenant_UsesDisplayName(t *testing.T) {
	// nil-safe: empty metadata yields a seller with just the display name.
	s := sellerFromTenantMeta("Acme Stone Co", "")
	assert.Equal(t, "Acme Stone Co", s.Name)
}

var _ = docpdf.Seller{}
