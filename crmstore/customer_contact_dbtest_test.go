//go:build dbtest

package crmstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomerDoNotContact(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	flagged := createRecord(t, pool, "customer", uniqueName("DNC Co"))
	_, err := pool.Exec(ctx, `UPDATE customer SET customer_do_not_contact = TRUE WHERE customer_uuid = $1`, flagged.ID)
	require.NoError(t, err)
	open := createRecord(t, pool, "customer", uniqueName("Open Co"))
	deleted := createRecord(t, pool, "customer", uniqueName("Gone Co"))
	_, err = pool.Exec(ctx, `UPDATE customer SET customer_do_not_contact = TRUE WHERE customer_uuid = $1`, deleted.ID)
	require.NoError(t, err)
	require.NoError(t, For("v2").DeleteRecord(ctx, pool, deleted.ID))

	cases := []struct {
		name string
		uuid string
		want bool
	}{
		{"flagged customer", flagged.ID, true},
		{"unflagged customer", open.ID, false},
		{"deleted customer", deleted.ID, false},
		{"missing customer", "00000000-0000-0000-0000-000000000000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CustomerDoNotContact(ctx, pool, tc.uuid)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
