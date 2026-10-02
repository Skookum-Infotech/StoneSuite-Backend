package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

const (
	testPDFType  = "application/pdf"
	testSHA      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testIdentity = "identity-a"
)

// fakeDocStore is an in-memory docExtractStore.
type fakeDocStore struct {
	rows       map[string]*docextractjob.Extraction
	today      int
	pending    int64
	created    []docextractjob.CreateParams
	discarded  []string
	queuedWith []string
	getErr     error
}

func (f *fakeDocStore) Create(_ context.Context, p docextractjob.CreateParams) (*docextractjob.Extraction, error) {
	f.created = append(f.created, p)
	ex := &docextractjob.Extraction{ID: "11111111-2222-4333-8444-555555555555", DocType: p.DocType, OwnerIdentityID: p.OwnerIdentityID,
		Status: docextractjob.StatusAwaitingUpload, FileName: p.FileName, ContentType: p.ContentType, SizeBytes: p.SizeBytes,
		StagingKey: docextractjob.StagingKey(p.TenantSlug, "11111111-2222-4333-8444-555555555555", p.Extension)}
	f.rows[ex.ID] = ex
	return ex, nil
}

func (f *fakeDocStore) Get(_ context.Context, id, owner string) (*docextractjob.Extraction, error) {
	if f.getErr != nil {
		return f.rows[id], f.getErr
	}
	ex, ok := f.rows[id]
	if !ok || ex.OwnerIdentityID != owner {
		return nil, docextractjob.ErrNotFound
	}
	return ex, nil
}

func (f *fakeDocStore) GetInternal(_ context.Context, id string) (*docextractjob.Extraction, error) {
	if ex, ok := f.rows[id]; ok {
		return ex, nil
	}
	return nil, docextractjob.ErrNotFound
}

func (f *fakeDocStore) ListReadyForOwner(context.Context, string) ([]docextractjob.Extraction, error) {
	return nil, nil
}

func (f *fakeDocStore) Discard(_ context.Context, id, _ string) (string, error) {
	f.discarded = append(f.discarded, id)
	return "key", nil
}

func (f *fakeDocStore) SetNotify(context.Context, string, string, bool) error { return nil }

func (f *fakeDocStore) MarkQueued(_ context.Context, id, _, _, jobID string) (string, bool, error) {
	f.queuedWith = append(f.queuedWith, jobID)
	f.rows[id].Status = docextractjob.StatusQueued
	return docextractjob.StatusQueued, true, nil
}

func (f *fakeDocStore) CompleteCAS(context.Context, string, string, string) error { return nil }

func (f *fakeDocStore) CountCreatedToday(context.Context) (int, error) { return f.today, nil }

func (f *fakeDocStore) SumPendingStagingBytes(context.Context) (int64, error) { return f.pending, nil }

// fakeDocObjects is an in-memory docExtractObjects.
type fakeDocObjects struct {
	headErr   error
	headSize  int64
	presigned []string
	deleted   []string
}

func (f *fakeDocObjects) PresignPut(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	f.presigned = append(f.presigned, key)
	return "https://r2.example/" + key, nil
}

func (f *fakeDocObjects) Head(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: f.headSize}, f.headErr
}

func (f *fakeDocObjects) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

// fakeDocQueue records Enqueue calls.
type fakeDocQueue struct{ idemKeys []string }

func (f *fakeDocQueue) Enqueue(_ context.Context, _ string, _ string, _ any, idem string) (string, error) {
	f.idemKeys = append(f.idemKeys, idem)
	return "job-1", nil
}

type docExtractEnv struct {
	h     *DocExtractOps
	store *fakeDocStore
	objs  *fakeDocObjects
	queue *fakeDocQueue
	allow bool // create permission
	ai    bool
}

