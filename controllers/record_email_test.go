package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/middleware"
	"stonesuite-backend/recordemail"
	"stonesuite-backend/tenancy"
)

const (
	testInboundDomain = "reply.example.com"
	testInboundSecret = "nk_test_inbound_secret_0123456789"
)

// unreachableTenants stands in for the control plane in tests that must stop
// before a tenant is looked up.
type unreachableTenants struct{}

func (unreachableTenants) TenantByID(context.Context, string) (*tenancy.Tenant, error) {
	return nil, errors.New("no control plane in this test")
}

func unreachablePool(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error) {
	return nil, errors.New("no tenant database in this test")
}

// inboundOps is an enabled RecordEmailOps with a store that has no database: any
// test using it must stop before a token is resolved.
func inboundOps(t *testing.T) *RecordEmailOps {
	t.Helper()
	ops := NewRecordEmailOps(recordemail.NewAddressStore(nil), testInboundDomain)
	return ops.WithInbound(testInboundSecret, unreachableTenants{}, unreachablePool)
}

// signedInbound builds a POST to the inbound route signed with secret at ts, the
// way stonesuite-notify signs it.
func signedInbound(t *testing.T, secret, body string, ts time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, InboundEmailPath, strings.NewReader(body))
	stamp := strconv.FormatInt(ts.Unix(), 10)
	req.Header.Set(middleware.InternalTimestampHeader, stamp)
	req.Header.Set(middleware.InternalSignatureHeader,
		middleware.SignInternal(secret, http.MethodPost, InboundEmailPath, stamp, []byte(body)))
	return req
}

// inboundJSON is the body notify forwards for a received mail.
func inboundJSON(t *testing.T, mail recordemail.InboundMail) string {
	t.Helper()
	b, err := json.Marshal(mail)
	if err != nil {
		t.Fatalf("marshal inbound mail: %v", err)
	}
	return string(b)
}

