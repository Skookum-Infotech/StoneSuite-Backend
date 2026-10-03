# Resend Delivery Status (backend side) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the backend show, per email it sent, what Resend actually reported (delivered / delayed / bounced / …), and make sure every email that has a user behind it carries that user's identity and a staff-side link so `stonesuite-notify` can alert them in-app when it goes wrong.

**Architecture:** `services.EmailStatuses` makes one batched, fail-open call to notify's `POST /api/deliveries/email-status`; `services.SummarizeEmail` folds a send's per-recipient states into one worst-of summary with a client-safe message. List handlers add that summary to the rows they already return. The notify ids that make the lookup possible are persisted on the two invite tables that lack them. `NotificationRequest` gains `statusLink`, and the invite/document builders pass the actor and link. No new routes, no new webhook, no status columns copied into domain tables.

**Tech Stack:** Go 1.25, pgx v5, testify (`assert`/`require`), `httptest` fake notify.

**Spec:** `docs/superpowers/specs/2026-10-01-resend-delivery-status-design.md` (§5, §7, §8).
**Depends on:** the notify plan `stonesuite-notify/docs/superpowers/plans/2026-10-01-resend-delivery-status.md` — specifically its Task 6 wire contract (`POST /api/deliveries/email-status`) and Task 7 (`statusLink` accepted on create). The backend is written to **fail open**, so it can be deployed before or after notify and simply shows `unknown` until notify is ready.
**Companion plan:** WebUI (`EmailStatusBadge`), written after this one; it depends on the JSON fields fixed in Task 5.

## Deviations from the spec (decided while planning — update the spec to match)

