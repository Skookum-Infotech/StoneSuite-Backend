package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These mirror ai_conversations_test.go's *_UnauthenticatedRejected pattern:
// no pool/tenant context is available in a unit test, so every handler must
// reject before ever touching one — proven by nil ImportOps dependencies
// (r2, queue) never being dereferenced.

func TestImportOpsPresign_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	body := strings.NewReader(`{"workflowKey":"lead","fileName":"f.csv","contentType":"text/csv","sizeBytes":10}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/import/presign", body)
	w := httptest.NewRecorder()

	h.Presign(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (R2 not configured must be checked before auth)", w.Code)
	}
}

func TestImportOpsCreateJob_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	body := strings.NewReader(`{"workflowKey":"lead","storageKey":"k","fileName":"f.csv"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/import/jobs", body)
	w := httptest.NewRecorder()

	h.CreateJob(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestImportOpsListJobs_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/import/jobs?workflowKey=lead", nil)
	w := httptest.NewRecorder()

	h.ListJobs(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestImportOpsListJobs_MissingWorkflowKeyRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/import/jobs", nil)
	w := httptest.NewRecorder()

	h.ListJobs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (workflowKey is required, checked before auth)", w.Code)
	}
}

func TestImportOpsGetJob_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/import/jobs/x", nil)
	w := httptest.NewRecorder()

	h.GetJob(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestImportOpsListRows_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/import/jobs/x/rows", nil)
	w := httptest.NewRecorder()

	h.ListRows(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestImportOpsUpdateRow_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	body := strings.NewReader(`{"skip":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/tenant/import/jobs/x/rows/y", body)
	w := httptest.NewRecorder()

	h.UpdateRow(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestImportOpsCommit_UnauthenticatedRejected(t *testing.T) {
	h := NewImportOps(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/import/jobs/x/commit", nil)
	w := httptest.NewRecorder()

	h.Commit(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
