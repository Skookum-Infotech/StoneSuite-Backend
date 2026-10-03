//go:build dbtest

package fabrication

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/purchaseorder"
	"testing"
)

func TestShortagePurchaseAtomicReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	var item, vendor string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO inventory_item(inventory_item_sku,inventory_item_name,inventory_item_unit_id,inventory_item_tracking) SELECT $1,'Stone',unit_id,'serialized' FROM lkp_unit WHERE unit_code='SQFT' RETURNING inventory_item_uuid`, testActionUUID()).Scan(&item))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO vendor(record_type,vendor_status,vendor_type,vendor_legal_name,vendor_created_by) SELECT r.record_type_id,s.record_status_id,'Organization','Shortage supplier',1 FROM lkp_record_type r JOIN lkp_record_status s ON s.record_status_record_type=r.record_type_id WHERE r.record_type_code='VNDR' AND s.record_status_code='ACT_' RETURNING vendor_uuid`).Scan(&vendor))
	line := TemplateLine{SourceLineID: testActionUUID(), MaterialID: item, Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 1500, WidthMM: 600, ThicknessMM: 30}}}
	raw, err := json.Marshal([]TemplateLine{line})
	require.NoError(t, err)
	var revision string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,state,baseline,measured_lines,change_summary) SELECT fabrication_job_id,1,1,'approved','[]',$2,'{}' FROM fabrication_job WHERE fabrication_job_uuid=$1 RETURNING template_uuid`, job.ID, raw).Scan(&revision))
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	in := ShortagePurchaseInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, TemplateID: revision, VendorID: vendor, Reason: "No stock fits the measured island", Requirements: []ShortageRequirement{{SourceLineID: line.SourceLineID, Quantity: 40, UnitPrice: 12}}}
	_, err = CreateShortagePurchase(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrActionPermission)
	denied := actor
	denied.AllScope = false
	_, err = createShortagePurchase(ctx, pool, job.ID, denied, in)
	require.ErrorIs(t, err, ErrNotFound)
	invalid := in
	invalid.Requirements = []ShortageRequirement{{SourceLineID: testActionUUID(), Quantity: 40, UnitPrice: 12}}
	_, err = createShortagePurchase(ctx, pool, job.ID, actor, invalid)
	require.ErrorContains(t, err, "approved template")
	result, err := createShortagePurchase(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	require.NotEmpty(t, result.RelatedID)
	replay, err := createShortagePurchase(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	order, err := purchaseorder.Get(ctx, pool, result.RelatedID)
	require.NoError(t, err)
	require.Equal(t, "DRFT", order.StatusCode)
	require.Len(t, order.Items, 1)
	require.Equal(t, purchaseRequirementDescription(line, 1), order.Items[0].Description)
	update := purchaseorder.UpdatePurchaseOrderInput{}
	update.Items = []purchaseorder.LineInput{{LineNumber: 1, InventoryItemUUID: item, Description: order.Items[0].Description, Quantity: 40, UnitPrice: 12}}
	updated, err := purchaseorder.Update(ctx, pool, result.RelatedID, update, actor.EmployeeID)
	require.NoError(t, err)
	require.Equal(t, order.Items[0].Description, updated.Items[0].Description)

	// Linked orders expose receipt progress per unit and enforce independent PO scope.
	availability, availabilityErr := ProcurementAction(ctx, pool, job.ID, actor.IdentityID, actor.EmployeeID)
	require.NoError(t, availabilityErr)
	require.False(t, availability.Enabled)
	require.NotEmpty(t, availability.Blockers)
	_, err = ProcurementOrders(ctx, pool, job.ID, actor.IdentityID)
	require.ErrorIs(t, err, ErrPurchaseReadPermission)
	visible, err := procurementOrders(ctx, pool, job.ID, actor.IdentityID, true)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Len(t, visible[0].Lines, 1)
	require.Equal(t, result.RelatedID, visible[0].ID)
	require.Equal(t, 40.0, visible[0].Lines[0].Ordered)
	hidden, err := procurementOrders(ctx, pool, job.ID, actor.IdentityID, false)
	require.NoError(t, err)
	require.Empty(t, hidden)
	other, err := procurementOrders(ctx, pool, testActionUUID(), actor.IdentityID, true)
	require.NoError(t, err)
	require.Empty(t, other)
	_, err = pool.Exec(ctx, `UPDATE purchase_order_item SET qty_received=15 WHERE purchase_order_id=(SELECT purchase_order_id FROM purchase_order WHERE purchase_order_uuid=$1)`, result.RelatedID)
	require.NoError(t, err)
	visible, err = procurementOrders(ctx, pool, job.ID, actor.IdentityID, true)
	require.NoError(t, err)
	require.Equal(t, 15.0, visible[0].Lines[0].Received)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM fabrication_shortage_purchase p JOIN fabrication_job j USING(fabrication_job_id) WHERE j.fabrication_job_uuid=$1`, job.ID).Scan(&count))
	require.Equal(t, 1, count)
	changed := in
	changed.Reason = "Changed requirement"
	_, err = createShortagePurchase(ctx, pool, job.ID, actor, changed)
	require.ErrorIs(t, err, ErrActionConflict)
	// Revoked procurement permission must reject even an otherwise matching saved receipt.
	payload, err := json.Marshal(in)
	require.NoError(t, err)
	_, err = executeAction(ctx, pool, job.ID, actor, in.CommandMeta, actionCommand{Code: "create_shortage_po", SubjectID: revision, Payload: payload, PurchasePermission: true}, func(context.Context, pgx.Tx, actionState) error { t.Fatal("unauthorized callback"); return nil })
	require.ErrorIs(t, err, ErrPurchasePermission)
	// The shared creator must leave no draft behind when the surrounding action fails.
	failedMeta := CommandMeta{ExpectedVersion: result.Version, RequestID: testActionUUID()}
	var rolledBack string
	_, err = executeAction(ctx, pool, job.ID, actor, failedMeta, actionCommand{Code: "rollback_po", SubjectID: revision, Payload: []byte(`{}`)}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		draftInput := purchaseorder.CreatePurchaseOrderInput{VendorUUID: vendor}
		draftInput.Items = []purchaseorder.LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1, UnitPrice: 12}}
		draft, createErr := purchaseorder.CreateDraftTx(ctx, tx, draftInput, 1)
		require.NoError(t, createErr)
		rolledBack = draft.ID
		return ErrActionConflict
	})
	require.ErrorIs(t, err, ErrActionConflict)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM purchase_order WHERE purchase_order_uuid=$1`, rolledBack).Scan(&count))
	require.Zero(t, count)
	_, err = pool.Exec(ctx, `UPDATE sales_order SET sales_order_record_version=sales_order_record_version+1 WHERE sales_order_uuid=$1`, job.SalesOrderID)
	require.NoError(t, err)
	in.RequestID = testActionUUID()
	in.ExpectedVersion = result.Version
	_, err = createShortagePurchase(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrActionConflict)
	_, err = pool.Exec(ctx, `UPDATE purchase_order_item SET item_deleted_at=NOW() WHERE purchase_order_id=(SELECT purchase_order_id FROM purchase_order WHERE purchase_order_uuid=$1)`, result.RelatedID)
	require.NoError(t, err)
	visible, err = procurementOrders(ctx, pool, job.ID, actor.IdentityID, true)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Empty(t, visible[0].Lines)
	_, err = pool.Exec(ctx, `UPDATE purchase_order SET purchase_order_deleted_at=NOW(),purchase_order_deleted_by=1 WHERE purchase_order_uuid=$1`, result.RelatedID)
	require.NoError(t, err)
	visible, err = procurementOrders(ctx, pool, job.ID, actor.IdentityID, true)
	require.NoError(t, err)
	require.Empty(t, visible)

}