1. **`customer_identities` is not given a `notify_notification_ids` column.** The CRM-record flow `POST /api/tenant/crm/customer/records/{id}/portal-invite` has no list or detail endpoint and no UI that shows invite state, so a persisted id would have nowhere to be displayed. It still gets the actor and a link, so the sender is **alerted**; only the persistent badge is deferred. (Spec §5.2's third row is dropped.)
2. **Badge surfaces are exactly four**: document-send history (`GET …/document/sends`, also serves PO "send to vendor"), staff invites (`GET /api/tenant/invites`), a customer's portal logins (`GET /api/tenant/customers/{uuid}/portal-users`), and a tenant's platform invites (`GET /api/platform/tenants/{id}/invites`). The tenant-wide portal roster (`GET /api/tenant/portal-users`) does not load invites today and is left alone.
3. **Platform onboarding invites carry no actor** (spec §9 item 1): badge yes, bell alert no.

## Global Constraints

- **Fail open, always.** A notify outage, timeout, malformed reply, or unconfigured notify must produce `emailStatus: "unknown"` — never a 5xx, never a missing list.
- **Provider detail never reaches a client.** `EmailDeliveryStatus.Detail` is for `slog` only; client text comes exclusively from the fixed `emailStateMessages` table.
- **No new IDOR path.** Ids sent to notify come only from rows the handler has already loaded under its normal auth/scope chain, and the call always names the tenant. Do not accept ids from the request.
- Client-facing states (identical to notify's `emailevents.DeriveState` vocabulary): `queued · sent · retrying · delayed · delivered · complained · bounced · failed · suppressed · skipped · unknown`. Worst-first severity: `bounced, failed, suppressed, complained, retrying, delayed, unknown, queued, sent, delivered, skipped`.
- One batched notify call per request (ids de-duplicated, chunked at 200, whole lookup bounded to 4 s).
- JSON additions are **camelCase** (`emailStatus`, `emailStatusAt`, `emailStatusMessage`, `emailRecipients`) even on rows whose existing keys are PascalCase.
- Schema: appended `ALTER TABLE … ADD COLUMN IF NOT EXISTS` at the end of `database/migrations/control_plane/schema.sql`; never edit an existing `CREATE TABLE`; no down-migrations. Refresh/resend paths reset the stored ids to `'{}'` (an old send's ids must not describe the new email).
- Errors wrapped with `fmt.Errorf("context: %w", err)`; `slog` only in request paths (no `log.Printf`); `context.Context` first; public functions have a one-line doc comment; no `any`-typed APIs; files over 300 lines are split (`services/notify_delivery.go` is ~280, hence the new file).
- Tests: testify; table-driven for pure functions.
- Scaffolding rules in `CLAUDE.md` (Notifications §1–5, Document Modules) apply: this plan adds no module, but any handler touched keeps its auth chain untouched.

## How to run Go here

No `go` on PATH. In every Bash call define this helper (shell state does not persist) and prefix Go commands with it. Dependencies are vendored, so it works offline.

```bash
dgo() { MSYS_NO_PATHCONV=1 docker run --rm -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
  -v "P:\WORKSPACE-SKOOKUM\StoneSuite-Backend:/app" -v ss-go-build:/root/.cache/go-build \
  -w /app golang:1.25.12-alpine "$@"; }
# e.g.  dgo go test ./services/ -run TestSummarizeEmail -count=1
```

**DB-backed tests** (`-tags dbtest`) need two databases, exactly as CI prepares them (`.github/workflows/ci-backend.yml`). Start the compose Postgres and prepare them once per session:

```bash
cd /p/WORKSPACE-SKOOKUM/StoneSuite-Backend && docker compose up -d postgres
PSQL() { docker compose exec -T postgres psql -U stonesuite -v ON_ERROR_STOP=1 "$@"; }
PSQL -d postgres -c 'DROP DATABASE IF EXISTS cp_tests;' -c 'CREATE DATABASE cp_tests;'
PSQL -d cp_tests < database/migrations/control_plane/schema.sql >/dev/null     # re-run after Task 2 edits the schema
PSQL -d postgres -c 'DROP DATABASE IF EXISTS t_tests;' -c 'CREATE DATABASE t_tests;'
PSQL -d t_tests < database/migrations/tenant/schema.sql >/dev/null
```

Then a DB-test runner (note `host.docker.internal:5433` — the compose Postgres):

```bash
dgodb() { MSYS_NO_PATHCONV=1 docker run --rm -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
  -e TEST_CP_DATABASE_URL='postgres://stonesuite:stonesuite_secret@host.docker.internal:5433/cp_tests?sslmode=disable' \
  -e TEST_DATABASE_URL='postgres://stonesuite:stonesuite_secret@host.docker.internal:5433/t_tests?sslmode=disable' \
  -v "P:\WORKSPACE-SKOOKUM\StoneSuite-Backend:/app" -v ss-go-build:/root/.cache/go-build \
  -w /app golang:1.25.12-alpine "$@"; }
# e.g.  dgodb go test -tags dbtest ./tenancy/ -run NotifyIDs -count=1 -v
```

CI runs each dbtest package against its own fresh database; if a locally-run package leaves rows that confuse another (some tests assert on absence), re-create `t_tests` as above. Working-tree files are CRLF: `gofmt -l` lists nearly everything; check a file with `tr -d '\r' < f | dgo gofmt -l` only after stripping CRs. `TestGeneratedAPIDocsAreCurrent` fails locally for CRLF reasons even when current (it is unaffected by this plan, which adds no routes — diff regenerated docs with CRs stripped before believing it).

**Checkpoints:** each task ends with files to stage and a suggested Conventional-Commit message. **Do not commit unless the user has asked you to.**

## File Structure

| File | Responsibility |
|---|---|
| `services/notify_status.go` (+`_test.go`) | state vocabulary, `EmailDeliveryStatus`, `EmailSummary`/`EmailRecipient`, `EmailStatuses` (fetch, fail-open), `SummarizeEmail` (worst-of fold + client-safe message) |
| `services/notify.go` | `NotificationRequest.StatusLink` |
| `services/email.go` (+`_test.go`) | actor + `statusLink` threaded through the user/portal/customer-portal invite builders |
| `database/migrations/control_plane/schema.sql` | `notify_notification_ids` on `tenant_invites`, `portal_invites` |
| `tenancy/invites.go`, `tenancy/portal_invites.go` (+dbtests) | carry/persist/reset the ids; `SetInviteNotifyIDs`, `SetPortalInviteNotifyIDs` |
| `controllers/email_status.go` (+`_test.go`) | `emailStatusIndex`, view builders, `withEmailSummary`, `persistNotifyIDs` |
| `controllers/documents.go`, `user.go`, `portal_access.go`, `tenant.go`, `customer_portal_admin.go` | persist ids at send sites; return `emailStatus…` on the four list endpoints; pass actor/link |
| `controllers/email_outcome_dbtest_test.go`, `user_invite_dbtest_test.go` | fake notify serves the status route; list-endpoint assertions |
| `docs/sales-documents.md`, `administration.md`, `customer-portal.md`; `CLAUDE.md` | help text; engineering rule |

---

### Task 1: `services/notify_status.go` — the status client and the fold

**Files:**
- Create: `services/notify_status.go`
- Create: `services/notify_status_test.go`

**Interfaces:**
- Consumes: `config.Config{NotifyURL, NotifyAPIKey}`, `notifyClient` (`services/notify.go`), `msgEmailSkipped` (`services/notify_delivery.go`).
- Produces (later tasks use these exact names):
  - consts `EmailStateQueued, EmailStateSent, EmailStateRetrying, EmailStateDelayed, EmailStateDelivered, EmailStateComplained, EmailStateBounced, EmailStateFailed, EmailStateSuppressed, EmailStateSkipped, EmailStateUnknown` (all `string`)
  - `type EmailDeliveryStatus struct{ State, Recipient, Detail string; UpdatedAt time.Time }`
  - `type EmailRecipient struct{ Email string "json:\"email\""; Status string "json:\"status\"" }`
  - `type EmailSummary struct{ Status string "json:\"emailStatus\""; At *time.Time "json:\"emailStatusAt,omitempty\""; Message string "json:\"emailStatusMessage,omitempty\""; Recipients []EmailRecipient "json:\"emailRecipients,omitempty\"" }`
  - `func EmailStatuses(ctx context.Context, tenantID string, ids []string) map[string]EmailDeliveryStatus` — never returns nil, never an error
  - `func SummarizeEmail(ids []string, statuses map[string]EmailDeliveryStatus) EmailSummary`

- [ ] **Step 1: Write the failing tests** — `services/notify_status_test.go`:

```go
package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestSummarizeEmail(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := func(state, recipient string) EmailDeliveryStatus {
		return EmailDeliveryStatus{State: state, Recipient: recipient, UpdatedAt: at, Detail: "provider text"}
	}

	tests := []struct {
		name         string
		ids          []string
		statuses     map[string]EmailDeliveryStatus
		wantStatus   string
		wantMessage  bool
		wantRecips   int
		wantAtSet    bool
	}{
		{"no ids", nil, nil, EmailStateUnknown, false, 0, false},
		{"notify returned nothing", []string{"a"}, map[string]EmailDeliveryStatus{}, EmailStateUnknown, false, 0, false},
		{"single delivered", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com")}, EmailStateDelivered, false, 1, true},
		{"single bounced carries a message", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("bounced", "x@y.com")}, EmailStateBounced, true, 1, true},
		{"bounce beats delivered", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com"), "b": st("bounced", "z@y.com")}, EmailStateBounced, true, 2, true},
		{"sent is worse than delivered", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com"), "b": st("sent", "z@y.com")}, EmailStateSent, false, 2, true},
		{"delayed beats queued and sent", []string{"a", "b", "c"}, map[string]EmailDeliveryStatus{"a": st("queued", "1@y.com"), "b": st("delayed", "2@y.com"), "c": st("sent", "3@y.com")}, EmailStateDelayed, true, 3, true},
		{"complaint outranks a retry", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("complained", "1@y.com"), "b": st("retrying", "2@y.com")}, EmailStateComplained, true, 2, true},
		{"one unknown id among delivered stays unsure", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com")}, EmailStateUnknown, false, 1, false},
		{"state outside the vocabulary becomes unknown", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("exploded", "x@y.com")}, EmailStateUnknown, false, 1, true},
		{"skipped has the turned-off message", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("skipped", "x@y.com")}, EmailStateSkipped, true, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeEmail(tc.ids, tc.statuses)
			assert.Equal(t, tc.wantStatus, got.Status)
			assert.Equal(t, tc.wantMessage, got.Message != "", "message = %q", got.Message)
			assert.Len(t, got.Recipients, tc.wantRecips)
			assert.Equal(t, tc.wantAtSet, got.At != nil)
			assert.NotContains(t, got.Message, "provider text", "provider detail must never be copied into the summary")
		})
	}
}

func TestSummarizeEmail_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got := SummarizeEmail([]string{"a"}, map[string]EmailDeliveryStatus{
		"a": {State: "bounced", Recipient: "x@y.com", UpdatedAt: at},
	})
	raw, err := json.Marshal(got)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "bounced", m["emailStatus"])
	assert.Equal(t, "2026-10-01T12:00:00Z", m["emailStatusAt"])
	assert.NotEmpty(t, m["emailStatusMessage"])
	assert.Equal(t, []any{map[string]any{"email": "x@y.com", "status": "bounced"}}, m["emailRecipients"])
}

// fakeStatusNotify records every status request and answers from a script.
type fakeStatusNotify struct {
	mu       sync.Mutex
	requests []struct {
		Secret string
		Body   struct {
			TenantID        string   `json:"tenantId"`
			NotificationIDs []string `json:"notificationIds"`
		}
	}
	respond func(w http.ResponseWriter, ids []string)
}

func (f *fakeStatusNotify) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deliveries/email-status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var rec struct {
			Secret string
			Body   struct {
				TenantID        string   `json:"tenantId"`
				NotificationIDs []string `json:"notificationIds"`
			}
		}
		rec.Secret = r.Header.Get("X-Internal-Secret")
		_ = json.NewDecoder(r.Body).Decode(&rec.Body)
		f.mu.Lock()
		f.requests = append(f.requests, rec)
		f.mu.Unlock()
		f.respond(w, rec.Body.NotificationIDs)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func deliveredFor(w http.ResponseWriter, ids []string) {
	statuses := map[string]any{}
	for _, id := range ids {
		statuses[id] = map[string]any{"state": "delivered", "recipient": id + "@example.com", "detail": "", "updatedAt": "2026-10-01T12:00:00Z"}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"statuses": statuses}})
}

func TestFetchEmailStatuses_RequestShapeAndParsing(t *testing.T) {
	fake := &fakeStatusNotify{respond: deliveredFor}
	srv := fake.server(t)
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "secret-1"}

	got, err := fetchEmailStatuses(context.Background(), cfg, "tenant-1", []string{"n1", "n2"})

	require.NoError(t, err)
	require.Len(t, fake.requests, 1)
	assert.Equal(t, "secret-1", fake.requests[0].Secret)
	assert.Equal(t, "tenant-1", fake.requests[0].Body.TenantID)
	assert.Equal(t, []string{"n1", "n2"}, fake.requests[0].Body.NotificationIDs)
	assert.Equal(t, "delivered", got["n1"].State)
	assert.Equal(t, "n2@example.com", got["n2"].Recipient)
}

func TestFetchEmailStatuses_DedupesAndChunks(t *testing.T) {
	fake := &fakeStatusNotify{respond: deliveredFor}
	srv := fake.server(t)
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "k"}

	ids := make([]string, 0, 451)
	for i := 0; i < 450; i++ {
		ids = append(ids, "n"+itoa(i))
	}
	ids = append(ids, "n"+itoa(0)) // an exact duplicate of the first id must be sent once

	got, err := fetchEmailStatuses(context.Background(), cfg, "t", ids)

	require.NoError(t, err)
	require.Len(t, fake.requests, 3, "450 distinct ids (the duplicate is dropped) at 200 per request is 3 requests")
	assert.Len(t, fake.requests[0].Body.NotificationIDs, 200)
	assert.Len(t, fake.requests[1].Body.NotificationIDs, 200)
	assert.Len(t, fake.requests[2].Body.NotificationIDs, 50)
	assert.Len(t, got, 450)
}

func TestFetchEmailStatuses_NothingToAsk(t *testing.T) {
	fake := &fakeStatusNotify{respond: deliveredFor}
	srv := fake.server(t)
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "k"}

	got, err := fetchEmailStatuses(context.Background(), cfg, "t", []string{"", ""})

	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, fake.requests, "no ids means no request")
}

func TestFetchEmailStatuses_NotConfiguredIsEmptyNotError(t *testing.T) {
	got, err := fetchEmailStatuses(context.Background(), config.Config{}, "t", []string{"n1"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFetchEmailStatuses_Failures(t *testing.T) {
	tests := []struct {
		name    string
		respond func(w http.ResponseWriter, ids []string)
	}{
		{"notify 500", func(w http.ResponseWriter, _ []string) { w.WriteHeader(http.StatusInternalServerError) }},
		{"notify 400", func(w http.ResponseWriter, _ []string) { w.WriteHeader(http.StatusBadRequest) }},
		{"not json", func(w http.ResponseWriter, _ []string) { _, _ = w.Write([]byte("<html>")) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStatusNotify{respond: tc.respond}
			srv := fake.server(t)
			cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "k"}

			got, err := fetchEmailStatuses(context.Background(), cfg, "t", []string{"n1"})

			require.Error(t, err)
			assert.NotNil(t, got, "a failure still returns a usable (empty) map")
			assert.Empty(t, got)
		})
	}
}

func TestEmailStatuses_IsFailOpen(t *testing.T) {
	// Unreachable notify: the public entry point swallows the error.
	orig := config.AppConfig
	config.AppConfig.NotifyURL = "http://127.0.0.1:1" // nothing listens here
	config.AppConfig.NotifyAPIKey = "k"
	t.Cleanup(func() { config.AppConfig = orig })

	got := EmailStatuses(context.Background(), "t", []string{"n1"})

	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func itoa(i int) string { return string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) }
```

(`itoa` yields a 3-digit zero-padded id; 450 ids stay distinct and `n000` collides with nothing else. If `services` already has an `itoa` helper the compiler will say so — rename this one `statusTestID`.)

- [ ] **Step 2: Run to verify failure**

Run: `dgo go test ./services/ -run 'TestSummarizeEmail|TestFetchEmailStatuses|TestEmailStatuses' -count=1`
Expected: FAIL to compile — `undefined: SummarizeEmail`, `fetchEmailStatuses`, `EmailStatuses`, `EmailDeliveryStatus`.

- [ ] **Step 3: Implement** — `services/notify_status.go`:

```go
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/config"
)

// ConfirmEmailDelivery (notify_delivery.go) answers "did the first attempt
// work?" at request time. This file answers the later question — what did the
// email provider actually report (delivered, delayed, bounced, …) — by asking
// stonesuite-notify, which receives Resend's webhooks and is the one place that
// state lives. Every lookup here fails open: a list page must render, with
// "unknown", when notify is slow, down, or not configured.
const (
	// emailStatusTimeout bounds one whole lookup (all chunks). notify's
	// scale-to-zero cold start is ~1-2s; a list page must not hang on it.
	emailStatusTimeout = 4 * time.Second
	// emailStatusChunk is notify's per-request cap on notification ids.
	emailStatusChunk     = 200
	emailStatusPath      = "%s/api/deliveries/email-status"
	emailStatusBodyLimit = 1 << 20
)

// Real-delivery states as notify reports them (stonesuite-notify
// emailevents.DeriveState). The vocabulary is closed: anything else is treated
// as EmailStateUnknown so a notify upgrade can never put an unrendered string
// in front of a user.
const (
	EmailStateQueued     = "queued"
	EmailStateSent       = "sent"
	EmailStateRetrying   = "retrying"
	EmailStateDelayed    = "delayed"
	EmailStateDelivered  = "delivered"
	EmailStateComplained = "complained"
	EmailStateBounced    = "bounced"
	EmailStateFailed     = "failed"
	EmailStateSuppressed = "suppressed"
	EmailStateSkipped    = "skipped"
	EmailStateUnknown    = "unknown"
)

// emailStatesWorstFirst orders states by how much the sender needs to know: a
// summary shows the worst state across a send's recipients, so one bounced
// address is never hidden behind two delivered ones, and "sent" (accepted, not
// yet delivered) is never reported as "delivered".
var emailStatesWorstFirst = []string{
	EmailStateBounced, EmailStateFailed, EmailStateSuppressed, EmailStateComplained,
	EmailStateRetrying, EmailStateDelayed, EmailStateUnknown, EmailStateQueued,
	EmailStateSent, EmailStateDelivered, EmailStateSkipped,
}

// emailStateMessages is the only text a client ever sees about a state. It is
// fixed on purpose: notify's Detail can name the sending domain or an API-key
// problem and stays in the server log.
var emailStateMessages = map[string]string{
	EmailStateBounced:    "The recipient's mail server rejected this email. Check the address and try again.",
	EmailStateFailed:     "The email could not be sent. Try again, or contact support if the problem continues.",
	EmailStateSuppressed: "This address is on a suppression list because earlier emails to it bounced or were reported as spam.",
	EmailStateComplained: "The recipient reported this email as spam.",
	EmailStateRetrying:   "The first attempt to send this email failed. We are trying again.",
	EmailStateDelayed:    "The recipient's mail server is slow to accept this email. We will keep trying; no action is needed yet.",
	EmailStateSkipped:    msgEmailSkipped,
}

// EmailDeliveryStatus is notify's reading of one email.
type EmailDeliveryStatus struct {
	State     string    `json:"state"`
	Recipient string    `json:"recipient"`
	// Detail is the provider diagnostic. Server-side only: never copy it into a response.
	Detail    string    `json:"detail"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EmailRecipient is one recipient's state inside a summary.
type EmailRecipient struct {
	Email  string `json:"email"`
	Status string `json:"status"`
}

// EmailSummary is the client-facing outcome of one send (one or more
// recipients). Embedding it in a response struct, or copying its fields into a
// map view, adds the emailStatus… keys.
type EmailSummary struct {
	Status     string           `json:"emailStatus"`
	At         *time.Time       `json:"emailStatusAt,omitempty"`
	Message    string           `json:"emailStatusMessage,omitempty"`
	Recipients []EmailRecipient `json:"emailRecipients,omitempty"`
}

func emailStateSeverity(state string) int {
	for i, s := range emailStatesWorstFirst {
		if s == state {
			return i
		}
	}
	return emailStateSeverity(EmailStateUnknown)
}

func normalizeEmailState(state string) string {
	for _, s := range emailStatesWorstFirst {
		if s == state {
			return state
		}
	}
	return EmailStateUnknown
}

// SummarizeEmail folds the per-recipient states of one send into a single
// worst-of summary. An id notify did not answer for counts as unknown, so a
// send is never reported as better than what is actually known.
func SummarizeEmail(ids []string, statuses map[string]EmailDeliveryStatus) EmailSummary {
	if len(ids) == 0 {
		return EmailSummary{Status: EmailStateUnknown}
	}
	worst, worstSeverity := "", -1
	var worstAt time.Time
	recipients := make([]EmailRecipient, 0, len(ids))
	for _, id := range ids {
		state, at := EmailStateUnknown, time.Time{}
		if st, ok := statuses[id]; ok {
			state, at = normalizeEmailState(st.State), st.UpdatedAt
			if st.Recipient != "" {
				recipients = append(recipients, EmailRecipient{Email: st.Recipient, Status: state})
			}
		}
		if severity := emailStateSeverity(state); worst == "" || severity < worstSeverity {
			worst, worstSeverity, worstAt = state, severity, at
		}
	}
	out := EmailSummary{Status: worst, Message: emailStateMessages[worst], Recipients: recipients}
	if !worstAt.IsZero() {
		out.At = &worstAt
	}
	return out
}

// EmailStatuses asks notify what actually happened to each notification's
// email. It never fails the caller: on any problem it logs and returns what it
// has (often an empty map), and SummarizeEmail reads the gaps as unknown.
func EmailStatuses(ctx context.Context, tenantID string, ids []string) map[string]EmailDeliveryStatus {
	out, err := fetchEmailStatuses(ctx, config.AppConfig, tenantID, ids)
	if err != nil {
		slog.WarnContext(ctx, "email status lookup failed; rendering without delivery status",
			"tenant", tenantID, "ids", len(ids), "error", err)
	}
	return out
}

// fetchEmailStatuses de-duplicates ids, asks notify in chunks, and returns the
// merged answers plus the first error (the map is always usable). An
// unconfigured notify is "nothing to ask", not an error: dev setups run without it.
func fetchEmailStatuses(ctx context.Context, cfg config.Config, tenantID string, ids []string) (map[string]EmailDeliveryStatus, error) {
	out := map[string]EmailDeliveryStatus{}
	distinct := distinctNonEmpty(ids)
	if len(distinct) == 0 || cfg.NotifyURL == "" || cfg.NotifyAPIKey == "" {
		return out, nil
	}

	ctx, cancel := context.WithTimeout(ctx, emailStatusTimeout)
	defer cancel()

	var firstErr error
	for start := 0; start < len(distinct); start += emailStatusChunk {
		end := min(start+emailStatusChunk, len(distinct))
		got, err := postEmailStatus(ctx, cfg, tenantID, distinct[start:end])
		for id, st := range got {
			out[id] = st
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

func distinctNonEmpty(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// postEmailStatus makes one call to notify's internal-secret-gated
// POST /api/deliveries/email-status.
func postEmailStatus(ctx context.Context, cfg config.Config, tenantID string, ids []string) (map[string]EmailDeliveryStatus, error) {
	payload, err := json.Marshal(map[string]any{"tenantId": tenantID, "notificationIds": ids})
	if err != nil {
		return nil, fmt.Errorf("marshal email status request: %w", err)
	}
	endpoint := fmt.Sprintf(emailStatusPath, strings.TrimRight(cfg.NotifyURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build email status request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", cfg.NotifyAPIKey)

	resp, err := notifyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read email status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, emailStatusBodyLimit))
		return nil, fmt.Errorf("email status returned %d: %s", resp.StatusCode, body)
	}

	// { "success": true, "data": { "statuses": { "<id>": { "state", "recipient", "detail", "updatedAt" } } } }
	var decoded struct {
		Data struct {
			Statuses map[string]EmailDeliveryStatus `json:"statuses"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, emailStatusBodyLimit)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode email status: %w", err)
	}
	return decoded.Data.Statuses, nil
}
```

(`min` is the Go 1.21 builtin; the repo is on 1.25. `[]any`-free: `map[string]any` is used only for building the JSON request body, which is a local literal, not an API type.)

- [ ] **Step 4: Run to verify it passes**

Run: `dgo sh -c 'go vet ./services/ && go test ./services/ -count=1'`
Expected: PASS (new tests and the existing `services` suite).

- [ ] **Step 5: Checkpoint** — stage `services/notify_status.go services/notify_status_test.go`; suggested message `feat(services): read real email delivery status from notify (fail-open, worst-of fold)`.

---

### Task 2: Persist notify ids on `tenant_invites` and `portal_invites`

**Files:**
- Modify: `database/migrations/control_plane/schema.sql` (append at EOF)
- Modify: `tenancy/invites.go` (`Invite`, SQL column lists, `RefreshInvite`; add `SetInviteNotifyIDs`)
- Modify: `tenancy/portal_invites.go` (`PortalInvite`, `portalInviteColumns`, `scanPortalInvite`, `RefreshPortalInvite`; add `SetPortalInviteNotifyIDs`)
- Modify: `tenancy/invites_dbtest_test.go` (add tests) and `tenancy/portal_invites_dbtest_test.go` (add tests)

**Interfaces:**
- Produces: `Invite.NotifyNotificationIDs []string`; `PortalInvite.NotifyNotificationIDs []string`;
  `func (c *ControlPlane) SetInviteNotifyIDs(ctx context.Context, id string, ids []string) error`;
  `func (c *ControlPlane) SetPortalInviteNotifyIDs(ctx context.Context, id string, ids []string) error`.
  Both setters replace the stored value, store `nil` as `'{}'`, and treat a missing row as a no-op.

- [ ] **Step 1: Write the failing DB tests**

Append to `tenancy/invites_dbtest_test.go`:

```go
// TestTenantInviteNotifyIDs covers the notify_notification_ids column on the
// platform onboarding invite: empty on create, round-trips through the setter,
// nil stores as '{}', a refresh (resend) clears it, a missing row is a no-op.
func TestTenantInviteNotifyIDs(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	mkInvite := func(t *testing.T) *Invite {
		t.Helper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		inv, err := cp.CreateInvite(ctx, tenantID, "owner-"+suffix+"@example.com", "tok-"+suffix, time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
		return inv
	}

	t.Run("fresh invite scans as empty, not nil", func(t *testing.T) {
		inv := mkInvite(t)
		if inv.NotifyNotificationIDs == nil || len(inv.NotifyNotificationIDs) != 0 {
			t.Fatalf("NotifyNotificationIDs = %#v, want empty non-nil slice", inv.NotifyNotificationIDs)
		}
	})

	t.Run("setter round-trips through list and by-token reads", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, []string{"n-1", "n-2"}); err != nil {
			t.Fatalf("SetInviteNotifyIDs: %v", err)
		}
		got, err := cp.InviteByToken(ctx, inv.Token)
		if err != nil {
			t.Fatalf("InviteByToken: %v", err)
		}
		if len(got.NotifyNotificationIDs) != 2 || got.NotifyNotificationIDs[0] != "n-1" {
			t.Fatalf("by-token ids = %v, want [n-1 n-2]", got.NotifyNotificationIDs)
		}
		list, err := cp.ListInvitesByTenant(ctx, tenantID)
		if err != nil {
			t.Fatalf("ListInvitesByTenant: %v", err)
		}
		found := false
		for _, i := range list {
			if i.ID == inv.ID {
				found = true
				if len(i.NotifyNotificationIDs) != 2 {
					t.Fatalf("listed ids = %v, want 2", i.NotifyNotificationIDs)
				}
			}
		}
		if !found {
			t.Fatal("ListInvitesByTenant did not include the invite")
		}
	})

	t.Run("nil stores as an empty array", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, []string{"n-9"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, nil); err != nil {
			t.Fatalf("SetInviteNotifyIDs(nil): %v", err)
		}
		got, err := cp.InviteByToken(ctx, inv.Token)
		if err != nil || got.NotifyNotificationIDs == nil || len(got.NotifyNotificationIDs) != 0 {
			t.Fatalf("ids = %#v, err = %v; want empty non-nil", got.NotifyNotificationIDs, err)
		}
	})

	t.Run("a resend clears the previous send's ids", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, []string{"n-old"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		refreshed, err := cp.RefreshInvite(ctx, inv.ID, "tok2-"+inv.Token, time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("RefreshInvite: %v", err)
		}
		if len(refreshed.NotifyNotificationIDs) != 0 {
			t.Fatalf("ids after refresh = %v, want empty", refreshed.NotifyNotificationIDs)
		}
	})

	t.Run("missing row is a no-op", func(t *testing.T) {
		if err := cp.SetInviteNotifyIDs(ctx, "00000000-0000-0000-0000-000000000000", []string{"n-1"}); err != nil {
			t.Fatalf("SetInviteNotifyIDs(missing): %v", err)
		}
	})
}
```

Append to `tenancy/portal_invites_dbtest_test.go`:

```go
// TestPortalInviteNotifyIDs covers notify_notification_ids on portal_invites
// the same way TestUserInviteNotifyIDs does for staff invites.
func TestPortalInviteNotifyIDs(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	mkInvite := func(t *testing.T) *PortalInvite {
		t.Helper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		email := "portal-" + suffix + "@example.com"
		identity, err := cp.CreateIdentity(ctx, tenantID, email, "", "Portal Person", false)
		if err != nil {
			t.Fatalf("CreateIdentity: %v", err)
		}
		inv, err := cp.CreatePortalInvite(ctx, tenantID, identity.ID, email, "Portal Person",
			"00000000-0000-0000-0000-0000000000aa", "tok-"+suffix, "", time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("CreatePortalInvite: %v", err)
		}
		return inv
	}

	t.Run("fresh invite scans as empty, not nil", func(t *testing.T) {
		inv := mkInvite(t)
		if inv.NotifyNotificationIDs == nil || len(inv.NotifyNotificationIDs) != 0 {
			t.Fatalf("NotifyNotificationIDs = %#v, want empty non-nil slice", inv.NotifyNotificationIDs)
		}
	})

	t.Run("setter round-trips via the identity lookup the list uses", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetPortalInviteNotifyIDs(ctx, inv.ID, []string{"n-1"}); err != nil {
			t.Fatalf("SetPortalInviteNotifyIDs: %v", err)
		}
		got, err := cp.LatestPortalInviteForIdentity(ctx, tenantID, inv.IdentityID)
		if err != nil || len(got.NotifyNotificationIDs) != 1 || got.NotifyNotificationIDs[0] != "n-1" {
			t.Fatalf("ids = %v, err = %v; want [n-1]", got.NotifyNotificationIDs, err)
		}
	})

	t.Run("nil stores as an empty array", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetPortalInviteNotifyIDs(ctx, inv.ID, []string{"n-9"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := cp.SetPortalInviteNotifyIDs(ctx, inv.ID, nil); err != nil {
			t.Fatalf("SetPortalInviteNotifyIDs(nil): %v", err)
		}
		got, err := cp.PortalInviteByToken(ctx, inv.Token)
		if err != nil || got.NotifyNotificationIDs == nil || len(got.NotifyNotificationIDs) != 0 {
			t.Fatalf("ids = %#v, err = %v; want empty non-nil", got.NotifyNotificationIDs, err)
		}
	})

	t.Run("a resend clears the previous send's ids", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetPortalInviteNotifyIDs(ctx, inv.ID, []string{"n-old"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		refreshed, err := cp.RefreshPortalInvite(ctx, inv.ID, "tok2-"+inv.Token, time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("RefreshPortalInvite: %v", err)
		}
		if len(refreshed.NotifyNotificationIDs) != 0 {
			t.Fatalf("ids after refresh = %v, want empty", refreshed.NotifyNotificationIDs)
		}
	})

	t.Run("missing row is a no-op", func(t *testing.T) {
		if err := cp.SetPortalInviteNotifyIDs(ctx, "00000000-0000-0000-0000-000000000000", []string{"n-1"}); err != nil {
			t.Fatalf("SetPortalInviteNotifyIDs(missing): %v", err)
		}
	})
}
```

- [ ] **Step 2: Run to verify failure**

Re-apply nothing yet. Run: `dgodb go vet -tags dbtest ./tenancy/`
Expected: FAIL to compile — `inv.NotifyNotificationIDs undefined (type *Invite …)`, `cp.SetInviteNotifyIDs undefined`.

- [ ] **Step 3: Implement the schema** — append to the very end of `database/migrations/control_plane/schema.sql`:

```sql

-- stonesuite-notify ids for the invite email, so the invite's real delivery
-- status (delivered / delayed / bounced / …) can be looked up from notify later
-- and shown next to the invite. Same purpose and shape as
-- user_invites.notify_notification_ids and document_sends.notify_notification_ids.
-- Empty array = pre-migration rows, a send whose notify response could not be
-- parsed, or an invite whose email send failed outright; a resend resets it.
ALTER TABLE tenant_invites ADD COLUMN IF NOT EXISTS notify_notification_ids TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE portal_invites ADD COLUMN IF NOT EXISTS notify_notification_ids TEXT[] NOT NULL DEFAULT '{}';
```

- [ ] **Step 4: Implement `tenancy/invites.go`** — give `Invite` the field (last, after `CreatedAt`):

```go
	// NotifyNotificationIDs holds the stonesuite-notify notification id for each
	// recipient of the most recent invite email send, so its real delivery status
	// can be looked up later. Empty for pre-migration invites, an unparseable
	// notify response, or a send that failed outright.
	NotifyNotificationIDs []string
```

Replace the four inline column lists with one constant and scanner (placed right after the `Invite` type):

```go
const inviteColumns = `id, tenant_id, contact_email, token, status, expires_at, accepted_at, created_at, notify_notification_ids`

func scanInvite(row pgx.Row) (*Invite, error) {
	var inv Invite
	if err := row.Scan(&inv.ID, &inv.TenantID, &inv.ContactEmail, &inv.Token, &inv.Status,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt, &inv.NotifyNotificationIDs); err != nil {
		return nil, err
	}
	return &inv, nil
}
```

Then rewrite the four functions to use them:

```go
// CreateInvite inserts a pending invite for a tenant.
func (c *ControlPlane) CreateInvite(ctx context.Context, tenantID, email, token string, expiresAt time.Time) (*Invite, error) {
	inv, err := scanInvite(c.pool.QueryRow(ctx, `
		INSERT INTO tenant_invites (tenant_id, contact_email, token, status, expires_at, sent_at)
		VALUES ($1, $2, $3, 'pending', $4, NOW())
		RETURNING `+inviteColumns, tenantID, email, token, expiresAt))
	if err != nil {
		return nil, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// InviteByToken loads an invite by its token.
func (c *ControlPlane) InviteByToken(ctx context.Context, token string) (*Invite, error) {
	inv, err := scanInvite(c.pool.QueryRow(ctx,
		`SELECT `+inviteColumns+` FROM tenant_invites WHERE token = $1`, token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("invite by token: %w", err)
	}
	return inv, nil
}

// ListInvitesByTenant returns a tenant's invites, newest first.
func (c *ControlPlane) ListInvitesByTenant(ctx context.Context, tenantID string) ([]Invite, error) {
	rows, err := c.pool.Query(ctx,
		`SELECT `+inviteColumns+` FROM tenant_invites WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list invites by tenant: %w", err)
	}
	defer rows.Close()

	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// RefreshInvite re-issues an existing invite with a fresh token + expiry and
// resets it to pending (the "resend / retry" path). updated_at/sent_at bump, and
// notify_notification_ids is cleared so a failed resend does not leave the
// previous send's ids implying the new email is trackable.
func (c *ControlPlane) RefreshInvite(ctx context.Context, id, token string, expiresAt time.Time) (*Invite, error) {
	inv, err := scanInvite(c.pool.QueryRow(ctx, `
		UPDATE tenant_invites
		SET token = $2, expires_at = $3, status = 'pending', accepted_at = NULL,
		    notify_notification_ids = '{}', sent_at = NOW(), updated_at = NOW()
		WHERE id = $1
		RETURNING `+inviteColumns, id, token, expiresAt))
	if err != nil {
		return nil, fmt.Errorf("refresh invite: %w", err)
	}
	return inv, nil
}
```

(`LatestInviteForTenant` is unchanged.) Add the setter after `RefreshInvite`:

```go
// SetInviteNotifyIDs records the stonesuite-notify notification ids returned for
// the most recent onboarding-invite email, so its real delivery status can be
// looked up later. Replaces any previous value; nil is stored as an empty array.
// Best-effort: a missing row (invite since removed) affects zero rows and is not
// an error.
func (c *ControlPlane) SetInviteNotifyIDs(ctx context.Context, id string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenant_invites SET notify_notification_ids = $2, updated_at = NOW() WHERE id = $1`,
		id, ids); err != nil {
		return fmt.Errorf("set invite notify ids: %w", err)
	}
	return nil
}
```

Before relying on the refactor, confirm nothing else reads `tenant_invites` into `Invite`: `grep -rn "tenant_invites" --include=*.go . | grep -v _test` must list only `tenancy/invites.go`.

- [ ] **Step 5: Implement `tenancy/portal_invites.go`** — add to `PortalInvite` after `CreatedAt`:

```go
	// NotifyNotificationIDs: see Invite.NotifyNotificationIDs.
	NotifyNotificationIDs []string
```

Update the column list and scan (append the column last in both):

```go
const portalInviteColumns = `id, tenant_id, identity_id, email, full_name, customer_uuid,
	token, status, COALESCE(invited_by::text, ''), expires_at, accepted_at, created_at,
	notify_notification_ids`
```

```go
	err := row.Scan(&inv.ID, &inv.TenantID, &inv.IdentityID, &inv.Email, &inv.FullName,
		&inv.CustomerUUID, &inv.Token, &inv.Status, &inv.InvitedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt, &inv.NotifyNotificationIDs)
```

Change `RefreshPortalInvite`'s SET clause to also clear the ids:

```go
	q := `UPDATE portal_invites
		SET token = $2, expires_at = $3, status = 'pending', accepted_at = NULL,
		    notify_notification_ids = '{}', updated_at = NOW()
		WHERE id = $1
		RETURNING ` + portalInviteColumns
```

Add the setter after `RefreshPortalInvite`:

```go
// SetPortalInviteNotifyIDs records the stonesuite-notify notification ids for
// the most recent portal-invite email. Same contract as SetInviteNotifyIDs.
func (c *ControlPlane) SetPortalInviteNotifyIDs(ctx context.Context, id string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	if _, err := c.pool.Exec(ctx,
		`UPDATE portal_invites SET notify_notification_ids = $2, updated_at = NOW() WHERE id = $1`,
		id, ids); err != nil {
		return fmt.Errorf("set portal invite notify ids: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Re-apply the control-plane schema to the test database, run the DB tests**

```bash
PSQL -d cp_tests < database/migrations/control_plane/schema.sql >/dev/null     # idempotent: proves the new ALTERs apply, twice if you run it again
PSQL -d cp_tests < database/migrations/control_plane/schema.sql >/dev/null
dgodb go test -tags dbtest ./tenancy/ -count=1 -v -run 'NotifyIDs|PortalInvite|Invite'
```

Expected: PASS for `TestTenantInviteNotifyIDs`, `TestPortalInviteNotifyIDs`, and the existing `TestUserInviteNotifyIDs` / `TestPortalInviteForCustomer`. Then the default build: `dgo sh -c 'go vet ./... && go test ./tenancy/ -count=1'` — PASS.

- [ ] **Step 7: Audit the schema change** — run the `migration-auditor` agent on `database/migrations/control_plane/schema.sql` (it checks idempotency, transaction-safety, non-destructiveness). Expected: no findings.

- [ ] **Step 8: Checkpoint** — stage `database/migrations/control_plane/schema.sql tenancy/`; suggested message `feat(tenancy): persist notify ids on tenant and portal invites`.

---

### Task 3: Actor and `statusLink` on every email that has a user behind it

**Files:**
- Modify: `services/notify.go` (`NotificationRequest.StatusLink`)
- Modify: `services/email.go` (builders + senders for user, portal, and customer-portal invites)
- Modify: `services/email_test.go` (update the call-sites listed below)
- Create: `services/email_actor_test.go`
- Modify: `controllers/documents.go` (`customerSendRequest`)
- Modify: `controllers/portal_access.go` (`issueInvite` call), `controllers/customer_portal_admin.go` (call), `controllers/email_preview_test.go` (two call-sites)

**Interfaces:**
- Consumes: notify plan Task 7 (`statusLink` accepted on `POST /api/notifications/internal`; unknown JSON fields are ignored by older notify, so this is safe to ship first).
- Produces:
  - `NotificationRequest.StatusLink string` — `json:"statusLink,omitempty"`
  - `buildUserInviteNotification(...)` now also sets `StatusLink: userInvitesPath` (`"/config/users"`) — signature unchanged
  - `buildPortalInviteNotification(tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int)`
  - `SendPortalInviteEmailWithResult(ctx, tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int)` and its wrapper `SendPortalInviteEmail(...)` with the same two new parameters
  - `buildCustomerPortalInviteNotification(tenantID, resourceID, actorUserID, statusLink, recipientEmail, recipientName, tenantDisplayName, setupLink string)`, `SendCustomerPortalInviteEmailWithResult(...)`, `SendCustomerPortalInviteEmail(...)` likewise
  - platform onboarding builders are **unchanged** (no actor, spec §9 item 1)

- [ ] **Step 1: Write the failing tests** — `services/email_actor_test.go`:

```go
package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestInviteBuilders_ActorAndStatusLink(t *testing.T) {
	t.Run("staff invite links to the Users page", func(t *testing.T) {
		req := buildUserInviteNotification("t1", "inv-1", "identity-9", "a@b.com", "A", "Acme", "https://x/accept")
		assert.Equal(t, "identity-9", req.ActorUserID)
		assert.Equal(t, userInvitesPath, req.StatusLink)
		assert.Equal(t, "/config/users", req.StatusLink)
	})

	t.Run("portal invite carries the granting staff member and a customer link", func(t *testing.T) {
		req := buildPortalInviteNotification("t1", "inv-1", "identity-9", "/crm/customer/c-1",
			"a@b.com", "A", "Acme", "https://x/setup", 72)
		assert.Equal(t, "identity-9", req.ActorUserID)
		assert.Equal(t, "/crm/customer/c-1", req.StatusLink)
	})

	t.Run("customer portal invite carries the inviting staff member and a customer link", func(t *testing.T) {
		req := buildCustomerPortalInviteNotification("t1", "7", "identity-9", "/crm/customer/c-1",
			"a@b.com", "A", "Acme", "https://x/set")
		assert.Equal(t, "identity-9", req.ActorUserID)
		assert.Equal(t, "/crm/customer/c-1", req.StatusLink)
	})

	t.Run("platform onboarding invite deliberately has no actor", func(t *testing.T) {
		req := buildOnboardingInviteNotification("t1", "inv-1", "a@b.com", "A", "https://x/apply")
		assert.Empty(t, req.ActorUserID, "a platform admin is not a user inside the invited tenant")
		assert.Empty(t, req.StatusLink)
	})
}

func TestNotificationRequest_StatusLinkOnTheWire(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-1"}]}}`))
	}))
	defer srv.Close()

	orig := config.AppConfig
	config.AppConfig.NotifyURL = srv.URL
	config.AppConfig.NotifyAPIKey = "k"
	t.Cleanup(func() { config.AppConfig = orig })

	_, err := SendNotificationWithResult(context.Background(), NotificationRequest{
		TenantID: "t1", Recipients: []RecipientTarget{{Email: "a@b.com"}},
		ActorUserID: "identity-9", EventType: "x", Resource: "user", ResourceID: "r1", Title: "T",
		StatusLink: "/config/users", Channels: []string{"email"},
	})

	require.NoError(t, err)
	assert.Equal(t, "/config/users", got["statusLink"])
	assert.Equal(t, "identity-9", got["actorUserId"])
}

func TestNotificationRequest_StatusLinkOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(NotificationRequest{TenantID: "t1"})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "statusLink")
}
```

Add one controller-level test for the document link — append to a non-DB test file in `controllers/` (for example a new `controllers/documents_statuslink_test.go`):

```go
package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCustomerSendRequest_CarriesStaffStatusLink(t *testing.T) {
	req := customerSendRequest("t1", "identity-1", DocMeta{WorkflowKey: "invoice", Number: "INV-1"},
		"rec-1", "Subject", docpdf.PrintableDoc{}, "", []string{"a@b.com"}, nil, "INV-1.pdf", []byte("%PDF"))

	assert.Equal(t, recordLink("invoice", "rec-1"), req.StatusLink)
	assert.NotEmpty(t, req.StatusLink, "invoice is a registered global-search resource, so it has a staff route")
	assert.Equal(t, "identity-1", req.ActorUserID)
}
```

(add `"stonesuite-backend/docpdf"` to that file's imports.)

- [ ] **Step 2: Run to verify failure**

Run: `dgo go vet ./services/ ./controllers/`
Expected: FAIL — `unknown field StatusLink`, `undefined: userInvitesPath`, `too many arguments in call to buildPortalInviteNotification`.

- [ ] **Step 3: Implement**

`services/notify.go` — add to `NotificationRequest` after `Link`:

```go
	// StatusLink is the staff-side page a delivery-problem alert about this
	// notification's email should open (where the email's status is shown).
	// Distinct from Link, which for a customer email is the customer's URL.
	// Optional; notify truncates nothing, so keep it a short app-relative path.
	StatusLink string `json:"statusLink,omitempty"`
```

`services/email.go` — add the constant near the top:

```go
// userInvitesPath is the staff page that lists workspace invites; a
// delivery-problem alert for an invite email links there.
const userInvitesPath = "/config/users"
```

In `buildUserInviteNotification`, add `StatusLink: userInvitesPath,` to the returned struct (after `Link`/before `Channels` is fine).

Change the portal-invite pair (signature and body):

```go
func buildPortalInviteNotification(tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) NotificationRequest {
	return NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []RecipientTarget{{Email: recipientEmail}},
		ActorUserID: actorUserID,
		StatusLink:  statusLink,
		EventType:   "portal_user.invited",
		// … everything else unchanged …
```

```go
func SendPortalInviteEmailWithResult(ctx context.Context, tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) (NotificationResult, error) {
	return SendNotificationWithResult(ctx, buildPortalInviteNotification(tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink, expiryHours))
}

func SendPortalInviteEmail(ctx context.Context, tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) error {
	_, err := SendPortalInviteEmailWithResult(ctx, tenantID, inviteID, actorUserID, statusLink, recipientEmail, recipientName, workspaceName, setupLink, expiryHours)
	return err
}
```

and the customer-portal pair:

```go
func buildCustomerPortalInviteNotification(tenantID, resourceID, actorUserID, statusLink, recipientEmail, recipientName, tenantDisplayName, setupLink string) NotificationRequest {
	return NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []RecipientTarget{{Email: recipientEmail}},
		ActorUserID: actorUserID,
		StatusLink:  statusLink,
		// … rest unchanged …
```

```go
func SendCustomerPortalInviteEmailWithResult(ctx context.Context, tenantID, resourceID, actorUserID, statusLink, recipientEmail, recipientName, tenantDisplayName, setupLink string) (NotificationResult, error) {
	return SendNotificationWithResult(ctx, buildCustomerPortalInviteNotification(tenantID, resourceID, actorUserID, statusLink, recipientEmail, recipientName, tenantDisplayName, setupLink))
}

func SendCustomerPortalInviteEmail(ctx context.Context, tenantID, resourceID, actorUserID, statusLink, recipientEmail, recipientName, tenantDisplayName, setupLink string) error {
	_, err := SendCustomerPortalInviteEmailWithResult(ctx, tenantID, resourceID, actorUserID, statusLink, recipientEmail, recipientName, tenantDisplayName, setupLink)
	return err
}
```

Extend the doc comments of the three portal/customer-portal functions with: `// actorUserID is the staff member's control-plane identity id (what notify scopes by); with statusLink it lets notify alert them if the email bounces.`

`controllers/documents.go` — in `customerSendRequest`, add to the `services.NotificationRequest{…}` literal after `ResourceID: recordID,`:

```go
		StatusLink:  recordLink(meta.WorkflowKey, recordID),
```

`controllers/portal_access.go` — the `issueInvite` call becomes (the customer's staff route is built from the customer uuid; `actorIdentityID` is already a parameter):

```go
	res, merr := services.SendPortalInviteEmailWithResult(r.Context(), tenant.ID, invite.ID,
		actorIdentityID, recordLink(string(authz.ResourceCustomer), customerUUID),
		email, fullName, tenant.DisplayName, portalInviteLink(token), inviteExpiryHours())
```

`controllers/customer_portal_admin.go` — `PortalInvite` currently discards the 4th return of `authCRMByRecordID`; capture it and pass it. Change the first lines of the handler and the send call:

```go
	_, pool, key, actorIdentityID, ok := h.crm.authCRMByRecordID(w, r, recordID, authz.ActionUpdate)
```

```go
	res, sendErr := services.SendCustomerPortalInviteEmailWithResult(r.Context(), tenant.ID, strconv.Itoa(custInternalID),
		actorIdentityID, recordLink(string(authz.ResourceCustomer), recordID),
		req.Email, req.FullName, tenant.DisplayName, link)
```

(Before editing: read `authCRMByRecordID` in `controllers/crm.go:184` and confirm its 4th return is the caller's identity id — if it is something else, take the identity from `middleware.GetUserFromContext(r.Context())` `.ID` instead, as that function itself does.)

Update the existing call-sites, which the compiler will list:
- `services/email_test.go:100` → `buildPortalInviteNotification("tenant-1", "invite-1", "actor-1", "/crm/customer/c-1", "staffer@example.com", …)`;
  `:120` → `buildCustomerPortalInviteNotification("tenant-1", "42", "actor-1", "/crm/customer/c-1", "buyer@example.com", …)`;
  `:169` → `buildPortalInviteNotification("t", "id", "actor", "/crm/customer/c-1", "to@x.com", …)`;
  `:170` → `buildCustomerPortalInviteNotification("t", "id", "actor", "/crm/customer/c-1", "to@x.com", …)`.
- `controllers/email_preview_test.go:56` → `services.SendPortalInviteEmail(ctx, "t", "i", "actor", "/crm/customer/c-1", "pat@example.com", …)`; `:59` → `services.SendCustomerPortalInviteEmail(ctx, "t", "i", "actor", "/crm/customer/c-1", "pat@example.com", …)`.

- [ ] **Step 4: Run to verify it passes**

Run: `dgo sh -c 'go vet ./... && go test ./services/ ./controllers/ -count=1'`
Expected: PASS, including the four sub-tests of `TestInviteBuilders_ActorAndStatusLink`, `TestNotificationRequest_StatusLink…`, `TestCustomerSendRequest_CarriesStaffStatusLink`, and the unchanged email-golden/preview tests.

- [ ] **Step 5: Checkpoint** — stage `services/ controllers/documents.go controllers/portal_access.go controllers/customer_portal_admin.go controllers/documents_statuslink_test.go controllers/email_preview_test.go`; suggested message `feat(notify): send actor and statusLink with invite and document emails`.

---

### Task 4: Controller helpers, and persist ids at the send sites

**Files:**
- Create: `controllers/email_status.go`
- Create: `controllers/email_status_test.go`
- Modify: `controllers/tenant.go` (two send sites), `controllers/portal_access.go` (`issueInvite`)

**Interfaces:**
- Consumes: Task 1 (`services.EmailStatuses`, `services.SummarizeEmail`, `services.EmailSummary`, `services.EmailDeliveryStatus`), Task 2 setters.
- Produces (Task 5 uses these exact names):
  - `type emailStatusIndex map[string]services.EmailDeliveryStatus`
  - `func loadEmailStatuses(ctx context.Context, tenantID string, idLists ...[]string) emailStatusIndex`
  - `func requestEmailStatuses(r *http.Request, idLists ...[]string) emailStatusIndex` — takes the tenant from the request context; an empty index when there is none
  - `func (idx emailStatusIndex) summary(ids []string) services.EmailSummary`
  - `func withEmailSummary(view map[string]any, s services.EmailSummary) map[string]any`
  - `func persistNotifyIDs(ctx context.Context, what, rowID string, res services.NotificationResult, sendErr error, set func(context.Context, string, []string) error)`
  - view types `documentSendView{workflow.DocumentSend; services.EmailSummary}`, `userInviteView{tenancy.UserInvite; services.EmailSummary}`, builders `documentSendViews`, `userInviteViews`, `tenantInviteViews`

- [ ] **Step 1: Write the failing tests** — `controllers/email_status_test.go`:

```go
package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

func idxWith(entries map[string]string) emailStatusIndex {
	idx := emailStatusIndex{}
	for id, state := range entries {
		idx[id] = services.EmailDeliveryStatus{State: state, Recipient: id + "@example.com", Detail: "provider text", UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	}
	return idx
}

func TestEmailStatusIndex_Summary(t *testing.T) {
	idx := idxWith(map[string]string{"a": "delivered", "b": "bounced"})

	assert.Equal(t, services.EmailStateBounced, idx.summary([]string{"a", "b"}).Status)
	assert.Equal(t, services.EmailStateUnknown, idx.summary(nil).Status)
	assert.Equal(t, services.EmailStateUnknown, emailStatusIndex(nil).summary([]string{"a"}).Status, "a nil index (lookup skipped) reads as unknown, not a panic")
}

func TestWithEmailSummary_AddsCamelCaseKeysOnly(t *testing.T) {
	idx := idxWith(map[string]string{"a": "bounced"})
	view := withEmailSummary(map[string]any{"id": "row-1"}, idx.summary([]string{"a"}))

	raw, err := json.Marshal(view)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "row-1", got["id"], "existing keys are untouched")
	assert.Equal(t, "bounced", got["emailStatus"])
	assert.NotEmpty(t, got["emailStatusMessage"])
	assert.NotEmpty(t, got["emailStatusAt"])
	assert.Len(t, got["emailRecipients"], 1)
	assert.NotContains(t, string(raw), "provider text", "provider detail must never be serialised")
}

func TestWithEmailSummary_UnknownOmitsOptionalKeys(t *testing.T) {
	view := withEmailSummary(map[string]any{}, services.SummarizeEmail(nil, nil))

	assert.Equal(t, services.EmailStateUnknown, view["emailStatus"])
	assert.NotContains(t, view, "emailStatusMessage")
	assert.NotContains(t, view, "emailStatusAt")
	assert.NotContains(t, view, "emailRecipients")
}

func TestDocumentSendViews(t *testing.T) {
	sends := []workflow.DocumentSend{
		{ID: "s1", SentTo: "x@y.com", NotifyNotificationIDs: []string{"a", "b"}},
		{ID: "s2", SentTo: "z@y.com"}, // predates notify ids
	}
	views := documentSendViews(sends, idxWith(map[string]string{"a": "delivered", "b": "bounced"}))

	require.Len(t, views, 2)
	assert.Equal(t, services.EmailStateBounced, views[0].Status)
	assert.Equal(t, services.EmailStateUnknown, views[1].Status)

	raw, err := json.Marshal(views[0])
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "s1", got["id"], "DocumentSend's own keys survive embedding")
	assert.Equal(t, "bounced", got["emailStatus"])
	assert.NotContains(t, string(raw), "provider text")

	assert.NotNil(t, documentSendViews(nil, nil), "an empty history serialises as [] not null")
	assert.Empty(t, documentSendViews(nil, nil))
}

func TestUserInviteViews_KeepLegacyPascalCaseAndAddCamelCase(t *testing.T) {
	invites := []tenancy.UserInvite{{ID: "i1", Email: "a@b.com", NotifyNotificationIDs: []string{"a"}}}
	views := userInviteViews(invites, idxWith(map[string]string{"a": "delayed"}))

	raw, err := json.Marshal(views[0])
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "i1", got["ID"], "the existing PascalCase keys the UI reads are unchanged")
	assert.Equal(t, "a@b.com", got["Email"])
	assert.Equal(t, "delayed", got["emailStatus"])
}

func TestTenantInviteViews(t *testing.T) {
	invites := []tenancy.Invite{{ID: "t1", ContactEmail: "owner@acme.com", Token: "tok", NotifyNotificationIDs: []string{"a"}}}
	views := tenantInviteViews(invites, idxWith(map[string]string{"a": "suppressed"}))

	require.Len(t, views, 1)
	assert.Equal(t, "t1", views[0]["id"])
	assert.Equal(t, "owner@acme.com", views[0]["contactEmail"])
	assert.Equal(t, "suppressed", views[0]["emailStatus"])
}

func TestPersistNotifyIDs(t *testing.T) {
	var gotRow string
	var gotIDs []string
	set := func(_ context.Context, id string, ids []string) error { gotRow, gotIDs = id, ids; return nil }

	t.Run("stores ids after a clean send", func(t *testing.T) {
		gotRow, gotIDs = "", nil
		persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{NotificationIDs: []string{"n1"}}, nil, set)
		assert.Equal(t, "row-1", gotRow)
		assert.Equal(t, []string{"n1"}, gotIDs)
	})

	t.Run("does nothing when the send failed", func(t *testing.T) {
		gotRow, gotIDs = "", nil
		persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{NotificationIDs: []string{"n1"}}, errors.New("notify down"), set)
		assert.Empty(t, gotRow)
	})

	t.Run("does nothing when notify returned no ids", func(t *testing.T) {
		gotRow, gotIDs = "", nil
		persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{}, nil, set)
		assert.Empty(t, gotRow)
	})

	t.Run("a persistence failure is swallowed — the send already happened", func(t *testing.T) {
		failing := func(context.Context, string, []string) error { return errors.New("db down") }
		assert.NotPanics(t, func() {
			persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{NotificationIDs: []string{"n1"}}, nil, failing)
		})
	})
}
```

- [ ] **Step 2: Run to verify failure**

Run: `dgo go test ./controllers/ -run 'TestEmailStatusIndex|TestWithEmailSummary|TestDocumentSendViews|TestUserInviteViews|TestTenantInviteViews|TestPersistNotifyIDs' -count=1`
Expected: FAIL to compile — `undefined: emailStatusIndex` etc.

- [ ] **Step 3: Implement** — `controllers/email_status.go`:

```go
package controllers

import (
	"context"
	"log/slog"
	"net/http"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// This file holds the glue between list handlers and services.EmailStatuses:
// one batched lookup per request, a worst-of summary per row, and JSON views
// that add the emailStatus… keys without disturbing a row's existing shape.
// Every helper is fail-open — a missing index reads as "unknown".

// emailStatusIndex maps a notify notification id to what notify reported for it.
type emailStatusIndex map[string]services.EmailDeliveryStatus

// loadEmailStatuses asks notify about every id in idLists with one batched
// lookup. Callers pass only ids from rows they have already loaded under their
// normal auth chain; the lookup always names the tenant.
func loadEmailStatuses(ctx context.Context, tenantID string, idLists ...[]string) emailStatusIndex {
	var all []string
	for _, ids := range idLists {
		all = append(all, ids...)
	}
	return emailStatusIndex(services.EmailStatuses(ctx, tenantID, all))
}

// requestEmailStatuses is loadEmailStatuses for a tenant-scoped request: the
// tenant comes from the request context. Outside a tenant request it returns an
// empty index (rows then read as unknown) rather than failing the page.
func requestEmailStatuses(r *http.Request, idLists ...[]string) emailStatusIndex {
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		return emailStatusIndex{}
	}
	return loadEmailStatuses(r.Context(), tenant.ID, idLists...)
}

// summary reduces one send's notification ids to a client-facing summary.
func (idx emailStatusIndex) summary(ids []string) services.EmailSummary {
	return services.SummarizeEmail(ids, idx)
}

// withEmailSummary adds a summary's JSON keys to a map view and returns it.
// Optional keys are left out when empty, matching EmailSummary's omitempty tags.
func withEmailSummary(view map[string]any, s services.EmailSummary) map[string]any {
	view["emailStatus"] = s.Status
	if s.At != nil {
		view["emailStatusAt"] = s.At
	}
	if s.Message != "" {
		view["emailStatusMessage"] = s.Message
	}
	if len(s.Recipients) > 0 {
		view["emailRecipients"] = s.Recipients
	}
	return view
}

// documentSendView is a send-history row plus its real email outcome.
type documentSendView struct {
	workflow.DocumentSend
	services.EmailSummary
}

// documentSendViews attaches each send's email summary. The result is never
// nil, so an empty history serialises as [] rather than null.
func documentSendViews(sends []workflow.DocumentSend, idx emailStatusIndex) []documentSendView {
	out := make([]documentSendView, 0, len(sends))
	for _, s := range sends {
		out = append(out, documentSendView{DocumentSend: s, EmailSummary: idx.summary(s.NotifyNotificationIDs)})
	}
	return out
}

// userInviteView is a staff-invite row plus its real email outcome. The
// embedded UserInvite has no JSON tags, so its keys stay PascalCase (the UI
// depends on that); the added keys are camelCase.
type userInviteView struct {
	tenancy.UserInvite
	services.EmailSummary
}

// userInviteViews attaches each invite's email summary.
func userInviteViews(invites []tenancy.UserInvite, idx emailStatusIndex) []userInviteView {
	out := make([]userInviteView, 0, len(invites))
	for _, inv := range invites {
		out = append(out, userInviteView{UserInvite: inv, EmailSummary: idx.summary(inv.NotifyNotificationIDs)})
	}
	return out
}

// tenantInviteViews renders platform invites (via inviteView) plus their email summary.
func tenantInviteViews(invites []tenancy.Invite, idx emailStatusIndex) []map[string]any {
	out := make([]map[string]any, 0, len(invites))
	for _, inv := range invites {
		out = append(out, withEmailSummary(inviteView(inv), idx.summary(inv.NotifyNotificationIDs)))
	}
	return out
}

// notifyIDsOf collects the notification ids of rows for one batched lookup.
func notifyIDsOf[T any](rows []T, ids func(T) []string) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, ids(r))
	}
	return out
}

// persistNotifyIDs stores the notify ids of a just-sent email on its invite row
// so its real delivery status can be looked up later. Best-effort by design:
// the email has already gone, so a persistence failure is logged, never
// returned. Nothing is stored when the send failed or notify returned no ids.
func persistNotifyIDs(ctx context.Context, what, rowID string, res services.NotificationResult, sendErr error,
	set func(context.Context, string, []string) error) {
	if sendErr != nil || len(res.NotificationIDs) == 0 {
		return
	}
	if err := set(ctx, rowID, res.NotificationIDs); err != nil {
		slog.WarnContext(ctx, what+": persist notify ids failed (non-fatal)", "row_id", rowID, "error", err)
	}
}
```

(`notifyIDsOf` is generic over the row type to avoid four near-identical loops; `[T any]` here is a type parameter, not an `any`-typed API.)

- [ ] **Step 4: Run to verify the helper tests pass**

Run: `dgo sh -c 'go vet ./controllers/ && go test ./controllers/ -run "TestEmailStatusIndex|TestWithEmailSummary|TestDocumentSendViews|TestUserInviteViews|TestTenantInviteViews|TestPersistNotifyIDs" -count=1 -v'`
Expected: PASS.

- [ ] **Step 5: Persist ids at the three invite send sites**

`controllers/tenant.go`, first send (around line 454, in the create-tenant-with-invite path), immediately after `res, sendErr := services.SendOnboardingInviteEmailWithResult(…)`:

```go
	persistNotifyIDs(r.Context(), "tenant invite", invite.ID, res, sendErr, h.CP.SetInviteNotifyIDs)
```

second send (around line 756, in `tenantInvites` POST), immediately after its `res, sendErr := …` line:

```go
		persistNotifyIDs(r.Context(), "tenant invite resend", invite.ID, res, sendErr, h.CP.SetInviteNotifyIDs)
```

`controllers/portal_access.go`, in `issueInvite`, immediately after `res, merr := services.SendPortalInviteEmailWithResult(…)`:

```go
	persistNotifyIDs(r.Context(), "portal invite", invite.ID, res, merr, h.CP.SetPortalInviteNotifyIDs)
```

(The staff-invite sends in `user.go` already persist inline; leave them.)

- [ ] **Step 6: Verify the build and the existing flows**

Run: `dgo sh -c 'go vet ./... && go test ./... -count=1'`
Expected: PASS (all packages). Then the DB suite for the packages touched so far: `dgodb go test -tags dbtest ./tenancy/ ./controllers/ -count=1` — PASS (the existing outcome tests still pass: they never read the new columns).

- [ ] **Step 7: Checkpoint** — stage `controllers/email_status.go controllers/email_status_test.go controllers/tenant.go controllers/portal_access.go`; suggested message `feat(controllers): persist notify ids for tenant and portal invites; add email-status view helpers`.

---

### Task 5: Return `emailStatus…` on the four list endpoints

**Files:**
- Modify: `controllers/documents.go` (`Sends`)
- Modify: `controllers/user.go` (`ListInvites`)
- Modify: `controllers/portal_access.go` (`ListPortalUsers`)
- Modify: `controllers/tenant.go` (`tenantInvites` GET branch)
- Modify: `controllers/email_outcome_dbtest_test.go` (fake notify serves the status route; assertions)
- Modify: `controllers/user_invite_dbtest_test.go` (grant `user:read`; list assertion)

**Interfaces:**
- Consumes: Task 4 helpers. Produces the response contract the WebUI plan depends on:

| Endpoint | Row keys added |
|---|---|
| `GET …/records/{id}/document/sends` → `sends[]` | `emailStatus`, `emailStatusAt?`, `emailStatusMessage?`, `emailRecipients?` |
| `GET /api/tenant/invites` → `invites[]` | same (existing keys stay PascalCase) |
| `GET /api/tenant/customers/{uuid}/portal-users` → `portalUsers[]` | same, **only on rows that have an invite** (`inviteStatus != "none"`) |
| `GET /api/platform/tenants/{id}/invites` → `invites[]` | same |

`emailStatus` ∈ `queued sent retrying delayed delivered complained bounced failed suppressed skipped unknown`; `emailRecipients[]` is `{email, status}`.

- [ ] **Step 1: Extend the fake notify and write the failing DB assertions**

In `controllers/email_outcome_dbtest_test.go`, extend `notifyScenario` and `withFakeNotify`:

```go
type notifyScenario struct {
	createStatus   int
	emailStatus    string
	emailLastError string
	// statusState is the state POST /api/deliveries/email-status reports for
	// every id asked about ("" = answer with an empty map). statusHTTP makes
	// that route fail with the given HTTP status (0 = 200).
	statusState string
	statusHTTP  int
}
```

and a new `case` in the `switch` inside `withFakeNotify` (before `default`):

```go
		case r.Method == http.MethodPost && r.URL.Path == "/api/deliveries/email-status":
			if sc.statusHTTP != 0 {
				w.WriteHeader(sc.statusHTTP)
				return
			}
			var req struct {
				NotificationIDs []string `json:"notificationIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			statuses := map[string]any{}
			if sc.statusState != "" {
				for _, id := range req.NotificationIDs {
					statuses[id] = map[string]any{"state": sc.statusState, "recipient": "bob@buyer.example",
						"detail": providerRefusal, "updatedAt": "2026-10-01T12:00:00Z"}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"statuses": statuses}})
```

Add a `sendsHandler` to `docSendOutcomeEnv` (field + construction) so the history endpoint can be driven:

```go
type docSendOutcomeEnv struct {
	pool         *pgxpool.Pool
	handler      http.Handler
	sendsHandler http.Handler
	orderID      string
	request      func() *http.Request
	sendsRequest func() *http.Request
}
```

In `newDocSendOutcomeEnv`, beside `handler:`, add:

```go
		sendsHandler: tenancy.NewResolver(cp, router).Middleware(http.HandlerFunc(docOps.Sends)),
		sendsRequest: func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/records/"+order.ID+"/document/sends", nil)
			req.SetPathValue("id", order.ID)
			return req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
				middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}))
		},
```

Append the tests:

```go
// TestDocumentSends_ReportsRealEmailStatus_DB: after a send, the history shows
// what notify reports — and degrades to "unknown", never an error, when the
// status route is down.
func TestDocumentSends_ReportsRealEmailStatus_DB(t *testing.T) {
	tests := []struct {
		name       string
		sc         notifyScenario
		wantStatus string
		wantMsg    bool
	}{
		{"bounced", notifyScenario{emailStatus: "sent", statusState: "bounced"}, "bounced", true},
		{"delivered", notifyScenario{emailStatus: "sent", statusState: "delivered"}, "delivered", false},
		{"notify status route down", notifyScenario{emailStatus: "sent", statusHTTP: http.StatusInternalServerError}, "unknown", false},
		{"notify knows nothing", notifyScenario{emailStatus: "sent"}, "unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newDocSendOutcomeEnv(t)
			withFakeNotify(t, tt.sc)
			env.handler.ServeHTTP(httptest.NewRecorder(), env.request()) // creates one send

			rr := httptest.NewRecorder()
			env.sendsHandler.ServeHTTP(rr, env.sendsRequest())

			require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
			var resp struct {
				Sends []map[string]any `json:"sends"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
			require.Len(t, resp.Sends, 1)
			assert.Equal(t, tt.wantStatus, resp.Sends[0]["emailStatus"])
			if tt.wantMsg {
				assert.NotEmpty(t, resp.Sends[0]["emailStatusMessage"])
			} else {
				assert.NotContains(t, resp.Sends[0], "emailStatusMessage")
			}
			assert.NotContains(t, rr.Body.String(), "not verified", "provider detail must stay server-side")
			assert.NotContains(t, rr.Body.String(), "403")
		})
	}
}
```

In `controllers/user_invite_dbtest_test.go`, grant read in `newInviteTestEnv` (add one entry to the `[]authz.Grant`):

```go
			{Resource: authz.ResourceUser, Action: authz.ActionRead, Scope: authz.ScopeAll},
```

and add the helper + test:

```go
// list drives GET /api/tenant/invites through the tenancy resolver.
func (e *inviteTestEnv) list(t *testing.T) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/invites", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, e.payload))
	rec := httptest.NewRecorder()
	e.resolver.Middleware(http.HandlerFunc(e.ops.ListInvites)).ServeHTTP(rec, req)

	var resp struct {
		Invites []map[string]any `json:"invites"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return rec, resp.Invites
}

// TestUserOps_ListInvites_ReportsRealEmailStatus_DB: the invites tab shows what
// notify reports for each invite email, keeps its legacy PascalCase keys, and
// never errors when notify's status route is down.
func TestUserOps_ListInvites_ReportsRealEmailStatus_DB(t *testing.T) {
	env := newInviteTestEnv(t)

	for _, tt := range []struct {
		name       string
		sc         notifyScenario
		wantStatus string
	}{
		{"bounced", notifyScenario{emailStatus: "sent", statusState: "bounced"}, "bounced"},
		{"status route down", notifyScenario{emailStatus: "sent", statusHTTP: http.StatusInternalServerError}, "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withFakeNotify(t, tt.sc)
			email := inviteTestEmail("status")
			rec, _ := env.invite(t, map[string]any{"email": email, "fullName": "Status Invitee"})
			require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

			lrec, invites := env.list(t)

			require.Equal(t, http.StatusOK, lrec.Code, "body: %s", lrec.Body.String())
			var row map[string]any
			for _, inv := range invites {
				if inv["Email"] == email {
					row = inv
				}
			}
			require.NotNil(t, row, "the new invite is listed")
			assert.Equal(t, tt.wantStatus, row["emailStatus"])
			assert.NotEmpty(t, row["ID"], "legacy PascalCase keys are intact")
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `dgodb go test -tags dbtest ./controllers/ -run 'TestDocumentSends_ReportsRealEmailStatus_DB|TestUserOps_ListInvites_ReportsRealEmailStatus_DB' -count=1 -v`
Expected: FAIL — `emailStatus` is nil in the rows.

- [ ] **Step 3: Implement the four handlers**

`controllers/documents.go` — `Sends`: replace the response with views:

```go
	sends, err := workflow.ListDocumentSends(r.Context(), pool, recordID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list sends.")
		return
	}
	idx := requestEmailStatuses(r, notifyIDsOf(sends, func(s workflow.DocumentSend) []string { return s.NotifyNotificationIDs })...)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sends": documentSendViews(sends, idx)})
```

(The `_ = identityID` line above it stays.)

`controllers/user.go` — `ListInvites`: replace the tail:

```go
	invites, err := h.CP.ListUserInvitesByTenant(r.Context(), tenant.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list invites.")
		return
	}
	idx := loadEmailStatuses(r.Context(), tenant.ID,
		notifyIDsOf(invites, func(i tenancy.UserInvite) []string { return i.NotifyNotificationIDs })...)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invites": userInviteViews(invites, idx)})
```

(`userInviteViews` never returns nil, so the `if invites == nil { … }` guard is no longer needed — delete it.)

`controllers/portal_access.go` — `ListPortalUsers`: replace the per-user loop with two passes:

```go
	// Attach each login's invitation state so the UI can show "invited",
	// "expired" (with a resend button) or nothing at all, without the frontend
	// having to infer it. The invite emails' real delivery status is fetched
	// with ONE batched lookup for the whole list.
	invites := make([]*tenancy.PortalInvite, len(users))
	var idLists [][]string
	for i := range users {
		invite, ierr := h.CP.LatestPortalInviteForIdentity(r.Context(), tenant.ID, users[i].IdentityID)
		if ierr != nil {
			// No invite on record is normal for a customer who already had a
			// login at another workspace. Render the row without invite state.
			continue
		}
		invites[i] = invite
		idLists = append(idLists, invite.NotifyNotificationIDs)
	}
	idx := loadEmailStatuses(r.Context(), tenant.ID, idLists...)

	views := make([]map[string]any, 0, len(users))
	for i := range users {
		view := portalUserView(&users[i], invites[i])
		if invites[i] != nil {
			withEmailSummary(view, idx.summary(invites[i].NotifyNotificationIDs))
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "portalUsers": views})
```

`controllers/tenant.go` — `tenantInvites` GET branch: replace the view-building loop:

```go
		invites, err := h.CP.ListInvitesByTenant(r.Context(), tenantID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load invites.")
			return
		}
		idx := loadEmailStatuses(r.Context(), tenantID,
			notifyIDsOf(invites, func(i tenancy.Invite) []string { return i.NotifyNotificationIDs })...)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "invites": tenantInviteViews(invites, idx)})
```

(The tenant id passed to notify is the **invited** tenant's — `SendOnboardingInviteEmailWithResult` was called with `tenant.ID` of that tenant, so that is the scope notify stored the notification under.)

- [ ] **Step 4: Run to verify the DB tests pass**

Run: `dgodb go test -tags dbtest ./controllers/ -run 'TestDocumentSends_ReportsRealEmailStatus_DB|TestUserOps_ListInvites_ReportsRealEmailStatus_DB|EmailOutcome|InviteUser' -count=1 -v`
Expected: PASS, including the unchanged `TestUserInvite_EmailOutcome_DB`, `TestDocumentSend_EmailOutcome_DB` and `TestUserOps_InviteUser_ConflictMessages_DB` (the added `user:read` grant must not disturb them).

Then the whole backend: `dgo sh -c 'go vet ./... && go test ./... -count=1'` — PASS (apart from the known CRLF-only `TestGeneratedAPIDocsAreCurrent`; this plan adds no routes).

- [ ] **Step 5: Security review** — run the `tenancy-security-reviewer` agent over `controllers/documents.go`, `user.go`, `portal_access.go`, `tenant.go` and `email_status.go`. The points to confirm: every id passed to `loadEmailStatuses` comes from a row loaded after the handler's existing auth/scope check; the tenant is always explicit; no provider detail reaches a response. Expected: no findings.

- [ ] **Step 6: Checkpoint** — stage `controllers/`; suggested message `feat(controllers): show real email delivery status on send history and invite lists`.

---

### Task 6: Help text, engineering rule, full verification

**Files:**
- Modify: `docs/sales-documents.md`, `docs/administration.md`, `docs/customer-portal.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: End-user help** (these files are embedded into the assistant's corpus: no `/api/`, no code fences, no table or service names, `TestHelpCorpusIsUserFacing` must stay green).

`docs/sales-documents.md` — append to the end of the "Sending a document by email" section (before `## Exporting a document as a PDF`):

```markdown

After you send, the document's send history shows what happened to each email:
Sent means the email was handed to the recipient's mail provider, Delivered
means it reached their mailbox, and Delayed, Bounced, or Marked as spam point
to a problem. If something goes wrong you also get a notification in the bell
menu. A bounce usually means the email address is wrong — correct it on the
customer and send again.
```

`docs/administration.md` — append to the "Users and invites" section (after the paragraph ending "…use Deactivate account to remove their access."):

```markdown

Each invitation on the Invites tab shows whether its email was delivered. If
an invitation bounces or is delayed you'll get a notification, and you can
correct the address and send it again.
```

`docs/customer-portal.md` — append to the end of the "Managing a customer's portal logins" section:

```markdown

An invitation that hasn't been accepted yet also shows whether its email was
delivered, so you can tell the difference between a customer who hasn't acted
on it and an email that never arrived. If it bounced, check the contact email
on the customer and resend.
```

- [ ] **Step 2: Engineering rule** — in `CLAUDE.md`, under "Notifications", add item 6:

```markdown
6. **"Sent" is not "delivered" — show what Resend reported.** `ConfirmEmailDelivery`
   only covers the first attempt. The real outcome (delivered / delayed / bounced /
   complained / suppressed) arrives later as a Resend webhook into `stonesuite-notify`
   and is read back with `services.EmailStatuses` (one batched, **fail-open** call) and
   `services.SummarizeEmail` (worst-of fold, client-safe message). A list that shows
   emails you sent must (a) persist the `notify_notification_ids` of each send
   (`persistNotifyIDs` — reset them on resend), (b) add the summary via the
   `controllers/email_status.go` views, never hand-rolled, and (c) never return notify's
   `Detail` to a client. Every email with a user behind it sets `ActorUserID` (the
   control-plane identity id) and a staff-side `StatusLink`, so notify can alert them if
   it bounces. Platform onboarding invites are the one deliberate exception (no tenant
   actor). The CRM-record portal invite (`customer_identities`) persists no ids — it has
   no list UI to show them.
```

- [ ] **Step 3: Verify the help corpus guard and the whole suite**

Run: `dgo sh -c 'go vet ./... && go test ./docs/ ./services/ ./controllers/ ./tenancy/ -count=1 && go build ./...'`
Expected: PASS (`TestHelpCorpusIsUserFacing` included). The help corpus re-ingests itself on next boot (hash changes) — nothing to run.

- [ ] **Step 4: Full DB suite, as CI does it**

```bash
dgodb go test -tags dbtest ./tenancy/ ./controllers/ ./workflow/ -count=1
```

Expected: PASS (recreate `t_tests` between packages if a package complains about leftover rows, as CI does).

- [ ] **Step 5: Report** — summarise: tests run and counts; the four endpoints and the JSON contract (copy the Task 5 table into the report for the WebUI work); the three spec deviations at the top of this plan so the user can approve updating the spec; that nothing was committed; the deploy order (notify → backend → WebUI) from spec §11. Suggest one cross-service check after deployment: send a document to `bounced@resend.dev` and watch the badge and the bell.

---

## Self-Review (run against the spec before handing off)

**Spec coverage (§5):**
- §5.1 `EmailStatuses` fail-open, batched, chunked, timeout; worst-of fold with client-safe messages and per-recipient list → Task 1.
- §5.2 persist ids where missing → Task 2 (`tenant_invites`, `portal_invites`) + Task 4 Step 5 (send sites). `customer_identities` dropped — documented deviation 1.
- §5.3 actor and `StatusLink` on every email with a user → Task 3 (staff invite, portal invite, customer-portal invite, document send); platform onboarding intentionally excluded — deviation 3.
- §5.4 API shape → Task 5 (four endpoints; `emailStatus`, `emailStatusAt`, `emailStatusMessage`, `emailRecipients`); rows with no ids ⇒ `unknown`; one lookup per request.
- §5.5 help docs → Task 6.
- §7 security (no provider text to clients, no new IDOR path, tenant explicit) → Global Constraints, Task 1 test `assert.NotContains(…"provider text")`, Task 5 DB assertions and the `tenancy-security-reviewer` step.
- §8 failure modes (notify down ⇒ unknown) → Task 1 (`TestEmailStatuses_IsFailOpen`, failure table) and Task 5 (`status route down` cases).
- `docs/openapi.json`: unaffected — no routes added (the generator reads routes from `main.go`); Task 5 Step 4 runs the whole suite to confirm.

**Placeholder scan:** none; the two places that say "read X first and confirm" (Task 3 Step 3, the `authCRMByRecordID` return value; Task 2 Step 4's `grep` for other `tenant_invites` readers) are explicit pre-edit checks with the fallback spelled out.

**Type consistency:** `EmailDeliveryStatus{State, Recipient, Detail, UpdatedAt}`, `EmailSummary{Status, At, Message, Recipients}`, `emailStatusIndex.summary`, `loadEmailStatuses`/`requestEmailStatuses`, `withEmailSummary`, `persistNotifyIDs`, `documentSendViews`/`userInviteViews`/`tenantInviteViews`, `SetInviteNotifyIDs`/`SetPortalInviteNotifyIDs`, and the five-argument-longer portal/customer-portal sender signatures are used identically in Tasks 1–6. The state vocabulary matches the notify plan's `DeriveState` output.
