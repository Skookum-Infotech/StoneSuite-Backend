//go:build dbtest

package orderform

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/query"
)

// These run the real edit path: the form is rewritten and the Draft Sales Order
// brought along in one transaction, and a draft staff have moved on is refused.

// placed files an order and returns it with its Sales Order's internal id.
func placed(t *testing.T, pool *pgxpool.Pool, who Submitter, in Input) (*Result, int) {
	t.Helper()
	res, err := Create(context.Background(), pool, who, in, time.Now().UTC())
	require.NoError(t, err)
	var soID int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT sales_order_id FROM customer_order_form WHERE customer_order_form_uuid = $1::uuid`,
		res.Form.ID).Scan(&soID))
	return res, soID
}

// movePastDraft is what a staff submit does to the order, without its rules.
func movePastDraft(t *testing.T, pool *pgxpool.Pool, soID int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE sales_order SET sales_order_status = rs.record_status_id
		FROM lkp_record_status rs
		JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		WHERE rt.record_type_code = 'SORD' AND rs.record_status_code = 'PAPV'
		  AND sales_order_id = $1`, soID)
	require.NoError(t, err)
}

func TestUpdate_ChangesTheFormAndBringsTheDraftAlong(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	area := uniq("Mud Room")
	in := placeable(area)
	res, soID := placed(t, pool, who, in)
	require.True(t, res.Form.Editable)

	edited := in
	edited.Areas = []Area{in.Areas[0]}
	edited.Areas[0].Color = uniq("Calm")
	edited.Areas[0].Edge = "" // the customer dropped the edge
	edited.Areas[0].Finish = uniq("Honed")
	edited.InstallAddress = "9 Quarry Rd"
	edited.InstallDate = time.Now().UTC().AddDate(0, 0, 14).Format(dateLayout)

	upd, err := Update(ctx, pool, who, res.Form.ID, edited, time.Now().UTC())

	require.NoError(t, err)
	assert.Equal(t, res.SalesOrderUUID, upd.SalesOrderUUID, "the same Sales Order, not a new one")
	assert.True(t, upd.Form.Editable)
	require.NotNil(t, upd.Form.UpdatedAt)

	got, err := Get(ctx, pool, who.CustomerID, res.Form.ID)
	require.NoError(t, err)
	assert.Equal(t, "9 Quarry Rd", got.InstallAddress)
	require.Len(t, got.Areas, 1)
	assert.Equal(t, edited.Areas[0].Color, got.Areas[0].Color)
	assert.Empty(t, got.Areas[0].Edge)
	require.NotNil(t, got.UpdatedAt)

	a := edited.Areas[0]
	assert.Equal(t, []soLine{
		{a.Color, "Color", area, 1},
		{a.Finish, "Finish", area, 1},
		{a.SinkModel, "Sink Model", area, 1},
	}, salesOrderLines(t, pool, soID), "the colour was swapped in place, the edge dropped, the finish added")

	var ship, install, memo string
	var forms int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT sales_order_ship_addr_line1, to_char(sales_order_scheduled_install_date, 'YYYY-MM-DD'), sales_order_memo,
		       (SELECT COUNT(*) FROM customer_order_form WHERE customer_id = $2)
		FROM sales_order WHERE sales_order_id = $1`, soID, who.CustomerID).Scan(&ship, &install, &memo, &forms))
	assert.Equal(t, "9 Quarry Rd", ship)
	assert.Equal(t, edited.InstallDate, install)
	assert.Contains(t, memo, "9 Quarry Rd")
	assert.Equal(t, 1, forms, "editing never makes a second order")
}

func TestUpdate_KeepsWhatStaffDidOnTheDraft(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Laundry"))
	res, soID := placed(t, pool, who, in)

	// Staff finish the sink line and add to the memo.
	_, err := pool.Exec(ctx, `
		UPDATE sales_order_item SET unit_price = 250, quantity = 12
		WHERE sales_order_id = $1 AND description = 'Sink Model'`, soID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE sales_order SET sales_order_memo = sales_order_memo || E'\nTemplate by Friday.'
		WHERE sales_order_id = $1`, soID)
	require.NoError(t, err)

	edited := in
	edited.Areas = []Area{in.Areas[0]}
	edited.Areas[0].Color = uniq("Calm")
	edited.Plan = "301"

	_, err = Update(ctx, pool, who, res.Form.ID, edited, time.Now().UTC())

	require.NoError(t, err)
	var price, qty float64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT unit_price::float8, quantity::float8 FROM sales_order_item
		WHERE sales_order_id = $1 AND description = 'Sink Model' AND item_deleted_at IS NULL`,
		soID).Scan(&price, &qty))
	assert.Equal(t, 250.0, price)
	assert.Equal(t, 12.0, qty)

	var memo string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sales_order_memo FROM sales_order WHERE sales_order_id = $1`, soID).Scan(&memo))
	assert.Contains(t, memo, "Template by Friday.", "staff's note survives")
	assert.Contains(t, memo, "Updated from the customer portal by Brandon Reyes")
	assert.Contains(t, memo, "Plan: 301")
}