func TestInbound_RejectsWhatItCannotTrust(t *testing.T) {
	body := inboundJSON(t, recordemail.InboundMail{EmailID: "em_1", To: []string{"sales@example.com"}})

	tests := []struct {
		name string
		ops  func(*testing.T) *RecordEmailOps
		req  func(*testing.T) *http.Request
		want int
	}{
		{
			name: "feature off",
			ops:  func(*testing.T) *RecordEmailOps { return NewRecordEmailOps(nil, "") },
			req:  func(t *testing.T) *http.Request { return signedInbound(t, testInboundSecret, body, time.Now()) },
			want: http.StatusServiceUnavailable,
		},
		{
			name: "no signing secret configured",
			ops: func(t *testing.T) *RecordEmailOps {
				return NewRecordEmailOps(recordemail.NewAddressStore(nil), testInboundDomain).WithInbound("", unreachableTenants{}, unreachablePool)
			},
			req:  func(t *testing.T) *http.Request { return signedInbound(t, "", body, time.Now()) },
			want: http.StatusServiceUnavailable,
		},
		{
			name: "unsigned",
			ops:  inboundOps,
			req: func(*testing.T) *http.Request {
				return httptest.NewRequest(http.MethodPost, InboundEmailPath, strings.NewReader(body))
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "signed with another secret",
			ops:  inboundOps,
			req:  func(t *testing.T) *http.Request { return signedInbound(t, "someone-else", body, time.Now()) },
			want: http.StatusUnauthorized,
		},
		{
			name: "body changed after signing",
			ops:  inboundOps,
			req: func(t *testing.T) *http.Request {
				req := signedInbound(t, testInboundSecret, body, time.Now())
				req.Body = io.NopCloser(strings.NewReader(strings.Replace(body, "em_1", "em_2", 1)))
				return req
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "signed for another route",
			ops:  inboundOps,
			req: func(t *testing.T) *http.Request {
				req := signedInbound(t, testInboundSecret, body, time.Now())
				stamp := req.Header.Get(middleware.InternalTimestampHeader)
				req.Header.Set(middleware.InternalSignatureHeader,
					middleware.SignInternal(testInboundSecret, http.MethodPost, "/api/internal/automation/tick", stamp, []byte(body)))
				return req
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "timestamp outside the replay window",
			ops:  inboundOps,
			req: func(t *testing.T) *http.Request {
				return signedInbound(t, testInboundSecret, body, time.Now().Add(-time.Hour))
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "larger than any real mail",
			ops:  inboundOps,
			req: func(t *testing.T) *http.Request {
				return signedInbound(t, testInboundSecret, strings.Repeat("x", maxInboundBodyBytes+1), time.Now())
			},
			want: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.ops(t).Inbound(rec, tc.req(t))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestInbound_RefusesAReplayedRequest(t *testing.T) {
	ops := inboundOps(t)
	body := inboundJSON(t, recordemail.InboundMail{EmailID: "em_1", To: []string{"sales@example.com"}})
	ts := time.Now()

	first := httptest.NewRecorder()
	ops.Inbound(first, signedInbound(t, testInboundSecret, body, ts))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.Code)
	}

	// The same signed request again, inside the window: a captured request must
	// not be accepted twice. (notify signs every retry with a fresh timestamp.)
	second := httptest.NewRecorder()
	ops.Inbound(second, signedInbound(t, testInboundSecret, body, ts))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replayed status = %d, want 401", second.Code)
	}
}

func TestInbound_AcknowledgesMailThatIsNotOursToFile(t *testing.T) {
	tests := []struct {
		name string
		mail recordemail.InboundMail
	}{
		{"no reply address among recipients", recordemail.InboundMail{
			EmailID: "em_1", To: []string{"sales@example.com"}, CC: []string{"r-abcdefghijklmnopqrst@elsewhere.com"}}},
		{"reply-address shaped but wrong token length", recordemail.InboundMail{
			EmailID: "em_1", To: []string{"r-short@reply.example.com"}}},
		{"no recipients at all", recordemail.InboundMail{EmailID: "em_1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			inboundOps(t).Inbound(rec, signedInbound(t, testInboundSecret, inboundJSON(t, tc.mail), time.Now()))
			// 200, so notify does not retry mail we will never file.
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestInbound_RejectsMalformedBodyAfterAuthentication(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        `{not json`,
		"no email id":     `{"to":["r-abcdefghijklmnopqrst@reply.example.com"]}`,
		"empty email id":  `{"emailId":"","to":["r-abcdefghijklmnopqrst@reply.example.com"]}`,
		"wrong json type": `{"emailId":["em_1"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			inboundOps(t).Inbound(rec, signedInbound(t, testInboundSecret, body, time.Now()))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestReplyAddress_OnlyForThreadedRecordTypesWhenEnabled(t *testing.T) {
	off := NewRecordEmailOps(nil, "")
	if _, ok := off.ReplyAddress(context.Background(), "t", "r", "sales_order"); ok {
		t.Fatal("a switched-off feature must not hand out a reply address")
	}
	if off.Enabled() {
		t.Fatal("no store and no domain must read as disabled")
	}

	// A record type that keeps no thread never reaches the (database-backed) store.
	on := NewRecordEmailOps(recordemail.NewAddressStore(nil), testInboundDomain)
	if !on.Enabled() {
		t.Fatal("a store and a domain must read as enabled")
	}
	if _, ok := on.ReplyAddress(context.Background(), "t", "r", "purchase_order"); ok {
		t.Fatal("a purchase order has no Communication thread")
	}
}

func TestSenderLabel(t *testing.T) {
	tests := []struct{ person, company, want string }{
		{"Harish Narne", "Acme Stone", "Harish Narne (Acme Stone)"},
		{"  Harish  ", " Acme Stone ", "Harish (Acme Stone)"},
		{"Harish", "", "Harish"},
		{"", "Acme Stone", "Acme Stone"},
		{"", "", ""},
	}
	for _, tc := range tests {
		if got := senderLabel(tc.person, tc.company); got != tc.want {
			t.Errorf("senderLabel(%q, %q) = %q, want %q", tc.person, tc.company, got, tc.want)
		}
	}
}
