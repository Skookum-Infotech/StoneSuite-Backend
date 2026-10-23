//go:build dbtest

package recordemail

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileMail records a received mail and returns the record id.
func fileMail(t *testing.T, providerID string) (rec string) {
	t.Helper()
	pool, ctx := tenantPool(t), context.Background()
	rec = newID(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM record_email_message WHERE record_id = $1`, rec)
	})
	ok, err := Insert(ctx, pool, inbound(rec, providerID+rec, "<"+providerID+"@x>", "p@gmail.com", "See attached"))
	require.NoError(t, err)
	require.True(t, ok)
	return rec
}

func received() []ReceivedFile {
	return []ReceivedFile{
		{ProviderID: "att-pdf", Name: "quote.pdf", ContentType: "application/pdf", Size: 1024},
		{ProviderID: "att-exe", Name: "setup.exe", ContentType: "application/octet-stream", Size: 10},
		{ProviderID: "att-big", Name: "scan.tiff", ContentType: "image/tiff", Size: InboundMaxFileBytes + 1},
		{ProviderID: "att-logo", Name: "logo.png", ContentType: "image/png", Size: 300, Inline: true},
	}
}

func TestFileReceived_RecordsEveryFileAndReturnsOnlyThePendingOnes(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_a_")

	pending, err := FileReceived(ctx, pool, rec, "em_a_"+rec, received(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"att-pdf", "att-logo"}, pending, "a blocked and an oversized file are recorded but not copied")

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	got := map[string]Attachment{}
	for _, a := range msgs[0].Attachments {
		got[a.FileName] = a
	}
	require.Len(t, got, 4)
	assert.Equal(t, AttachmentPending, got["quote.pdf"].Status)
	assert.Equal(t, AttachmentBlocked, got["setup.exe"].Status)
	assert.Equal(t, AttachmentTooLarge, got["scan.tiff"].Status)
	assert.True(t, got["logo.png"].Inline)
	assert.Equal(t, int64(1024), got["quote.pdf"].SizeBytes)
	assert.Empty(t, got["quote.pdf"].StorageKey, "nothing is copied yet")
}

func TestFileReceived_ARetryReturnsWhatIsStillPendingAndDuplicatesNothing(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_b_")
	first, err := FileReceived(ctx, pool, rec, "em_b_"+rec, received(), true)
	require.NoError(t, err)
	require.Len(t, first, 2)

	// One copy finishes before the delivery is retried.
	att, err := ReceivedAttachment(ctx, pool, rec, "att-pdf")
	require.NoError(t, err)
	kept, err := MarkStored(ctx, pool, att.ID, "acme/quote/"+rec+"/obj.pdf", "application/pdf", 1024)
	require.NoError(t, err)
	require.True(t, kept)

	again, err := FileReceived(ctx, pool, rec, "em_b_"+rec, received(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"att-logo"}, again, "the stored file is not asked for again")

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	assert.Len(t, msgs[0].Attachments, 4, "no second copy of any file")
}

func TestFileReceived_WithoutStorageNothingIsPending(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_c_")

	pending, err := FileReceived(ctx, pool, rec, "em_c_"+rec, received(), false)
	require.NoError(t, err)
	assert.Empty(t, pending)

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	statuses := map[string]string{}
	for _, a := range msgs[0].Attachments {
		statuses[a.FileName] = a.Status
	}
	assert.Equal(t, AttachmentUnavailable, statuses["quote.pdf"])
	assert.Equal(t, AttachmentBlocked, statuses["setup.exe"], "blocked is blocked whether or not there is storage")
	assert.Equal(t, AttachmentTooLarge, statuses["scan.tiff"])
}

func TestFileReceived_AMailFiledUnderAnotherDeliveryHasNothingToAttachTo(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_d_")

	pending, err := FileReceived(ctx, pool, rec, "em_unknown", received(), true)
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.NotNil(t, pending, "an empty list, so it reads as [] in the response")
}

func TestList_AMessageWithNoFilesHasAnEmptyListNotNull(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_e_")

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.NotNil(t, msgs[0].Attachments)
	assert.Empty(t, msgs[0].Attachments)
}

func TestList_APendingFileThatNeverFinishedReadsAsUnavailable(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_f_")
	_, err := FileReceived(ctx, pool, rec, "em_f_"+rec, received()[:1], true)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE record_email_attachment SET created_at = now() - interval '2 days' WHERE record_id = $1`, rec)
	require.NoError(t, err)

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	assert.Equal(t, AttachmentUnavailable, msgs[0].Attachments[0].Status, "the thread must not say Copying… forever")

	// But a copy that finishes late is still accepted: the real state is pending.
	att, err := ReceivedAttachment(ctx, pool, rec, "att-pdf")
	require.NoError(t, err)
	assert.Equal(t, AttachmentPending, att.Status)
}

