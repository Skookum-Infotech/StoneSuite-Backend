//go:build dbtest

package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/recordemail"
	"stonesuite-backend/tenancy"
)

// These drive the real inbound route against two real databases (a control
// plane for the reply addresses, a tenant database for the conversation): a
// customer's reply, as stonesuite-notify forwards it, is filed under the right
// record exactly once, and mail that is not ours to file stores nothing.

func inboundPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Skipf("%s not set; skipping dbtest", env)
	}
	pool, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

type fakeTenantLookup struct{ tenant *tenancy.Tenant }

func (f fakeTenantLookup) TenantByID(context.Context, string) (*tenancy.Tenant, error) {
	return f.tenant, nil
}

type inboundFixture struct {
	cp, tenantDB *pgxpool.Pool
	tenantID     string
	recordID     string
	token        string
	ops          *RecordEmailOps
	sent         int
}

func newInboundFixture(t *testing.T, status string) *inboundFixture {
	t.Helper()
	ctx := context.Background()
	f := &inboundFixture{cp: inboundPool(t, "TEST_CP_DATABASE_URL"), tenantDB: inboundPool(t, "TEST_DATABASE_URL")}

	require.NoError(t, f.cp.QueryRow(ctx,
		`INSERT INTO tenants (slug, display_name) VALUES ($1, 'Inbound Test') RETURNING id::text`,
		fmt.Sprintf("inbound-%d", time.Now().UnixNano())).Scan(&f.tenantID))
	t.Cleanup(func() { _, _ = f.cp.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, f.tenantID) })
	require.NoError(t, f.tenantDB.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&f.recordID))

	store := recordemail.NewAddressStore(f.cp)
	var err error
	f.token, err = store.TokenFor(ctx, f.tenantID, f.recordID, "sales_order")
	require.NoError(t, err)

	tenant := &tenancy.Tenant{ID: f.tenantID, Status: status}
	f.ops = NewRecordEmailOps(store, testInboundDomain).WithInbound(testInboundSecret, fakeTenantLookup{tenant},
		func(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error) { return f.tenantDB, nil })
	return f
}

// post delivers mail the way notify does. Every call is signed at a later second
// than the last, as each of notify's retries is: an identical timestamp would be
// (rightly) refused as a replay.
func (f *inboundFixture) post(t *testing.T, mail recordemail.InboundMail) *httptest.ResponseRecorder {
	t.Helper()
	f.sent++
	ts := time.Now().Add(time.Duration(f.sent) * time.Second)
	rec := httptest.NewRecorder()
	f.ops.Inbound(rec, signedInbound(t, testInboundSecret, inboundJSON(t, mail), ts))
	return rec
}

func (f *inboundFixture) stored(t *testing.T) []recordemail.Message {
	t.Helper()
	msgs, err := recordemail.List(context.Background(), f.tenantDB, f.recordID)
	require.NoError(t, err)
	return msgs
}

func (f *inboundFixture) replyTo() string { return recordemail.Address(f.token, testInboundDomain) }

// reply is a customer's answer addressed to the given recipients.
func (f *inboundFixture) reply(emailID string, to ...string) recordemail.InboundMail {
	return recordemail.InboundMail{
		EmailID:   emailID,
		From:      "Priya Shah <priya@gmail.com>",
		To:        to,
		Subject:   "Re: Your Sales Order SO-1",
		MessageID: "<" + emailID + "@mail.gmail.com>",
		Text:      "Looks good, go ahead.\n\nOn Thu, Oct 15, 2026 at 9:00 AM Acme <o@acme.com> wrote:\n> Please confirm the slabs.",
		InReplyTo: "<sent@resend>",
	}
}

func TestInboundDB_FilesTheReplyUnderItsRecordExactlyOnce(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	mail := f.reply("em_1_"+f.token, "Acme Orders <"+f.replyTo()+">")
	mail.AttachmentCount = 2

	first := f.post(t, mail)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	msgs := f.stored(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, recordemail.DirectionIn, msgs[0].Direction)
	assert.Equal(t, "Looks good, go ahead.", msgs[0].Body, "quoted history is stripped")
	assert.Equal(t, "priya@gmail.com", msgs[0].FromEmail)
	assert.Equal(t, "Priya Shah", msgs[0].FromName)
	assert.Equal(t, "sales_order", msgs[0].WorkflowKey)
	assert.Equal(t, 2, msgs[0].AttachmentCount)

	// notify forwarding the same mail again (Resend retried its webhook) must not
	// duplicate the message.
	second := f.post(t, mail)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Len(t, f.stored(t), 1)
}

func TestInboundDB_ACopiedAddressStillFilesTheMail(t *testing.T) {
	t.Run("in To alongside the rep", func(t *testing.T) {
		f := newInboundFixture(t, tenancy.StatusActive)
		// The customer replied to the rep and copied the order's address: it is
		// among several recipients, not the only one.
		rec := f.post(t, f.reply("em_cc_"+f.token, "rep@acme.com", f.replyTo()))

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Len(t, f.stored(t), 1)
	})

	t.Run("in Bcc", func(t *testing.T) {
		f := newInboundFixture(t, tenancy.StatusActive)
		mail := f.reply("em_bcc_"+f.token, "rep@acme.com")
		mail.BCC = []string{f.replyTo()}

		rec := f.post(t, mail)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Len(t, f.stored(t), 1)
	})
}

func TestInboundDB_StoresNothingForMailThatIsNotOurs(t *testing.T) {
	t.Run("unknown token", func(t *testing.T) {
		f := newInboundFixture(t, tenancy.StatusActive)
		rec := f.post(t, f.reply("em_x_"+f.token, "r-aaaaaaaaaaaaaaaaaaaa@"+testInboundDomain))
		assert.Equal(t, http.StatusOK, rec.Code, "acknowledged, so notify does not retry it forever")
		assert.Empty(t, f.stored(t))
	})

	t.Run("suspended tenant", func(t *testing.T) {
		f := newInboundFixture(t, "suspended")
		rec := f.post(t, f.reply("em_s_"+f.token, f.replyTo()))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, f.stored(t))
	})
}

func TestInboundDB_AsksForARetryWhenTheWorkspaceCannotBeOpened(t *testing.T) {
	f := newInboundFixture(t, tenancy.StatusActive)
	f.ops.poolFor = func(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error) {
		return nil, errors.New("tenant database is down")
	}

	rec := f.post(t, f.reply("em_f_"+f.token, f.replyTo()))

	// Not a 2xx: notify must retry, because filing could succeed next time.
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, f.stored(t))
}
