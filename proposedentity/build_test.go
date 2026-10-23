package proposedentity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/authz"
	"stonesuite-backend/proposedaction"
)

// fakeRows serves lkp_unit rows (id, code, name) to ResolveUnit.
type fakeRows struct {
	data []unitRow
	pos  int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) Next() bool                                   { r.pos++; return r.pos <= len(r.data) }
func (r *fakeRows) Scan(dst ...any) error {
	u := r.data[r.pos-1]
	*(dst[0].(*int)), *(dst[1].(*string)), *(dst[2].(*string)) = u.id, u.code, u.name
	return nil
}

type fakeQuerier struct{ units []unitRow }

func (f fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &fakeRows{data: f.units}, nil
}
func (f fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (f fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

var testUnits = []unitRow{
	{1, "EA", "Each"}, {2, "BOX", "Box"}, {6, "SQFT", "Square Foot"}, {7, "SQM", "Square Meter"}, {9, "KG", "Kilogram"},
}

func TestPickUnit(t *testing.T) {
	tests := []struct {
		uom      string
		wantID   int
		wantCode string
	}{
		{"sf", 6, "SQFT"}, {"Sq. Ft.", 6, "SQFT"}, {"m2", 7, "SQM"}, {"kgs", 9, "KG"},
		{"each", 1, "EA"}, {"", 1, "EA"}, {"furlong", 1, "EA"}, {"inches", 1, "EA"},
	}
	for _, tc := range tests {
		id, code, err := pickUnit(testUnits, tc.uom)
		require.NoError(t, err, tc.uom)
		assert.Equal(t, tc.wantID, id, tc.uom)
		assert.Equal(t, tc.wantCode, code, tc.uom)
	}
	_, _, err := pickUnit([]unitRow{{6, "SQFT", "Square Foot"}}, "zzz")
	assert.Error(t, err)
}

func TestBuildPayload_ItemPriceRules(t *testing.T) {
	q := fakeQuerier{units: testUnits}
	tests := []struct {
		side         string
		wantSell     int64
		wantPurchase int64
	}{
		{SideSales, 5000, 0},
		{SidePurchase, 0, 5000},
	}
	for _, tc := range tests {
		res, raw, err := BuildPayload(context.Background(), q, ProposeInput{
			Kind: KindItem, SourceRef: testRef, Name: " Slab\n One ", SKU: "S-1", DocUoM: "sf", UnitPriceCents: 5000, Side: tc.side})
		require.NoError(t, err, tc.side)
		assert.Equal(t, authz.ResourceInventoryItem, res)
		var p ItemPayload
		require.NoError(t, json.Unmarshal(raw, &p))
		assert.Equal(t, tc.wantSell, p.UnitPriceCents)
		assert.Equal(t, tc.wantPurchase, p.PurchaseUnitPriceCents)
		assert.Equal(t, "Slab One", p.Name)
		assert.Equal(t, 6, p.UnitID)
		assert.Equal(t, "SQFT", p.UnitCode)
	}
}

func TestBuildPayload_Errors(t *testing.T) {
	q := fakeQuerier{units: testUnits}
	tests := []struct {
		name string
		in   ProposeInput
	}{
		{"unknown kind", ProposeInput{Kind: "bogus"}},
		{"bad side", ProposeInput{Kind: KindItem, SourceRef: testRef, Name: "n", SKU: "s", Side: "x"}},
		{"negative price", ProposeInput{Kind: KindItem, SourceRef: testRef, Name: "n", SKU: "s", Side: SideSales, UnitPriceCents: -5}},
		{"item no sku", ProposeInput{Kind: KindItem, SourceRef: testRef, Name: "n", Side: SideSales}},
		{"customer no email", ProposeInput{Kind: KindCustomer, SourceRef: testRef, Name: "n"}},
		{"customer bad email", ProposeInput{Kind: KindCustomer, SourceRef: testRef, Name: "n", ContactEmail: "x"}},
		{"vendor no name", ProposeInput{Kind: KindVendor, SourceRef: testRef}},
		{"vendor masked", ProposeInput{Kind: KindVendor, SourceRef: testRef, Name: "Acme [masked]"}},
	}
	for _, tc := range tests {
		_, _, err := BuildPayload(context.Background(), q, tc.in)
		var ve *proposedaction.ValidationError
		require.True(t, errors.As(err, &ve), tc.name)
		assert.NotContains(t, ve.Message, "Acme", tc.name)
	}
}

func TestBuildPayload_CustomerAndVendor(t *testing.T) {
	ctx := context.Background()
	res, raw, err := BuildPayload(ctx, nil, ProposeInput{Kind: KindCustomer, SourceRef: testRef, Name: " Acme  Stone ",
		ContactEmail: "a@acme.example", Address: "1 Main\nSt", TaxID: "12"})
	require.NoError(t, err)
	assert.Equal(t, authz.ResourceCustomer, res)
	var cp CustomerPayload
	require.NoError(t, json.Unmarshal(raw, &cp))
	assert.Equal(t, "Acme Stone", cp.Name)
	assert.Equal(t, "1 Main St", cp.Address)

	res, raw, err = BuildPayload(ctx, nil, ProposeInput{Kind: KindVendor, SourceRef: testRef, Name: "Granite", Address: "2 Quarry Rd"})
	require.NoError(t, err)
	assert.Equal(t, authz.ResourceVendor, res)
	var vp VendorPayload
	require.NoError(t, json.Unmarshal(raw, &vp))
	assert.Equal(t, "2 Quarry Rd", vp.Address)
}
