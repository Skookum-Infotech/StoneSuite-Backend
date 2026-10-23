//go:build dbtest

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/recordemail"
	"stonesuite-backend/tenancy"
)

// fakeObjectStore stands in for a tenant's R2 bucket: it records what was put and
// deleted, and can be told to fail the Nth put.
type fakeObjectStore struct {
	mu        sync.Mutex
	puts      []fakePut
	deleted   []string
	failPutAt int // 1-based; 0 never fails
}

type fakePut struct {
	key, contentType string
	size             int
}

func (f *fakeObjectStore) Put(_ context.Context, key, contentType string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPutAt > 0 && len(f.puts)+1 == f.failPutAt {
		return errors.New("bucket unavailable")
	}
	f.puts = append(f.puts, fakePut{key: key, contentType: contentType, size: len(body)})
	return nil
}

func (f *fakeObjectStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://files.test/" + key, nil
}

func (f *fakeObjectStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeObjectStore) putKeys() []string {
	keys := make([]string, 0, len(f.puts))
	for _, p := range f.puts {
		keys = append(keys, p.key)
	}
	return keys
}

// withStorage gives the fixture's ops a bucket (the fake) and the tenant a slug,
// so storage keys have the prefix real ones do.
func (f *inboundFixture) withStorage(store ObjectStore) {
	tenant := &tenancy.Tenant{ID: f.tenantID, Status: tenancy.StatusActive, Slug: "acme"}
	f.ops.tenants = fakeTenantLookup{tenant}
	f.ops.WithStorage(func(*tenancy.Tenant) ObjectStore { return store })
}

func (f *inboundFixture) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/internal/email/inbound/{ref}/attachments/{attachmentId}", f.ops.InboundAttachment)
	return mux
}

// putFile delivers one file the way notify does, signed at a fresh second.
func (f *inboundFixture) putFile(t *testing.T, ref, attachmentID, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	f.sent++
	path := "/api/internal/email/inbound/" + ref + "/attachments/" + attachmentID
	stamp := strconv.FormatInt(time.Now().Add(time.Duration(f.sent)*time.Second).Unix(), 10)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set(middleware.InternalTimestampHeader, stamp)
	req.Header.Set(middleware.InternalSignatureHeader, middleware.SignInternal(testInboundSecret, http.MethodPut, path, stamp, body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	f.mux().ServeHTTP(rec, req)
	return rec
}

type inboundAnswer struct {
	UploadRef string   `json:"uploadRef"`
	Pending   []string `json:"pending"`
}

func answerOf(t *testing.T, rec *httptest.ResponseRecorder) inboundAnswer {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var a inboundAnswer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &a))
	return a
}

func (f *inboundFixture) mailWithFiles(emailID string) recordemail.InboundMail {
	mail := f.reply(emailID, f.replyTo())
	mail.Attachments = []recordemail.InboundAttachment{
		{ID: "att-pdf", Filename: "plan.pdf", ContentType: "application/pdf", Size: 11},
		{ID: "att-exe", Filename: "setup.exe", ContentType: "application/octet-stream", Size: 5},
		{ID: "att-big", Filename: "scan.tiff", ContentType: "image/tiff", Size: recordemail.InboundMaxFileBytes + 1},
	}
	return mail
}

func statusOf(t *testing.T, f *inboundFixture, name string) recordemail.Attachment {
	t.Helper()
	msgs := f.stored(t)
	require.NotEmpty(t, msgs)
	for _, a := range msgs[0].Attachments {
		if a.FileName == name {
			return a
		}
	}
	t.Fatalf("no attachment %q on the message", name)
	return recordemail.Attachment{}
}

func TestInboundDB_AnswersWithTheFilesToCopyAndTheRefToSendThemUnder(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	f.withStorage(&fakeObjectStore{})

	first := answerOf(t, f.post(t, f.mailWithFiles("em_p_"+f.token)))

	assert.Equal(t, f.token, first.UploadRef)
	assert.Equal(t, []string{"att-pdf"}, first.Pending, "a blocked and an oversized file are not asked for")
	assert.Equal(t, recordemail.AttachmentBlocked, statusOf(t, f, "setup.exe").Status)
	assert.Equal(t, recordemail.AttachmentTooLarge, statusOf(t, f, "scan.tiff").Status)
	assert.Equal(t, 3, f.stored(t)[0].AttachmentCount)

	// The webhook retried: same answer, nothing duplicated.
	second := answerOf(t, f.post(t, f.mailWithFiles("em_p_"+f.token)))
	assert.Equal(t, first, second)
	assert.Len(t, f.stored(t), 1)
	assert.Len(t, f.stored(t)[0].Attachments, 3)
}