func TestUpdate_IsRefusedOnceTheDraftIsMovedOn_AndChangesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Wet Bar"))
	res, soID := placed(t, pool, who, in)
	movePastDraft(t, pool, soID)
	edited := in
	edited.Builder = "Someone Else Homes"

	_, err := Update(ctx, pool, who, res.Form.ID, edited, time.Now().UTC())

	require.ErrorIs(t, err, ErrNotEditable)
	got, gerr := Get(ctx, pool, who.CustomerID, res.Form.ID)
	require.NoError(t, gerr)
	assert.Equal(t, in.Builder, got.Builder)
	assert.False(t, got.Editable, "the list and detail show it as locked")
	page, lerr := List(ctx, pool, who.CustomerID, query.Request{Limit: 10})
	require.NoError(t, lerr)
	require.Len(t, page.Orders, 1)
	assert.False(t, page.Orders[0].Editable)
}

func TestUpdate_AnotherCustomersOrderIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	owner := seedPortalCustomer(t, pool)
	other := seedPortalCustomer(t, pool)
	in := placeable(uniq("Closet"))
	res, _ := placed(t, pool, owner, in)

	_, err := Update(ctx, pool, other, res.Form.ID, in, time.Now().UTC())
	_, malformed := Update(ctx, pool, other, "not-a-uuid", in, time.Now().UTC())

	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, malformed, ErrNotFound)
}

func TestUpdate_ABadEditLeavesTheOrderAsItWas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Office"))
	res, soID := placed(t, pool, who, in)
	before := salesOrderLines(t, pool, soID)
	edited := in
	edited.Email = "not-an-email"

	_, err := Update(ctx, pool, who, res.Form.ID, edited, time.Now().UTC())

	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	got, gerr := Get(ctx, pool, who.CustomerID, res.Form.ID)
	require.NoError(t, gerr)
	assert.Equal(t, in.Email, got.Email)
	assert.Nil(t, got.UpdatedAt)
	assert.Equal(t, before, salesOrderLines(t, pool, soID))
}

func TestUpdate_RefusesAnAccountThatIsNoLongerActive(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Garage"))
	res, _ := placed(t, pool, who, in)
	_, err := pool.Exec(ctx, `
		UPDATE customer SET customer_crm_status =
			(SELECT crm_status_id FROM lkp_crm_status WHERE crm_status_code = 'CINA')
		WHERE customer_id = $1`, who.CustomerID)
	require.NoError(t, err)

	_, err = Update(ctx, pool, who, res.Form.ID, in, time.Now().UTC())

	require.ErrorIs(t, err, ErrCustomerNotActive)
}

// ---- the tenant side: a super admin changing the form from the Sales Order ----

var staffEditor = StaffEditor{Label: "Dana Admin (dana@acme.test)"}

func TestForSalesOrder_ReturnsTheFormBehindTheOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Bar"))
	res, _ := placed(t, pool, who, in)

	form, err := ForSalesOrder(ctx, pool, res.SalesOrderUUID)

	require.NoError(t, err)
	assert.Equal(t, res.Form.ID, form.ID)
	assert.Equal(t, in.Builder, form.Builder)
	require.Len(t, form.Areas, 1)
	assert.Equal(t, in.Areas[0].Color, form.Areas[0].Color)
	assert.True(t, form.Editable, "a Draft is still the customer's to change")
}

func TestForSalesOrder_AnOrderNotPlacedFromThePortalHasNoForm(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, noForm := ForSalesOrder(ctx, pool, "00000000-0000-0000-0000-000000000001")
	_, malformed := ForSalesOrder(ctx, pool, "not-a-uuid")

	assert.ErrorIs(t, noForm, ErrNotFound)
	assert.ErrorIs(t, malformed, ErrNotFound)
}

