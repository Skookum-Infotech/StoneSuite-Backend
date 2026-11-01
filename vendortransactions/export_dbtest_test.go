//go:build dbtest

package vendortransactions

import (
	"bytes"
	"context"
	"encoding/csv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func exportRowsFor(t *testing.T, a testActor, vendorUUID string, mod func(*Params)) []Row {
	t.Helper()
	p := Params{IdentityID: a.identityID, VendorUUID: vendorUUID, Today: dbToday}
	if mod != nil {
		mod(&p)
	}
	rows, _, err := ExportRows(context.Background(), dbPool(t), p)
	require.NoError(t, err)
	return rows
}

func TestExport_MatchesListForSameFilters(t *testing.T) {
	pool := dbPool(t)
	own := seedActor(t, pool, "exp-own", authz.Grant{Resource: authz.ResourceVendorBill, Action: authz.ActionRead, Scope: authz.ScopeOwn})
	all := seedActor(t, pool, "exp-all", readAll()...)
	for _, a := range []testActor{own, all} {
		vUUID, _ := seedMixedVendor(t, a)
		for _, mod := range []func(*Params){nil, func(p *Params) { p.OpenOnly = true }, func(p *Params) { p.Type = "vendor_bill" }} {
			want := listAll(t, a, vUUID, mod)
			got := exportRowsFor(t, a, vUUID, mod)
			assert.Equal(t, want, got)
		}
	}
}

// The feed has no vendor-name column, so the document number is the only
// free-text field exported from user-controlled data. Numeric Amount and Balance
// cells are intentionally not escaped (a negative amount starts with '-').
func TestExport_NumberStartingWithFormulaIsEscaped(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "exp-formula", readAll()...)
	vUUID, vID := seedMixedVendor(t, a)
	for _, tr := range formulaTriggers {
		t.Run(tr.name, func(t *testing.T) {
			d := seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-02", total: 1, balance: 1, owner: a.employee})
			num := tr.char + `HYPERLINK("http://evil","x")`
			_, err := pool.Exec(context.Background(), `UPDATE vendor_bill SET vendor_bill_number = $1 WHERE vendor_bill_id = $2`, num, d.id)
			require.NoError(t, err)

			var buf bytes.Buffer
			require.NoError(t, WriteCSV(&buf, exportRowsFor(t, a, vUUID, nil)))
			recs, err := csv.NewReader(&buf).ReadAll()
			require.NoError(t, err)
			found := false
			for _, rec := range recs[1:] {
				if rec[1] != "" {
					assert.NotContains(t, formulaTriggerChars, rec[1][:1], "no number cell may start with a trigger")
				}
				if rec[1] == "'"+num {
					found = true
				}
			}
			assert.True(t, found)
		})
	}
}

// formulaTriggerChars is every character that must never begin an exported text cell.
const formulaTriggerChars = "=+-@	\r"