func TestInboundDB_MailWithNoFilesGetsAnEmptyPendingList(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	f.withStorage(&fakeObjectStore{})

	rec := f.post(t, f.reply("em_n_"+f.token, f.replyTo()))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"success":true,"uploadRef":"","pending":[]}`, rec.Body.String())
}

func TestInboundDB_AMailFromAnOlderNotifyWithOnlyACountStillFiles(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	f.withStorage(&fakeObjectStore{})
	mail := f.reply("em_o_"+f.token, f.replyTo())
	mail.AttachmentCount = 2 // no attachments list

	a := answerOf(t, f.post(t, mail))

	assert.Empty(t, a.Pending)
	msgs := f.stored(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, 2, msgs[0].AttachmentCount)
	assert.Empty(t, msgs[0].Attachments)
}

func TestInboundDB_WithoutFileStorageNothingIsAskedFor(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive) // no WithStorage

	a := answerOf(t, f.post(t, f.mailWithFiles("em_s_"+f.token)))

	assert.Empty(t, a.Pending)
	assert.Empty(t, a.UploadRef)
	assert.Equal(t, recordemail.AttachmentUnavailable, statusOf(t, f, "plan.pdf").Status)
}

func TestInboundAttachmentDB_CopiesTheFileIntoTheTenantsBucket(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	store := &fakeObjectStore{}
	f.withStorage(store)
	a := answerOf(t, f.post(t, f.mailWithFiles("em_u_"+f.token)))
	data := []byte("%PDF-1.7 hi")

	rec := f.putFile(t, a.UploadRef, "att-pdf", "application/pdf; charset=binary", data)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, store.puts, 1)
	put := store.puts[0]
	assert.True(t, strings.HasPrefix(put.key, "acme/sales_order/"+f.recordID+"/"), "key %q is under the tenant and record", put.key)
	assert.True(t, strings.HasSuffix(put.key, ".pdf"))
	assert.Equal(t, "application/pdf", put.contentType, "the provider's content type is cleaned, parameters dropped")
	assert.Equal(t, len(data), put.size)

	got := statusOf(t, f, "plan.pdf")
	assert.Equal(t, recordemail.AttachmentStored, got.Status)
	assert.Equal(t, int64(len(data)), got.SizeBytes, "the size is what was copied, not what the provider claimed")
	assert.Equal(t, put.key, got.StorageKey)

	// The same file delivered again (notify retried) is acknowledged and not copied twice.
	again := f.putFile(t, a.UploadRef, "att-pdf", "application/pdf", data)
	assert.Equal(t, http.StatusOK, again.Code)
	assert.Len(t, store.puts, 1)
}

func TestInboundAttachmentDB_AFileTheTenantCannotKeepIsSettledNotRetried(t *testing.T) {
	t.Run("no bucket", func(t *testing.T) {
		f := newInboundFixture(t, tenancy.StatusActive)
		f.withStorage(&fakeObjectStore{})
		a := answerOf(t, f.post(t, f.mailWithFiles("em_x1_"+f.token)))
		// The bucket disappears between the mail and the file.
		f.ops.WithStorage(func(*tenancy.Tenant) ObjectStore { return nil })

		rec := f.putFile(t, a.UploadRef, "att-pdf", "application/pdf", []byte("data"))

		assert.Equal(t, http.StatusOK, rec.Code, "final, so notify does not retry")
		assert.Equal(t, recordemail.AttachmentUnavailable, statusOf(t, f, "plan.pdf").Status)
	})

	t.Run("record over its attachment cap", func(t *testing.T) {
		f := newInboundFixture(t, tenancy.StatusActive)
		store := &fakeObjectStore{}
		f.withStorage(store)
		a := answerOf(t, f.post(t, f.mailWithFiles("em_x2_"+f.token)))
		_, err := f.tenantDB.Exec(context.Background(), `
			INSERT INTO record_email_attachment (message_id, record_id, file_name, size_bytes, storage_key, status)
			SELECT id, record_id, 'huge.zip', $2, 'k', 'stored' FROM record_email_message WHERE record_id = $1`,
			f.recordID, int64(recordemail.MaxRecordAttachmentBytes))
		require.NoError(t, err)

		rec := f.putFile(t, a.UploadRef, "att-pdf", "application/pdf", []byte("one more byte over"))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, store.puts, "nothing is written past the cap")
		assert.Equal(t, recordemail.AttachmentUnavailable, statusOf(t, f, "plan.pdf").Status)
	})
}

func TestInboundAttachmentDB_AskForARetryWhenTheBucketFails(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	f.withStorage(&fakeObjectStore{failPutAt: 1})
	a := answerOf(t, f.post(t, f.mailWithFiles("em_r_"+f.token)))

	rec := f.putFile(t, a.UploadRef, "att-pdf", "application/pdf", []byte("data"))

	assert.Equal(t, http.StatusBadGateway, rec.Code, "5xx: notify must retry")
	assert.Equal(t, recordemail.AttachmentPending, statusOf(t, f, "plan.pdf").Status, "still waiting for its copy")
}

func TestInboundAttachmentDB_IgnoresWhatIsNotOurs(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	store := &fakeObjectStore{}
	f.withStorage(store)
	a := answerOf(t, f.post(t, f.mailWithFiles("em_i_"+f.token)))

	unknownRef := f.putFile(t, "r-aaaaaaaaaaaaaaaaaaaa", "att-pdf", "application/pdf", []byte("x"))
	unknownFile := f.putFile(t, a.UploadRef, "att-nope", "application/pdf", []byte("x"))
	blockedFile := f.putFile(t, a.UploadRef, "att-exe", "application/octet-stream", []byte("MZ"))

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"unknown reply address": unknownRef, "unknown file": unknownFile, "a file already settled as blocked": blockedFile,
	} {
		assert.Equal(t, http.StatusOK, rec.Code, name)
	}
	assert.Empty(t, store.puts, "none of them is written anywhere")
}

func TestInboundAttachmentDB_AFileFromAnotherRecordsMailIsNotFound(t *testing.T) {
	a := newInboundFixture(t, tenancy.StatusActive)
	a.withStorage(&fakeObjectStore{})
	answerOf(t, a.post(t, a.mailWithFiles("em_a_"+a.token)))

	// A second record, with its own reply address, tries to fill the first one's file.
	b := newInboundFixture(t, tenancy.StatusActive)
	store := &fakeObjectStore{}
	b.withStorage(store)

	rec := b.putFile(t, b.token, "att-pdf", "application/pdf", []byte("x"))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, store.puts)
	assert.Equal(t, recordemail.AttachmentPending, statusOf(t, a, "plan.pdf").Status, "the first record's file is untouched")
}

// keepReplyFiles runs against the tenant database (for the size cap) and a fake bucket.

func replyFiles(names ...string) []recordemail.ReplyFile {
	files := make([]recordemail.ReplyFile, 0, len(names))
	for _, n := range names {
		ct, _ := recordemail.ReplyContentType(n)
		files = append(files, recordemail.ReplyFile{Name: n, ContentType: ct, Data: []byte("data of " + n)})
	}
	return files
}

func TestKeepReplyFilesDB(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	ctx := context.Background()
	tenant := &tenancy.Tenant{ID: f.tenantID, Slug: "acme", Status: tenancy.StatusActive}

	t.Run("copies each file under the tenant, workflow and record", func(t *testing.T) {
		store := &fakeObjectStore{}
		f.withStorage(store)

		atts, discard, status, problem := f.ops.keepReplyFiles(ctx, f.tenantDB, tenant, "quote", f.recordID, "msg-1",
			replyFiles("Plan.PDF", "kitchen.jpg"))

		require.Zero(t, status, problem)
		require.Len(t, atts, 2)
		for _, a := range atts {
			assert.Equal(t, "msg-1", a.MessageID)
			assert.Equal(t, f.recordID, a.RecordID)
			assert.Equal(t, recordemail.AttachmentStored, a.Status)
			assert.True(t, strings.HasPrefix(a.StorageKey, "acme/quote/"+f.recordID+"/"), a.StorageKey)
		}
		assert.Equal(t, "application/pdf", atts[0].ContentType)
		assert.Equal(t, "image/jpeg", atts[1].ContentType)
		assert.Empty(t, store.deleted)

		discard()
		assert.ElementsMatch(t, store.putKeys(), store.deleted, "discard removes exactly what was copied")
	})

	t.Run("a failed copy removes the ones before it", func(t *testing.T) {
		store := &fakeObjectStore{failPutAt: 2}
		f.withStorage(store)

		atts, _, status, _ := f.ops.keepReplyFiles(ctx, f.tenantDB, tenant, "quote", f.recordID, "msg-2",
			replyFiles("a.pdf", "b.pdf", "c.pdf"))

		assert.Equal(t, http.StatusBadGateway, status)
		assert.Nil(t, atts)
		assert.Len(t, store.puts, 1)
		assert.Equal(t, store.putKeys(), store.deleted, "the first file is not left behind")
	})

	t.Run("no bucket is a 503", func(t *testing.T) {
		f.ops.WithStorage(func(*tenancy.Tenant) ObjectStore { return nil })

		_, _, status, problem := f.ops.keepReplyFiles(ctx, f.tenantDB, tenant, "quote", f.recordID, "msg-3", replyFiles("a.pdf"))

		assert.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, noStorageMessage, problem)
	})

	t.Run("no files needs no bucket and does nothing", func(t *testing.T) {
		f.ops.WithStorage(func(*tenancy.Tenant) ObjectStore { return nil })

		atts, discard, status, _ := f.ops.keepReplyFiles(ctx, f.tenantDB, tenant, "quote", f.recordID, "msg-4", nil)

		assert.Zero(t, status)
		assert.Empty(t, atts)
		discard() // must be safe to call
	})
}

func TestKeepReplyFilesDB_StopsAtTheRecordsStorageCap(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	ctx := context.Background()
	store := &fakeObjectStore{}
	f.withStorage(store)
	tenant := &tenancy.Tenant{ID: f.tenantID, Slug: "acme", Status: tenancy.StatusActive}
	var msgID string
	require.NoError(t, f.tenantDB.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&msgID))
	require.NoError(t, recordemail.InsertWithFiles(ctx, f.tenantDB,
		recordemail.Message{ID: msgID, RecordID: f.recordID, WorkflowKey: "sales_order", Direction: recordemail.DirectionOut, Body: "x"},
		[]recordemail.Attachment{{
			MessageID: msgID, RecordID: f.recordID, FileName: "huge.zip", SizeBytes: recordemail.MaxRecordAttachmentBytes,
			Status: recordemail.AttachmentStored, StorageKey: "k",
		}}))

	_, _, status, _ := f.ops.keepReplyFiles(ctx, f.tenantDB, tenant, "sales_order", f.recordID, "msg-cap", replyFiles("a.pdf"))

	assert.Equal(t, http.StatusConflict, status)
	assert.Empty(t, store.puts)
}

func TestSaveInternalNoteDB_KeepsItsFiles(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	ctx := context.Background()
	var msgID, identityID string
	require.NoError(t, f.tenantDB.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&msgID))
	require.NoError(t, f.tenantDB.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&identityID))
	atts := []recordemail.Attachment{{
		MessageID: msgID, RecordID: f.recordID, FileName: "site.jpg", ContentType: "image/jpeg", SizeBytes: 9,
		Status: recordemail.AttachmentStored, StorageKey: "acme/sales_order/" + f.recordID + "/x.jpg",
	}}

	rec := httptest.NewRecorder()
	ok := saveInternalNote(rec, newNoteRequest(f.recordID), f.tenantDB, "sales_order", f.recordID, identityID, "Site photo.", msgID, atts)

	require.True(t, ok)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	msgs := f.stored(t)
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].Internal)
	assert.Equal(t, msgID, msgs[0].ID)
	assert.Equal(t, 1, msgs[0].AttachmentCount)
	require.Len(t, msgs[0].Attachments, 1)
	assert.Equal(t, "site.jpg", msgs[0].Attachments[0].FileName)
}
