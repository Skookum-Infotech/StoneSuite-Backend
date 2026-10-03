//go:build dbtest

package docextractjob

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

func TestChecker_SamePOAndRevision(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	short := tag[len(tag)-9:] // document numbers are VARCHAR(20)
	cust := seedCustomer(t, pool, "Dupco"+tag, "CACT")
	other := seedCustomer(t, pool, "Elseco"+tag, "CACT")
	open := seedSalesOrder(t, pool, cust, "SORD-9"+short, "PO-0"+tag, "OPEN", 12000)
	closed := seedSalesOrder(t, pool, cust, "SORD-8"+short, "CLOSED-1"+tag, "FILL", 500)
	seedSalesOrder(t, pool, other, "SORD-7"+short, "PO-0"+tag, "OPEN", 12000)
	c := NewChecker(pool)

	t.Run("same customer and normalized PO, total as extra signal", func(t *testing.T) {
		got, err := c.Check(ctx, DuplicateInput{CustomerUUID: cust, PONumber: "#" + tag, TotalCents: 12000, HasTotal: true})
		require.NoError(t, err)
		require.Len(t, got, 1, "the other customer's identical PO must not match")
		assert.Equal(t, DupSamePO, got[0].Kind)
		assert.Equal(t, open, got[0].RecordUUID)
		assert.Contains(t, got[0].Reason, "same total")
	})
	t.Run("no customer means no PO match", func(t *testing.T) {
		got, err := c.Check(ctx, DuplicateInput{PONumber: tag})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("revision of an open order", func(t *testing.T) {
		got, err := c.Check(ctx, DuplicateInput{CustomerUUID: cust, PONumber: tag,
			Revision: &docextract.Revision{Label: "Revision 2"}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, DupRevision, got[0].Kind)
		assert.Equal(t, open, got[0].RecordUUID)
		assert.Equal(t, "Open", got[0].Status)
	})
	t.Run("revision of a closed order found by referenced number says manual", func(t *testing.T) {
		got, err := c.Check(ctx, DuplicateInput{CustomerUUID: cust, PONumber: "new-po",
			Revision: &docextract.Revision{Label: "Amended", ReferencedNumber: "sord-8" + short}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, closed, got[0].RecordUUID)
		assert.Contains(t, got[0].Reason, "handled manually")
	})
}

func TestChecker_QuoteEstimateReferences(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	short := tag[len(tag)-9:] // document numbers are VARCHAR(20)
	cust := seedCustomer(t, pool, "Refco"+tag, "CACT")

	var quote string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO quote (record_type, quote_status, quote_customer_id, quote_number, quote_created_by)
		SELECT rt.record_type_id, rs.record_status_id, c.customer_id, $2, 1
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = 'DRFT'
		JOIN customer c ON c.customer_uuid = $1::uuid
		WHERE rt.record_type_code = 'QUOT'
		RETURNING quote_uuid::text`, cust, "QUOT-Z"+short).Scan(&quote))

	got, err := NewChecker(pool).References(ctx, "quot-z"+short)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, DupQuoteRef, got[0].Kind)
	assert.Equal(t, quote, got[0].RecordUUID)
	assert.Equal(t, "Draft", got[0].Status)

	none, err := NewChecker(pool).References(ctx, "QUOT-NOPE"+short)
	require.NoError(t, err)
	assert.Empty(t, none)
}