func TestGetAttachment_OnlyWithinItsOwnRecord(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_g_")
	other := fileMail(t, "em_g2_")
	_, err := FileReceived(ctx, pool, rec, "em_g_"+rec, received()[:1], true)
	require.NoError(t, err)
	att, err := ReceivedAttachment(ctx, pool, rec, "att-pdf")
	require.NoError(t, err)

	got, err := GetAttachment(ctx, pool, rec, att.ID)
	require.NoError(t, err)
	assert.Equal(t, "quote.pdf", got.FileName)

	_, err = GetAttachment(ctx, pool, other, att.ID)
	assert.ErrorIs(t, err, ErrAttachmentNotFound, "another record's id must not reveal the file")
	_, err = GetAttachment(ctx, pool, rec, "not-a-uuid")
	assert.ErrorIs(t, err, ErrAttachmentNotFound)
	_, err = GetAttachment(ctx, pool, rec, newID(t, pool))
	assert.ErrorIs(t, err, ErrAttachmentNotFound)
}

func TestMarkStoredAndSetStatus_OnlyMovePendingFiles(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_h_")
	_, err := FileReceived(ctx, pool, rec, "em_h_"+rec, received()[:1], true)
	require.NoError(t, err)
	att, err := ReceivedAttachment(ctx, pool, rec, "att-pdf")
	require.NoError(t, err)

	kept, err := MarkStored(ctx, pool, att.ID, "k1", "application/pdf", 1024)
	require.NoError(t, err)
	assert.True(t, kept)
	kept, err = MarkStored(ctx, pool, att.ID, "k2", "application/pdf", 1024)
	require.NoError(t, err)
	assert.False(t, kept, "a file already stored is not stored twice")

	require.NoError(t, SetStatus(ctx, pool, att.ID, AttachmentUnavailable))
	again, err := GetAttachment(ctx, pool, rec, att.ID)
	require.NoError(t, err)
	assert.Equal(t, AttachmentStored, again.Status, "a settled file does not move again")
	assert.Equal(t, "k1", again.StorageKey)
}

func TestStoredBytes_CountsOnlyKeptFiles(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := fileMail(t, "em_i_")
	_, err := FileReceived(ctx, pool, rec, "em_i_"+rec, received(), true)
	require.NoError(t, err)

	none, err := StoredBytes(ctx, pool, rec)
	require.NoError(t, err)
	assert.Zero(t, none, "pending, blocked and oversized files take no space")

	att, err := ReceivedAttachment(ctx, pool, rec, "att-pdf")
	require.NoError(t, err)
	_, err = MarkStored(ctx, pool, att.ID, "k", "application/pdf", 1024)
	require.NoError(t, err)
	total, err := StoredBytes(ctx, pool, rec)
	require.NoError(t, err)
	assert.Equal(t, int64(1024), total)
}

func TestInsertWithFiles_WritesTheMessageAndItsFilesTogether(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := newID(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM record_email_message WHERE record_id = $1`, rec)
	})
	msgID := newID(t, pool)
	msg := outbound(rec, "priya@gmail.com", "Attached.")
	msg.ID, msg.AttachmentCount = msgID, 1

	err := InsertWithFiles(ctx, pool, msg, []Attachment{{
		MessageID: msgID, RecordID: rec, FileName: "plan.pdf", ContentType: "application/pdf",
		SizeBytes: 2048, Status: AttachmentStored, StorageKey: "acme/sales_order/" + rec + "/obj.pdf",
	}})
	require.NoError(t, err)

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, msgID, msgs[0].ID, "the id the caller chose is the one stored")
	require.Len(t, msgs[0].Attachments, 1)
	assert.Equal(t, "plan.pdf", msgs[0].Attachments[0].FileName)
	assert.Equal(t, AttachmentStored, msgs[0].Attachments[0].Status)
}

func TestInsertWithFiles_RollsBackTheMessageWhenAFileCannotBeRecorded(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := newID(t, pool)
	msgID := newID(t, pool)
	msg := outbound(rec, "priya@gmail.com", "Attached.")
	msg.ID = msgID

	// The file points at a message that does not exist, which the foreign key refuses.
	err := InsertWithFiles(ctx, pool, msg, []Attachment{{
		MessageID: newID(t, pool), RecordID: rec, FileName: "plan.pdf", Status: AttachmentStored,
	}})

	require.Error(t, err)
	msgs, listErr := List(ctx, pool, rec)
	require.NoError(t, listErr)
	assert.Empty(t, msgs, "no message may exist with only some of its files")
}
