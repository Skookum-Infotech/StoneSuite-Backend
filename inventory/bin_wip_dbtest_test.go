//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWIPBinPersistsMachineAndReturnsToStorage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var warehouse string
	require.NoError(t, pool.QueryRow(ctx, `SELECT company_location_uuid FROM company_location WHERE company_location_id=1`).Scan(&warehouse))
	in := BinInput{WarehouseUUID: warehouse, Code: uniq("WIP"), Name: "Saw work area", Type: "floor", IsActive: true, IsWIP: true, MachineLabel: "Saw 2"}
	bin, err := CreateBin(ctx, pool, in, 1)
	require.NoError(t, err)
	require.True(t, bin.IsWIP)
	require.Equal(t, "Saw 2", bin.MachineLabel)
	in.IsWIP, in.MachineLabel = false, ""
	require.NoError(t, UpdateBin(ctx, pool, bin.ID, in, 1))
	bin, err = GetBin(ctx, pool, bin.ID)
	require.NoError(t, err)
	require.False(t, bin.IsWIP)
	require.Empty(t, bin.MachineLabel)
}
