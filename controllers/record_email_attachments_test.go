package controllers

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/recordemail"
	"stonesuite-backend/tenancy"
)

func TestCleanContentType(t *testing.T) {
	tests := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"a plain type is kept", []string{"application/pdf"}, "application/pdf"},
		{"parameters are dropped", []string{"text/plain; charset=utf-8"}, "text/plain"},
		{"lower-cased", []string{"IMAGE/PNG"}, "image/png"},
		{"the first usable candidate wins", []string{"", "not a type", "image/jpeg", "image/png"}, "image/jpeg"},
		{"nothing usable falls back", []string{"", "garbage"}, "application/octet-stream"},
		{"a header-injection attempt falls back", []string{"text/plain\r\nX-Evil: 1"}, "application/octet-stream"},
		{"no candidates", nil, "application/octet-stream"},
		{"absurdly long", []string{"application/" + strings.Repeat("x", 200)}, "application/octet-stream"},
	}
	for _, tc := range tests {
		if got := cleanContentType(tc.candidates...); got != tc.want {
			t.Errorf("%s: cleanContentType(%q) = %q, want %q", tc.name, tc.candidates, got, tc.want)
		}
	}
}

func bodyRequest(body []byte, declared int64) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
	req.ContentLength = declared
	return req
}

func TestReadBoundedBody(t *testing.T) {
	data := []byte("hello world")

	t.Run("a declared length is read exactly", func(t *testing.T) {
		got, err := readBoundedBody(httptest.NewRecorder(), bodyRequest(data, int64(len(data))), 1024)
		if err != nil || string(got) != "hello world" {
			t.Fatalf("got %q, %v", got, err)
		}
		if cap(got) != len(data) {
			t.Errorf("cap = %d, want %d: the buffer must be sized once, not grown", cap(got), len(data))
		}
	})

	t.Run("an undeclared length is read up to the limit", func(t *testing.T) {
		got, err := readBoundedBody(httptest.NewRecorder(), bodyRequest(data, -1), 1024)
		if err != nil || string(got) != "hello world" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("a declared length over the limit is refused before reading", func(t *testing.T) {
		_, err := readBoundedBody(httptest.NewRecorder(), bodyRequest(data, 2048), 1024)
		var tooBig *http.MaxBytesError
		if !errors.As(err, &tooBig) {
			t.Fatalf("err = %v, want a *http.MaxBytesError", err)
		}
	})

	t.Run("an undeclared body over the limit is refused", func(t *testing.T) {
		_, err := readBoundedBody(httptest.NewRecorder(), bodyRequest(bytes.Repeat([]byte("x"), 2000), -1), 1024)
		var tooBig *http.MaxBytesError
		if !errors.As(err, &tooBig) {
			t.Fatalf("err = %v, want a *http.MaxBytesError", err)
		}
	})

	t.Run("a body shorter than declared is an error", func(t *testing.T) {
		if _, err := readBoundedBody(httptest.NewRecorder(), bodyRequest(data, 100), 1024); err == nil {
			t.Fatal("want an error for a truncated body")
		}
	})
}

type formFile struct{ field, name, content string }

func multipartReply(t *testing.T, fields map[string]string, files []formFile) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	for _, f := range files {
		part, err := mw.CreateFormFile(f.field, f.name)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := io.WriteString(part, f.content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/records/r/emails", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestReadReply_JSONHasNoFiles(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"message":"Hello","internalOnly":true}`))
	req.Header.Set("Content-Type", "application/json")

	in, status, problem := readReply(httptest.NewRecorder(), req)

	if status != 0 || problem != "" {
		t.Fatalf("status = %d, problem = %q", status, problem)
	}
	if in.req.Message != "Hello" || !in.req.InternalOnly || len(in.files) != 0 {
		t.Fatalf("in = %+v", in)
	}
}

func TestReadReply_JSONThatIsNotJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{nope`))
	_, status, _ := readReply(httptest.NewRecorder(), req)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestReadReply_MultipartCarriesTextFlagAndFiles(t *testing.T) {
	req := multipartReply(t,
		map[string]string{"message": "See attached.", "internalOnly": "false"},
		[]formFile{
			{"files", "Quote 12.pdf", "%PDF-1"},
			{"files", "../../photos/Kitchen.JPG", "jpegbytes"},
			{"files", "plan.exe", "MZ"},
		})

	in, status, problem := readReply(httptest.NewRecorder(), req)

	if status != 0 {
		t.Fatalf("status = %d (%s)", status, problem)
	}
	if in.req.Message != "See attached." || in.req.InternalOnly {
		t.Fatalf("req = %+v", in.req)
	}
	if len(in.files) != 3 {
		t.Fatalf("files = %d, want 3", len(in.files))
	}
	if f := in.files[0]; f.Name != "Quote 12.pdf" || f.ContentType != "application/pdf" || string(f.Data) != "%PDF-1" {
		t.Errorf("first file = %+v", f)
	}
	if f := in.files[1]; f.Name != "Kitchen.JPG" || f.ContentType != "image/jpeg" {
		t.Errorf("second file = %+v, want no path and the type from its extension", f)
	}
	// Not rejected here: an unlisted type has no content type, and validation refuses it.
	if f := in.files[2]; f.ContentType != "" {
		t.Errorf("third file content type = %q, want none for an unlisted type", f.ContentType)
	}
	if err := recordemail.ValidateReplyFiles(in.files); err == nil {
		t.Error("ValidateReplyFiles accepted a .exe")
	}
}

func TestReadReply_InternalFlagIsOnlyTrueForTrue(t *testing.T) {
	for value, want := range map[string]bool{"true": true, "false": false, "": false, "1": false, "TRUE": false} {
		in, status, _ := readReply(httptest.NewRecorder(),
			multipartReply(t, map[string]string{"message": "x", "internalOnly": value}, nil))
		if status != 0 || in.req.InternalOnly != want {
			t.Errorf("internalOnly=%q: status %d, InternalOnly %v, want %v", value, status, in.req.InternalOnly, want)
		}
	}
}

func TestReadReply_TooManyFiles(t *testing.T) {
	var files []formFile
	for i := 0; i <= recordemail.ReplyMaxFiles; i++ {
		files = append(files, formFile{"files", "f" + strconv.Itoa(i) + ".pdf", "x"})
	}
	_, status, problem := readReply(httptest.NewRecorder(), multipartReply(t, map[string]string{"message": "x"}, files))
	if status != http.StatusBadRequest || problem != "Attach at most 5 files." {
		t.Fatalf("status = %d, problem = %q", status, problem)
	}
}

func TestReadReply_RequestOverTheLimitIsRefused(t *testing.T) {
	big := strings.Repeat("x", recordemail.ReplyMaxTotalBytes+replyRequestOverheadBytes)
	_, status, problem := readReply(httptest.NewRecorder(),
		multipartReply(t, map[string]string{"message": "x"}, []formFile{{"files", "big.pdf", big}}))
	if status != http.StatusRequestEntityTooLarge || problem != recordemail.ReplyTooLargeMessage {
		t.Fatalf("status = %d, problem = %q", status, problem)
	}
}

func TestReplyFilesProblem(t *testing.T) {
	if got := replyFilesProblem(&recordemail.ReplyFilesError{Msg: "a.pdf is empty."}); got != "a.pdf is empty." {
		t.Errorf("typed error: %q", got)
	}
	if got := replyFilesProblem(errors.New("boom")); got != "The attachments could not be accepted." {
		t.Errorf("other error: %q", got)
	}
}

func TestNotifyAttachments(t *testing.T) {
	if got := notifyAttachments(nil); len(got) != 0 {
		t.Fatalf("no files = %v, want none", got)
	}
	got := notifyAttachments([]recordemail.ReplyFile{
		{Name: "a.pdf", ContentType: "application/pdf", Data: []byte("A")},
		{Name: "b.png", ContentType: "image/png", Data: []byte("BB")},
	})
	if len(got) != 2 || got[0].FileName != "a.pdf" || string(got[1].Content) != "BB" || got[1].ContentType != "image/png" {
		t.Fatalf("got %+v", got)
	}
}

func TestR2TenantStorage_NoClientMeansNoStorage(t *testing.T) {
	// A nil *storage.Client inside the interface would not compare equal to nil.
	if got := R2TenantStorage(nil)(&tenancy.Tenant{R2Bucket: "tenant-bucket"}); got != nil {
		t.Fatalf("got %#v, want a nil ObjectStore", got)
	}
}

func signedAttachmentPut(t *testing.T, secret string, body []byte, declared int64) *http.Request {
	t.Helper()
	const path = "/api/internal/email/inbound/r-abcdefghijklmnopqrst/attachments/att-1"
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.ContentLength = declared
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(middleware.InternalTimestampHeader, stamp)
	req.Header.Set(middleware.InternalSignatureHeader, middleware.SignInternal(secret, http.MethodPut, path, stamp, body))
	return req
}

// attachmentOps is an enabled RecordEmailOps with the route's dependencies wired
// but no database: a request must be refused before it reaches one.
func attachmentOps() *RecordEmailOps {
	return NewRecordEmailOps(recordemail.NewAddressStore(nil), testInboundDomain).
		WithInbound(testInboundSecret, unreachableTenants{}, unreachablePool).
		WithStorage(func(*tenancy.Tenant) ObjectStore { return nil })
}

func TestInboundAttachment_RejectsWhatItCannotTrust(t *testing.T) {
	body := []byte("%PDF-1.7 ...")
	tests := []struct {
		name string
		ops  func() *RecordEmailOps
		req  func() *http.Request
		want int
	}{
		{
			name: "feature off",
			ops:  func() *RecordEmailOps { return NewRecordEmailOps(nil, "") },
			req:  func() *http.Request { return signedAttachmentPut(t, testInboundSecret, body, int64(len(body))) },
			want: http.StatusServiceUnavailable,
		},
		{
			name: "no file storage wired",
			ops:  func() *RecordEmailOps { return inboundOps(t) },
			req:  func() *http.Request { return signedAttachmentPut(t, testInboundSecret, body, int64(len(body))) },
			want: http.StatusServiceUnavailable,
		},
		{
			name: "unsigned, refused before the body is read",
			ops:  attachmentOps,
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodPut, "/api/internal/email/inbound/r-x/attachments/a", bytes.NewReader(body))
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "signed with another secret",
			ops:  attachmentOps,
			req:  func() *http.Request { return signedAttachmentPut(t, "someone-else", body, int64(len(body))) },
			want: http.StatusUnauthorized,
		},
		{
			name: "body changed after signing",
			ops:  attachmentOps,
			req: func() *http.Request {
				req := signedAttachmentPut(t, testInboundSecret, body, int64(len(body)))
				req.Body = io.NopCloser(strings.NewReader("%PDF-1.7 !!!"))
				return req
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "declared larger than any file we keep",
			ops:  attachmentOps,
			req: func() *http.Request {
				return signedAttachmentPut(t, testInboundSecret, body, recordemail.InboundMaxFileBytes+1)
			},
			want: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.ops().InboundAttachment(rec, tc.req())
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