func TestStaffUpdate_BringsTheDraftAlongAndNamesTheEditorInTheMemo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	area := uniq("Bar")
	in := placeable(area)
	res, soID := placed(t, pool, who, in)
	// A note staff added to the memo makes the heading visible.
	_, err := pool.Exec(ctx, `UPDATE sales_order SET sales_order_memo = sales_order_memo || ' Template Friday.' WHERE sales_order_id = $1`, soID)
	require.NoError(t, err)

	edited := in
	edited.Areas = []Area{in.Areas[0]}
	edited.Areas[0].Color = uniq("Calm")
	edited.Plan = "512"

	upd, err := StaffUpdate(ctx, pool, res.SalesOrderUUID, edited, staffEditor, time.Now().UTC())

	require.NoError(t, err)
	assert.True(t, upd.Synced)
	assert.True(t, upd.Form.Editable)
	got, gerr := Get(ctx, pool, who.CustomerID, res.Form.ID)
	require.NoError(t, gerr)
	assert.Equal(t, edited.Areas[0].Color, got.Areas[0].Color, "the customer sees the change")
	require.NotNil(t, got.UpdatedAt)

	a := edited.Areas[0]
	assert.Equal(t, []soLine{
		{a.Color, "Color", area, 1},
		{a.Edge, "Edge", area, 1},
		{a.SinkModel, "Sink Model", area, 1},
	}, salesOrderLines(t, pool, soID))
	var memo string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sales_order_memo FROM sales_order WHERE sales_order_id = $1`, soID).Scan(&memo))
	assert.Contains(t, memo, "Template Friday.")
	assert.Contains(t, memo, "Customer order form updated by Dana Admin (dana@acme.test)")
	assert.Contains(t, memo, "Plan: 512")
}

func TestStaffUpdate_ChangesOnlyTheFormOnceTheDraftHasMovedOn(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Bar"))
	res, soID := placed(t, pool, who, in)
	movePastDraft(t, pool, soID)
	linesBefore := salesOrderLines(t, pool, soID)
	var versionBefore int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sales_order_record_version FROM sales_order WHERE sales_order_id = $1`, soID).Scan(&versionBefore))

	edited := in
	edited.Areas = []Area{in.Areas[0]}
	edited.Areas[0].Color = uniq("Calm")
	edited.Builder = "Someone Else Homes"

	upd, err := StaffUpdate(ctx, pool, res.SalesOrderUUID, edited, staffEditor, time.Now().UTC())

	require.NoError(t, err)
	assert.False(t, upd.Synced)
	assert.False(t, upd.Form.Editable, "the customer still cannot change it")
	got, gerr := Get(ctx, pool, who.CustomerID, res.Form.ID)
	require.NoError(t, gerr)
	assert.Equal(t, "Someone Else Homes", got.Builder, "the form record is changed")
	assert.Equal(t, edited.Areas[0].Color, got.Areas[0].Color)

	assert.Equal(t, linesBefore, salesOrderLines(t, pool, soID), "the Sales Order's lines are staff's now")
	var versionAfter int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sales_order_record_version FROM sales_order WHERE sales_order_id = $1`, soID).Scan(&versionAfter))
	assert.Equal(t, versionBefore, versionAfter, "the Sales Order was not saved")
}

func TestStaffUpdate_ABadChangeLeavesTheFormAsItWas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	in := placeable(uniq("Bar"))
	res, _ := placed(t, pool, who, in)
	edited := in
	edited.Zip = ""

	_, err := StaffUpdate(ctx, pool, res.SalesOrderUUID, edited, staffEditor, time.Now().UTC())

	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	got, gerr := Get(ctx, pool, who.CustomerID, res.Form.ID)
	require.NoError(t, gerr)
	assert.Equal(t, in.Zip, got.Zip)
	assert.Nil(t, got.UpdatedAt)
}

func TestStaffUpdate_AnOrderWithNoFormIsNotFound(t *testing.T) {
	pool := testPool(t)

	_, err := StaffUpdate(context.Background(), pool, "00000000-0000-0000-0000-000000000001",
		placeable(uniq("Bar")), staffEditor, time.Now().UTC())

	assert.ErrorIs(t, err, ErrNotFound)
}