func newDocExtractEnv(t *testing.T) *docExtractEnv {
	t.Helper()
	e := &docExtractEnv{
		store: &fakeDocStore{rows: map[string]*docextractjob.Extraction{}},
		objs:  &fakeDocObjects{headSize: 100},
		queue: &fakeDocQueue{},
		allow: true,
		ai:    true,
	}
	e.h = NewDocExtractOps(nil, e.queue, nil, nil, DocExtractSettings{
		Enabled: true, MaxBytes: 10 << 20, DailyCap: 3, StagingMaxBytes: 1000, StagingTTL: 24 * time.Hour, PresignTTL: 15 * time.Minute,
	})
	e.h.identify = func(*http.Request) (*docExtractCaller, int, string) {
		return &docExtractCaller{tenant: &tenancy.Tenant{ID: "t1", Slug: "acme"}, identityID: testIdentity, store: e.store}, 0, ""
	}
	e.h.check = func(_ context.Context, _ *docExtractCaller, _ authz.Resource, _ authz.Action) (authz.Decision, error) {
		return authz.Decision{Allowed: e.allow, Scope: authz.ScopeOwn}, nil
	}
	e.h.objectsFor = func(*tenancy.Tenant) docExtractObjects { return e.objs }
	e.h.aiAvailable = func(context.Context, *docExtractCaller) (bool, error) { return e.ai, nil }
	return e
}

func (e *docExtractEnv) do(handler http.HandlerFunc, method, target, body string, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

const createBody = `{"docType":"sales_order","fileName":"PO-4471.pdf","sizeBytes":500,"contentType":"application/pdf"}`

func TestDocExtract_FlagOffIs404(t *testing.T) {
	e := newDocExtractEnv(t)
	e.h.cfg.Enabled = false
	for name, h := range map[string]http.HandlerFunc{
		"create": e.h.Create, "list": e.h.List, "get": e.h.Get, "presign": e.h.Presign,
		"start": e.h.Start, "notify": e.h.Notify, "discard": e.h.Discard, "complete": e.h.Complete,
	} {
		rec := e.do(h, http.MethodPost, "/x?status=ready", createBody, "11111111-2222-4333-8444-555555555555")
		assert.Equal(t, http.StatusNotFound, rec.Code, name)
	}
	assert.Empty(t, e.store.created)
}

func TestDocExtract_Create_Gates(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		mutate   func(e *docExtractEnv)
		wantCode int
		wantBody string
	}{
		{"purchase order not supported yet", `{"docType":"purchase_order","fileName":"a.pdf","sizeBytes":5,"contentType":"application/pdf"}`, nil, 400, "not supported yet"},
		{"unknown doc type", `{"docType":"nope","fileName":"a.pdf","sizeBytes":5,"contentType":"application/pdf"}`, nil, 400, "Unknown document type"},
		{"bad json", `{`, nil, 400, "Invalid request body"},
		{"permission denied", createBody, func(e *docExtractEnv) { e.allow = false }, 403, "permission"},
		{"ai disabled", createBody, func(e *docExtractEnv) { e.ai = false }, 403, `"code":"ai_disabled"`},
		{"unsupported extension", `{"docType":"sales_order","fileName":"a.exe","sizeBytes":5,"contentType":"application/pdf"}`, nil, 400, "not supported"},
		{"content type mismatch", `{"docType":"sales_order","fileName":"a.pdf","sizeBytes":5,"contentType":"text/plain"}`, nil, 400, "does not match"},
		{"zero size", `{"docType":"sales_order","fileName":"a.pdf","sizeBytes":0,"contentType":"application/pdf"}`, nil, 400, "between"},
		{"too big", `{"docType":"sales_order","fileName":"a.pdf","sizeBytes":99999999,"contentType":"application/pdf"}`, nil, 400, "between"},
		{"daily cap", createBody, func(e *docExtractEnv) { e.store.today = 3 }, 429, `"code":"daily_limit"`},
		{"staging full", createBody, func(e *docExtractEnv) { e.store.pending = 800 }, 409, `"code":"storage_full"`},
		{"ok", createBody, nil, 201, `"uploadUrl"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDocExtractEnv(t)
			if tt.mutate != nil {
				tt.mutate(e)
			}
			rec := e.do(e.h.Create, http.MethodPost, "/api/tenant/document-extractions", tt.body, "")
			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			if tt.wantCode != http.StatusCreated {
				assert.Empty(t, e.store.created, "a refused request must not write a row")
				assert.Empty(t, e.objs.presigned)
			}
		})
	}
}

func TestDocExtract_Create_ServerGeneratesStagingKey(t *testing.T) {
	e := newDocExtractEnv(t)
	body := `{"docType":"sales_order","fileName":"../../evil/PO.pdf","sizeBytes":500,"contentType":"application/pdf","stagingKey":"other/key.pdf"}`
	rec := e.do(e.h.Create, http.MethodPost, "/x", body, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Len(t, e.objs.presigned, 1)
	assert.Equal(t, "acme/_staging/doc-extract/11111111-2222-4333-8444-555555555555.pdf", e.objs.presigned[0])
	assert.Equal(t, "PO.pdf", e.store.created[0].FileName, "file name is sanitized to its base name")
	assert.Equal(t, testIdentity, e.store.created[0].OwnerIdentityID)
}

func seedExtraction(e *docExtractEnv, status string) *docextractjob.Extraction {
	ex := &docextractjob.Extraction{ID: "11111111-2222-4333-8444-555555555555", DocType: "sales_order", OwnerIdentityID: testIdentity,
		Status: status, FileName: "PO.pdf", StagingKey: "acme/_staging/doc-extract/x.pdf", SHA256: "secretsha", SizeBytes: 5}
	e.store.rows[ex.ID] = ex
	return ex
}

func TestDocExtract_Start(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	body := `{"clientSha256":"` + testSHA + `"}`
	t.Run("invalid sha", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusAwaitingUpload)
		rec := e.do(e.h.Start, http.MethodPost, "/x", `{"clientSha256":"abc"}`, id)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Empty(t, e.queue.idemKeys)
	})
	t.Run("upload missing", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusAwaitingUpload)
		e.objs.headErr = storage.ErrNotFound
		rec := e.do(e.h.Start, http.MethodPost, "/x", body, id)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), `"code":"upload_missing"`)
		assert.Contains(t, rec.Body.String(), "The upload didn't finish")
		assert.Empty(t, e.queue.idemKeys)
	})
	t.Run("oversized object", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusAwaitingUpload)
		e.objs.headSize = 11 << 20
		rec := e.do(e.h.Start, http.MethodPost, "/x", body, id)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, []string{id}, e.store.discarded)
		assert.NotEmpty(t, e.objs.deleted)
	})
	t.Run("enqueues with the extraction id as idempotency key", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusAwaitingUpload)
		rec := e.do(e.h.Start, http.MethodPost, "/x", body, id)
		assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		assert.Equal(t, []string{id}, e.queue.idemKeys)
		assert.Contains(t, rec.Body.String(), `"status":"queued"`)
	})
	t.Run("already queued is idempotent", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusRunning)
		rec := e.do(e.h.Start, http.MethodPost, "/x", body, id)
		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"running"`)
		assert.Empty(t, e.queue.idemKeys)
	})
	t.Run("another owner's id is 404", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusAwaitingUpload).OwnerIdentityID = "someone-else"
		rec := e.do(e.h.Start, http.MethodPost, "/x", body, id)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Empty(t, e.queue.idemKeys)
	})
}

func TestDocExtract_Presign_OnlyWhileAwaitingUpload(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	e := newDocExtractEnv(t)
	ex := seedExtraction(e, docextractjob.StatusAwaitingUpload)
	rec := e.do(e.h.Presign, http.MethodPost, "/x", "", id)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{ex.StagingKey}, e.objs.presigned, "the same key is re-presigned")

	ex.Status = docextractjob.StatusReady
	rec = e.do(e.h.Presign, http.MethodPost, "/x", "", id)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestDocExtract_Get(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	t.Run("another owner is 404 and leaks nothing", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusReady).OwnerIdentityID = "someone-else"
		rec := e.do(e.h.Get, http.MethodGet, "/x", "", id)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.NotContains(t, rec.Body.String(), "PO.pdf")
	})
	t.Run("expired deletes the staging object", func(t *testing.T) {
		e := newDocExtractEnv(t)
		ex := seedExtraction(e, docextractjob.StatusReady)
		e.store.getErr = docextractjob.ErrExpired
		rec := e.do(e.h.Get, http.MethodGet, "/x", "", id)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), `"code":"expired"`)
		assert.Equal(t, []string{ex.StagingKey}, e.objs.deleted)
	})
	t.Run("response never carries the staging key, hash or owner", func(t *testing.T) {
		e := newDocExtractEnv(t)
		ex := seedExtraction(e, docextractjob.StatusFailed)
		ex.FailureCode = string(docextract.FailScanned)
		rec := e.do(e.h.Get, http.MethodGet, "/x", "", id)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		out := rec.Body.String()
		for _, secret := range []string{"_staging", "secretsha", testIdentity, "stagingKey", "sha256"} {
			assert.NotContains(t, out, secret)
		}
		var resp struct {
			Extraction docExtractView `json:"extraction"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "failed", resp.Extraction.Status)
		assert.Contains(t, resp.Extraction.FailureMessage, "PO.pdf")
	})
	t.Run("no create permission is 403", func(t *testing.T) {
		e := newDocExtractEnv(t)
		seedExtraction(e, docextractjob.StatusReady)
		e.allow = false
		rec := e.do(e.h.Get, http.MethodGet, "/x", "", id)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestDocExtract_List_RequiresReadyFilter(t *testing.T) {
	e := newDocExtractEnv(t)
	assert.Equal(t, http.StatusBadRequest, e.do(e.h.List, http.MethodGet, "/x", "", "").Code)
	assert.Equal(t, http.StatusOK, e.do(e.h.List, http.MethodGet, "/x?status=ready", "", "").Code)
}

func TestFailureMessage(t *testing.T) {
	codes := []docextract.FailureCode{
		docextract.FailCorrupt, docextract.FailScanned, docextract.FailUnreadableText, docextract.FailPasswordProtected,
		docextract.FailUnsupportedEncryption, docextract.FailPageCap, docextract.FailLineCap, docextract.FailTooLarge,
		docextract.FailUnsupportedType, docextract.FailEmpty,
	}
	all := []string{docextractjob.FailUploadMissing, docextractjob.FailUploadCorrupted, docextractjob.FailTransient, docextractjob.FailInternal}
	for _, c := range codes {
		all = append(all, string(c))
	}
	for _, code := range all {
		t.Run(code, func(t *testing.T) {
			_, ok := docExtractFailureMessages[code]
			assert.True(t, ok, "every worker failure code needs a user message")
			msg := failureMessage(code, "PO-4471.pdf")
			assert.Contains(t, msg, "PO-4471.pdf", "message names the file")
			assert.NotContains(t, msg, "%s")
		})
	}
	assert.Empty(t, failureMessage("", "a.pdf"))
	assert.Contains(t, failureMessage("brand_new_code", "a.pdf"), "a.pdf")
}

func TestFilterDuplicates(t *testing.T) {
	dups := []docextractjob.Duplicate{
		{Kind: docextractjob.DupSameFile, Reason: "same file"},
		{Kind: docextractjob.DupSamePO, RecordUUID: "so-1", Number: "SO-1", Reason: "dup", OwnerUserID: "mine"},
		{Kind: docextractjob.DupSamePO, RecordUUID: "so-2", Number: "SO-2", Status: "Closed", Reason: "dup SO-2", OwnerUserID: "theirs"},
		{Kind: docextractjob.DupQuoteRef, RecordUUID: "q-1", Number: "QUOT-1", Reason: "ref", OwnerUserID: "mine"},
		{Kind: "mystery", RecordUUID: "x", Number: "X", Reason: "?"},
	}
	canRead := func(res authz.Resource, owner string) bool {
		return owner == "mine" && res == authz.ResourceSalesOrder
	}
	got := filterDuplicates(dups, canRead)
	require.Len(t, got, len(dups), "findings are kept, only links are blanked")
	assert.Equal(t, "so-1", got[1].RecordUUID)
	assert.Equal(t, "SO-1", got[1].Number)
	for _, i := range []int{2, 3, 4} {
		assert.Empty(t, got[i].RecordUUID, "out-of-scope or unknown link %d is blanked", i)
		assert.Empty(t, got[i].Number)
		assert.Empty(t, got[i].Status, "status of an unreadable record is not disclosed")
		assert.Equal(t, docextractjob.GenericReason(got[i].Kind), got[i].Reason, "reason names no record")
	}
	for _, d := range got {
		assert.Empty(t, d.OwnerUserID, "the record owner is never sent to the client")
	}
	assert.NotNil(t, filterDuplicates(nil, canRead))
}

func TestValidSHA256Hex(t *testing.T) {
	assert.True(t, validSHA256Hex(testSHA))
	assert.False(t, validSHA256Hex(strings.Repeat("g", 64)))
	assert.False(t, validSHA256Hex(""))
}
